package engine

// Файлы пользователей (ADR-16, ADR-60): приём картинки, реестр files, хранилище байтов.
// Байты читает только владелец (по подписанной ссылке) и прораб задачи, в снимке которой
// этот файл есть; удаление стирает байты, а не только помечает запись.

import (
	"context"
	"errors"
	"io"
	"path"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/files"
)

const localBucket = "local"

var (
	// ErrFilesDisabled — хранилище файлов не настроено (CORE_STORAGE_DIR).
	ErrFilesDisabled = errors.New("file storage is not configured")
	// ErrFileQuota — у пользователя слишком много файлов.
	ErrFileQuota = errors.New("too many files")
)

// FileView — запись реестра файлов для клиента.
type FileView struct {
	ID        uuid.UUID `json:"file_id"`
	Filename  string    `json:"filename"`
	Mime      string    `json:"mime"`
	Size      int64     `json:"size"`
	Width     *int      `json:"width,omitempty"`
	Height    *int      `json:"height,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type fileStore struct {
	store files.Store
	slots chan struct{} // одновременных пересохранений картинок: декодер держит полотно в памяти
}

// WithFiles подключает хранилище файлов; без него загрузка отвечает ErrFilesDisabled.
func (e *Engine) WithFiles(store files.Store) *Engine {
	e.files = &fileStore{store: store, slots: make(chan struct{}, max(1, runtime.NumCPU()/2))}
	return e
}

// FilesEnabled — настроено ли хранилище.
func (e *Engine) FilesEnabled() bool { return e.files != nil }

// cleanFilename — только имя (без пути), без управляющих символов, не длиннее 120 символов;
// расширение — по сохранённому типу.
func cleanFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSuffix(name, path.Ext(name))
	if r := []rune(name); len(r) > 120 {
		name = string(r[:120])
	}
	if strings.Trim(name, ". ") == "" {
		name = "image"
	}
	return name + ".jpg"
}

// UploadImage — картинка пользователя: пересохраняется (ADR-60), кладётся в хранилище,
// записывается в реестр живой.
func (e *Engine) UploadImage(ctx context.Context, userID uuid.UUID, filename string, r io.Reader) (FileView, error) {
	if e.files == nil {
		return FileView{}, ErrFilesDisabled
	}
	var count int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE user_id = $1 AND status = 'active'`, userID).Scan(&count); err != nil {
		return FileView{}, err
	}
	if count >= e.cfg.FileMaxPerUser {
		return FileView{}, ErrFileQuota
	}
	select {
	case e.files.slots <- struct{}{}:
	case <-ctx.Done():
		return FileView{}, ctx.Err()
	}
	img, err := files.NormalizeImage(r, e.cfg.FileMaxBytes, e.cfg.ImageMaxSide)
	<-e.files.slots
	if err != nil {
		return FileView{}, err
	}
	id, object := uuid.New(), uuid.New()
	if err := e.files.store.Put(ctx, object.String(), img.Data); err != nil {
		return FileView{}, err
	}
	v := FileView{ID: id, Filename: cleanFilename(filename), Mime: files.Mime, Size: int64(len(img.Data)),
		Width: &img.Width, Height: &img.Height}
	err = e.pool.QueryRow(ctx, `
		INSERT INTO files (id, user_id, bucket, object_uuid, filename, size_bytes, mime, parent_kind, status, width, height)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'draft', 'active', $8, $9) RETURNING created_at`,
		id, userID, localBucket, object, v.Filename, v.Size, v.Mime, img.Width, img.Height).Scan(&v.CreatedAt)
	if err != nil {
		_ = e.files.store.Delete(context.WithoutCancel(ctx), object.String())
		return FileView{}, err
	}
	return v, nil
}

// FileOf — живой файл пользователя.
func (e *Engine) FileOf(ctx context.Context, userID, fileID uuid.UUID) (FileView, error) {
	var v FileView
	err := e.pool.QueryRow(ctx, `SELECT id, filename, mime, size_bytes, width, height, created_at FROM files
		WHERE id = $1 AND user_id = $2 AND status = 'active'`, fileID, userID).
		Scan(&v.ID, &v.Filename, &v.Mime, &v.Size, &v.Width, &v.Height, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// OpenFile — байты живого файла по id (доступ уже проверен подписью ссылки).
func (e *Engine) OpenFile(ctx context.Context, fileID uuid.UUID) (FileView, io.ReadCloser, error) {
	if e.files == nil {
		return FileView{}, nil, ErrFilesDisabled
	}
	var v FileView
	var object uuid.UUID
	err := e.pool.QueryRow(ctx, `SELECT id, filename, mime, size_bytes, object_uuid FROM files
		WHERE id = $1 AND status = 'active'`, fileID).Scan(&v.ID, &v.Filename, &v.Mime, &v.Size, &object)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, nil, ErrNotFound
	}
	if err != nil {
		return v, nil, err
	}
	rc, err := e.files.store.Open(ctx, object.String())
	if errors.Is(err, files.ErrNotFound) {
		return v, nil, ErrNotFound
	}
	return v, rc, err
}

// DeleteFile — запись помечается удалённой, байты стираются. Факты и задачи со ссылкой на
// файл остаются: снимок задачи отметит его LOST_REF (ADR-16).
func (e *Engine) DeleteFile(ctx context.Context, userID, fileID uuid.UUID) error {
	var object uuid.UUID
	err := e.pool.QueryRow(ctx, `UPDATE files SET status = 'deleted', deleted_at = now()
		WHERE id = $1 AND user_id = $2 AND status = 'active' RETURNING object_uuid`, fileID, userID).Scan(&object)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if e.files == nil {
		return nil
	}
	return e.files.store.Delete(ctx, object.String())
}

// TaskFile — файл для прораба: задача у него в работе, и файл есть в её снимке (ADR-14:
// воркер видит только написанное в бланке).
func (e *Engine) TaskFile(ctx context.Context, acc ServiceAccount, taskID, fileID uuid.UUID) (FileView, io.ReadCloser, error) {
	var t task
	var inSnapshot bool
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		if t, err = lockTask(ctx, tx, taskID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT COALESCE(jsonb_path_exists(materialized, '$.** ? (@.file_id == $id)',
			jsonb_build_object('id', $2::text)), false) FROM system_tasks WHERE id = $1`, taskID, fileID).Scan(&inSnapshot)
	})
	if err != nil {
		return FileView{}, nil, err
	}
	if !ownedProcessing(t, acc) {
		return FileView{}, nil, ErrNotOwner
	}
	if !inSnapshot {
		return FileView{}, nil, ErrNotFound // файла нет в бланке этой задачи
	}
	return e.OpenFile(ctx, fileID)
}
