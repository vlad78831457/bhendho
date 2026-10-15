package engine

// Витрина (ADR-74): админ кладёт на неё свои опубликованные факты — сказки-примеры для посадочной страницы.
// Язык указывает админ: ядро полей факта не понимает. Посетитель видит витрину своего языка, нет такой —
// языка по умолчанию. Отдаётся опубликованная версия на момент запроса.

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/codes"
)

// ShowcaseItem — место на витрине.
type ShowcaseItem struct {
	ID        uuid.UUID      `json:"id"`
	FactID    uuid.UUID      `json:"fact_id"`
	Kind      string         `json:"kind"`
	Language  string         `json:"language"`
	Position  int            `json:"position"`
	Values    map[string]any `json:"values,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

var langCode = regexp.MustCompile(`^[a-z]{2}$`)

// AddToShowcase — админ кладёт свой опубликованный факт на витрину языка; повтор меняет порядок.
func (e *Engine) AddToShowcase(ctx context.Context, admin, factID uuid.UUID, language string, position int) (ShowcaseItem, error) {
	if !langCode.MatchString(language) {
		return ShowcaseItem{}, refuse(codes.ValidationError, "language: two lowercase letters, e.g. ru")
	}
	var ok bool
	if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM facts f JOIN fact_versions v ON v.fact_id = f.id
		WHERE f.id = $1 AND f.user_id = $2 AND v.status = 'published')`, factID, admin).Scan(&ok); err != nil {
		return ShowcaseItem{}, err
	}
	if !ok {
		return ShowcaseItem{}, refuse(codes.ValidationError, "only your own published fact goes to the showcase")
	}
	var id uuid.UUID
	err := e.pool.QueryRow(ctx, `INSERT INTO showcase (id, fact_id, language, position, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (fact_id, language) DO UPDATE SET position = EXCLUDED.position RETURNING id`,
		uuid.New(), factID, language, position, admin).Scan(&id)
	if err != nil {
		return ShowcaseItem{}, err
	}
	items, err := e.showcase(ctx, `WHERE s.id = $1`, id)
	if err != nil || len(items) == 0 {
		return ShowcaseItem{}, errors.Join(err, ErrNotFound)
	}
	return items[0], nil
}

// RemoveFromShowcase — убрать место с витрины.
func (e *Engine) RemoveFromShowcase(ctx context.Context, id uuid.UUID) error {
	tag, err := e.pool.Exec(ctx, `DELETE FROM showcase WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// AdminShowcase — вся витрина для панели.
func (e *Engine) AdminShowcase(ctx context.Context) ([]ShowcaseItem, error) {
	return e.showcase(ctx, `ORDER BY s.language, s.position, s.created_at`)
}

// PublicShowcase — витрина языка посетителя; нет на нём ни одного места — витрина языка по умолчанию.
func (e *Engine) PublicShowcase(ctx context.Context, language, fallback string) ([]ShowcaseItem, error) {
	items, err := e.showcase(ctx, `WHERE s.language = $1 ORDER BY s.position, s.created_at`, language)
	if err != nil || len(items) > 0 || language == fallback {
		return items, err
	}
	return e.showcase(ctx, `WHERE s.language = $1 ORDER BY s.position, s.created_at`, fallback)
}

func (e *Engine) showcase(ctx context.Context, tail string, args ...any) ([]ShowcaseItem, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT s.id, s.fact_id, f.kind, s.language, s.position, s.created_at, v.values
		FROM showcase s JOIN facts f ON f.id = s.fact_id
		JOIN LATERAL (SELECT values FROM fact_versions WHERE fact_id = f.id AND status = 'published'
		              ORDER BY version DESC LIMIT 1) v ON TRUE `+tail, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShowcaseItem, error) {
		var (
			it  ShowcaseItem
			raw []byte
		)
		if err := r.Scan(&it.ID, &it.FactID, &it.Kind, &it.Language, &it.Position, &it.CreatedAt, &raw); err != nil {
			return it, err
		}
		return it, json.Unmarshal(raw, &it.Values)
	})
}

// ShowcaseFallback — язык витрины по умолчанию из настроек.
func (e *Engine) ShowcaseFallback() string { return e.cfg.ShowcaseLanguage }
