package engine

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/forms"
)

// FactVersion — версия факт-бланка.
type FactVersion struct {
	Version       int            `json:"version"`
	Status        string         `json:"status"`
	SchemaVersion int            `json:"schema_version"`
	Values        map[string]any `json:"values"`
	CreatedAt     time.Time      `json:"created_at"`
}

// Fact — факт (id_db) со всеми версиями, новые сверху (ADR-13/15).
type Fact struct {
	ID               uuid.UUID     `json:"fact_id"`
	Kind             string        `json:"kind"`
	Origin           string        `json:"origin"`
	PublishedVersion *int          `json:"published_version"`
	Versions         []FactVersion `json:"versions"`
	// Только в списке: статус последней версии, время создания и выбранные поля опубликованной.
	Status    string         `json:"status,omitempty"`
	CreatedAt *time.Time     `json:"created_at,omitempty"`
	Summary   map[string]any `json:"summary,omitempty"`
}

// CreateFact создаёт факт в статусе draft.
func (e *Engine) CreateFact(ctx context.Context, userID uuid.UUID, formID string, rawValues []byte) (Fact, error) {
	values, err := decodeValues(rawValues)
	if err != nil {
		return Fact{}, &Refusal{Code: codes.ValidationError, Detail: err.Error()}
	}
	id := uuid.New()
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		t, err := publishedTemplate(ctx, tx, formID, "fact")
		if err != nil {
			return err
		}
		if err := checkFactValues(ctx, tx, userID, t, values); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO facts (id, user_id, kind) VALUES ($1, $2, $3)`, id, userID, formID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO fact_versions (fact_id, version, schema_version, status, values) VALUES ($1, 1, $2, 'draft', $3)`,
			id, t.Version, values); err != nil {
			return err
		}
		return factChanged(ctx, tx, userID, id, 1, "draft")
	})
	if err != nil {
		return Fact{}, err
	}
	return e.GetFact(ctx, userID, id)
}

// UpdateFact: черновик правится на месте, иначе появляется новая draft-версия (5.9, факты).
func (e *Engine) UpdateFact(ctx context.Context, userID, factID uuid.UUID, rawValues []byte) (Fact, error) {
	values, err := decodeValues(rawValues)
	if err != nil {
		return Fact{}, &Refusal{Code: codes.ValidationError, Detail: err.Error()}
	}
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		kind, err := lockFact(ctx, tx, userID, factID)
		if err != nil {
			return err
		}
		t, err := publishedTemplate(ctx, tx, kind, "fact")
		if err != nil {
			return err
		}
		if err := checkFactValues(ctx, tx, userID, t, values); err != nil {
			return err
		}
		var last int
		var status string
		if err := tx.QueryRow(ctx, `SELECT version, status FROM fact_versions WHERE fact_id = $1
			ORDER BY version DESC LIMIT 1`, factID).Scan(&last, &status); err != nil {
			return err
		}
		switch status {
		case "deleted":
			return refuse(codes.CommandRefused, "fact is deleted")
		case "draft":
			_, err = tx.Exec(ctx, `UPDATE fact_versions SET values = $3, schema_version = $4, updated_at = now()
				WHERE fact_id = $1 AND version = $2`, factID, last, values, t.Version)
		default:
			last++
			_, err = tx.Exec(ctx, `INSERT INTO fact_versions (fact_id, version, schema_version, status, values)
				VALUES ($1, $2, $3, 'draft', $4)`, factID, last, t.Version, values)
		}
		if err != nil {
			return err
		}
		return factChanged(ctx, tx, userID, factID, last, "draft")
	})
	if err != nil {
		return Fact{}, err
	}
	return e.GetFact(ctx, userID, factID)
}

// PublishFact публикует версию (0 — последнюю); прежняя опубликованная уходит в archived,
// id_db сдвигается на опубликованную (ADR-15, ADR-22 п.4).
func (e *Engine) PublishFact(ctx context.Context, userID, factID uuid.UUID, version int) (Fact, error) {
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := lockFact(ctx, tx, userID, factID); err != nil {
			return err
		}
		if version == 0 {
			if err := tx.QueryRow(ctx, `SELECT max(version) FROM fact_versions WHERE fact_id = $1`, factID).Scan(&version); err != nil {
				return err
			}
		}
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM fact_versions WHERE fact_id = $1 AND version = $2`, factID, version).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "draft" && status != "archived" {
			return refuse(codes.CommandRefused, "publish is not allowed from %s", status)
		}
		if _, err := tx.Exec(ctx, `UPDATE fact_versions SET status = 'archived', updated_at = now()
			WHERE fact_id = $1 AND status = 'published'`, factID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE fact_versions SET status = 'published', updated_at = now()
			WHERE fact_id = $1 AND version = $2`, factID, version); err != nil {
			return err
		}
		return factChanged(ctx, tx, userID, factID, version, "published")
	})
	if err != nil {
		return Fact{}, err
	}
	return e.GetFact(ctx, userID, factID)
}

