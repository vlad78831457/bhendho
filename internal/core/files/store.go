package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// ErrNotFound — объекта в хранилище нет.
var ErrNotFound = errors.New("object not found")

// Store — где лежат байты файлов. Сейчас — каталог (том Docker, общий для реплик ядра);
// для облака — S3-совместимое хранилище тем же интерфейсом.
type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}

var safeKey = regexp.MustCompile(`^[0-9a-f-]{36}$`)

// Local — хранилище в каталоге: ключ — UUID объекта, файлы без расширений и прав на исполнение.
type Local struct{ Dir string }

func (l Local) path(key string) (string, error) {
	if !safeKey.MatchString(key) {
		return "", errors.New("bad object key")
	}
	return filepath.Join(l.Dir, key[:2], key), nil
}

// Put пишет во временный файл и переименовывает: читатель не увидит половину файла.
func (l Local) Put(_ context.Context, key string, data []byte) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".up-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func (l Local) Open(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

func (l Local) Delete(_ context.Context, key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
