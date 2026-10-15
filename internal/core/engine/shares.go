package engine

// «Поделиться» (ADR-73): хозяин даёт ссылку на версию своего факта — только чтение, отзывается в любой момент;
// получатель может сохранить копию к себе — это его новый факт, оригинал не меняется. Ядро не знает, что
// внутри факта: делиться можно сказкой, героем, итогом любого подразделения. Файлы (фото) — только по
// подписанным ссылкам на чтение; в копию они не попадают: файл остаётся собственностью хозяина (ADR-42).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/codes"
)

// Share — ссылка «поделиться» без самой ссылки (она показывается один раз).
type Share struct {
	ID        uuid.UUID  `json:"share_id"`
	FactID    uuid.UUID  `json:"fact_id"`
	Version   int        `json:"version"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// SharedFact — то, что видит получатель ссылки.
type SharedFact struct {
	Kind       string         `json:"kind"`
	Title      string         `json:"title"`
	Version    int            `json:"version"`
	Values     map[string]any `json:"values"`
	CreatedAt  time.Time      `json:"created_at"`
	FileFields []string       `json:"-"` // поля-файлы формы: HTTP-слой подписывает их на чтение
}

// CreateShare — ссылка на версию факта хозяина; version 0 — опубликованная. Делятся опубликованной или
// архивной версией, не черновиком и не удалённым.
func (e *Engine) CreateShare(ctx context.Context, owner, factID uuid.UUID, version int) (Share, string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Share{}, "", err
	}
	token := "sh_" + hex.EncodeToString(b[:])
	sh := Share{ID: uuid.New(), FactID: factID}
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `
			SELECT v.version, v.status FROM fact_versions v JOIN facts f ON f.id = v.fact_id
			WHERE f.id = $1 AND f.user_id = $2 AND (($3 = 0 AND v.status = 'published') OR v.version = $3)
			ORDER BY v.version DESC LIMIT 1`, factID, owner, version).Scan(&sh.Version, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "published" && status != "archived" {
			return refuse(codes.CommandRefused, "only a published or archived version can be shared, not %s", status)
		}
		return tx.QueryRow(ctx, `INSERT INTO shares (id, token_hash, owner_id, fact_id, version) VALUES ($1, $2, $3, $4, $5)
			RETURNING created_at`, sh.ID, hashToken(token), owner, factID, sh.Version).Scan(&sh.CreatedAt)
	})
	return sh, token, err
}

// Shares — ссылки хозяина на факт, новые сверху.
func (e *Engine) Shares(ctx context.Context, owner, factID uuid.UUID) ([]Share, error) {
	rows, err := e.pool.Query(ctx, `SELECT id, fact_id, version, created_at, revoked_at FROM shares
		WHERE owner_id = $1 AND fact_id = $2 ORDER BY created_at DESC`, owner, factID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Share, error) {
		var s Share
		return s, r.Scan(&s.ID, &s.FactID, &s.Version, &s.CreatedAt, &s.RevokedAt)
	})
}

// RevokeShare — отозвать ссылку; повтор безопасен.
func (e *Engine) RevokeShare(ctx context.Context, owner, shareID uuid.UUID) error {
	tag, err := e.pool.Exec(ctx, `UPDATE shares SET revoked_at = COALESCE(revoked_at, now())
		WHERE id = $1 AND owner_id = $2`, shareID, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Shared — факт по ссылке. Отозванная ссылка и удалённый факт неотличимы от несуществующих.
func (e *Engine) Shared(ctx context.Context, token string) (SharedFact, error) {
	var (
		out    SharedFact
		raw    []byte
		status string
		schema int
	)
	err := e.pool.QueryRow(ctx, `
		SELECT f.kind, v.version, v.values, v.created_at, v.status, v.schema_version
		FROM shares s JOIN facts f ON f.id = s.fact_id JOIN fact_versions v ON v.fact_id = s.fact_id AND v.version = s.version
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL`, hashToken(token)).
		Scan(&out.Kind, &out.Version, &raw, &out.CreatedAt, &status, &schema)
	if errors.Is(err, pgx.ErrNoRows) || status == "deleted" {
		return SharedFact{}, ErrNotFound
	}
	if err != nil {
		return SharedFact{}, err
	}
	if err := json.Unmarshal(raw, &out.Values); err != nil {
		return SharedFact{}, err
	}
	if tpl, err := templateVersion(ctx, e.pool, out.Kind, schema); err == nil {
		out.Title = tpl.Title
		for name, rule := range tpl.Rules {
			if rule.Type == "file" {
				out.FileFields = append(out.FileFields, name)
			}
		}
	}
	return out, nil
}

// CopyShared — получатель сохраняет копию к себе: новый опубликованный факт без файлов хозяина.
func (e *Engine) CopyShared(ctx context.Context, user uuid.UUID, token string) (Fact, error) {
	sf, err := e.Shared(ctx, token)
	if err != nil {
		return Fact{}, err
	}
	for _, f := range sf.FileFields {
		delete(sf.Values, f)
	}
	raw, err := json.Marshal(sf.Values)
	if err != nil {
		return Fact{}, err
	}
	f, err := e.CreateFact(ctx, user, sf.Kind, raw)
	if err != nil {
		return Fact{}, err
	}
	return e.PublishFact(ctx, user, f.ID, 0)
}