// DeleteFact — tombstone всех версий; ссылками не блокируется: снимки задач хранят тела (ADR-15).
func (e *Engine) DeleteFact(ctx context.Context, userID, factID uuid.UUID) error {
	return pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := lockFact(ctx, tx, userID, factID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE fact_versions SET status = 'deleted', updated_at = now()
			WHERE fact_id = $1`, factID); err != nil {
			return err
		}
		var last int
		if err := tx.QueryRow(ctx, `SELECT max(version) FROM fact_versions WHERE fact_id = $1`, factID).Scan(&last); err != nil {
			return err
		}
		return factChanged(ctx, tx, userID, factID, last, "deleted")
	})
}

// GetFact — факт пользователя со всеми версиями.
func (e *Engine) GetFact(ctx context.Context, userID, factID uuid.UUID) (Fact, error) {
	f := Fact{ID: factID}
	err := e.pool.QueryRow(ctx, `SELECT kind, origin FROM facts WHERE id = $1 AND user_id = $2`, factID, userID).Scan(&f.Kind, &f.Origin)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	rows, err := e.pool.Query(ctx, `SELECT version, status, schema_version, values, created_at
		FROM fact_versions WHERE fact_id = $1 ORDER BY version DESC`, factID)
	if err != nil {
		return f, err
	}
	f.Versions, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (FactVersion, error) {
		var v FactVersion
		var raw []byte
		if err := r.Scan(&v.Version, &v.Status, &v.SchemaVersion, &raw, &v.CreatedAt); err != nil {
			return v, err
		}
		v.Values, err = decodeValues(raw)
		return v, err
	})
	for _, v := range f.Versions {
		if v.Status == "published" {
			n := v.Version
			f.PublishedVersion = &n
			break
		}
	}
	return f, err
}

// FactListOptions — что выбрать в списке фактов.
type FactListOptions struct {
	Kind string
	// Fields — поля верхнего уровня опубликованной версии в summary (для карточек списка:
	// название, язык…) без тяжёлых тел; пусто — без summary.
	Fields []string
	// Live — без удалённых фактов.
	Live bool
}

// ListFacts — факты пользователя, новые сверху, без тел версий.
func (e *Engine) ListFacts(ctx context.Context, userID uuid.UUID, o FactListOptions) ([]Fact, error) {
	if o.Fields == nil {
		o.Fields = []string{}
	}
	rows, err := e.pool.Query(ctx, `
		SELECT f.id, f.kind, f.origin, f.created_at, pub.version, last.status,
		       (SELECT jsonb_object_agg(k, pub.values -> k) FROM unnest($3::text[]) k WHERE pub.values ? k)
		FROM facts f
		LEFT JOIN LATERAL (SELECT version, values FROM fact_versions v
		                   WHERE v.fact_id = f.id AND v.status = 'published' ORDER BY version DESC LIMIT 1) pub ON true
		JOIN LATERAL (SELECT status FROM fact_versions v WHERE v.fact_id = f.id ORDER BY version DESC LIMIT 1) last ON true
		WHERE f.user_id = $1 AND ($2 = '' OR f.kind = $2) AND (NOT $4 OR last.status <> 'deleted')
		ORDER BY f.created_at DESC LIMIT 500`, userID, o.Kind, o.Fields, o.Live)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Fact, error) {
		var f Fact
		var created time.Time
		var summary []byte
		err := r.Scan(&f.ID, &f.Kind, &f.Origin, &created, &f.PublishedVersion, &f.Status, &summary)
		f.CreatedAt = &created
		f.Versions = []FactVersion{}
		if err == nil && summary != nil {
			f.Summary, err = decodeValues(summary)
		}
		return f, err
	})
}

func lockFact(ctx context.Context, tx pgx.Tx, userID, factID uuid.UUID) (string, error) {
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM facts WHERE id = $1 AND user_id = $2 FOR UPDATE`, factID, userID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return kind, err
}

// checkFactValues — форма значений и живость ссылок факта (факт → факт своего владельца).
func checkFactValues(ctx context.Context, tx pgx.Tx, userID uuid.UUID, t template, values map[string]any) error {
	errs, refs := forms.Validate(t.Rules, values)
	for _, r := range refs.Facts {
		var kind string
		err := tx.QueryRow(ctx, `SELECT kind FROM facts WHERE id = $1 AND user_id = $2`, r.ID, userID).Scan(&kind)
		if err != nil || kind != r.ExpectedKind {
			errs = append(errs, forms.FieldError{Field: r.Field, Message: "fact not found or of another kind"})
		}
	}
	errs = append(errs, checkFiles(ctx, tx, userID, refs.Files)...)
	if len(errs) > 0 {
		return &Refusal{Code: codes.ValidationError, FieldErrors: errs}
	}
	return nil
}

// checkFiles — файл должен быть active, принадлежать пользователю (ADR-16) и подходить по типу
// (allowed_file_types поля).
func checkFiles(ctx context.Context, tx pgx.Tx, userID uuid.UUID, refs []forms.Ref) []forms.FieldError {
	var errs []forms.FieldError
	for _, r := range refs {
		var mime string
		err := tx.QueryRow(ctx, `SELECT mime FROM files WHERE id = $1 AND user_id = $2 AND status = 'active'`,
			r.ID, userID).Scan(&mime)
		switch {
		case err != nil:
			errs = append(errs, forms.FieldError{Field: r.Field, Message: "file not found or deleted"})
		case len(r.AllowedTypes) > 0 && !slices.Contains(r.AllowedTypes, mime):
			errs = append(errs, forms.FieldError{Field: r.Field, Message: "file type " + mime + " is not allowed here"})
		}
	}
	return errs
}

func factChanged(ctx context.Context, tx pgx.Tx, userID, factID uuid.UUID, version int, status string) error {
	return emit(ctx, tx, userID, "fact.changed", map[string]any{
		"fact_id": factID.String(), "version": version, "status": status,
	})
}
