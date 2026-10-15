package engine

// Чтение и запись для панели админа (ADR-66): подразделения и их бланки, последние факты подразделения,
// факт от админа, настройки «как есть» без секретов. Права проверяет HTTP-слой (роль admin, ADR-61).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/codes"
)

// AdminForm — бланк подразделения для обзора: что за форма и какие у неё поля.
type AdminForm struct {
	FormID   string         `json:"form_id"`
	Version  int            `json:"version"`
	Kind     string         `json:"kind"`
	Audience string         `json:"audience"`
	Title    string         `json:"title"`
	Price    string         `json:"price"`
	Fields   map[string]any `json:"fields"`
}

// AdminService — подразделение со всеми опубликованными бланками.
type AdminService struct {
	Code          string      `json:"code"`
	Title         string      `json:"title"`
	ModuleID      string      `json:"module_id"`
	ModuleVersion string      `json:"module_version"`
	Accounting    any         `json:"accounting,omitempty"`
	Forms         []AdminForm `json:"forms"`
}

// AdminServices — все подразделения и их бланки (схема бланков для админа).
func (e *Engine) AdminServices(ctx context.Context) ([]AdminService, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT s.code, s.title, s.module_id, s.module_version, s.accounting,
		       f.form_id, f.version, f.kind, f.audience, f.title, f.price::text, f.meta_ui
		FROM services s LEFT JOIN forms f ON f.service_id = s.id AND f.status = 'published'
		ORDER BY s.code, f.kind DESC, f.form_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminService{}
	for rows.Next() {
		var (
			svc                                  AdminService
			accounting, meta                     []byte
			formID, kind, audience, title, price *string
			version                              *int
		)
		if err := rows.Scan(&svc.Code, &svc.Title, &svc.ModuleID, &svc.ModuleVersion, &accounting,
			&formID, &version, &kind, &audience, &title, &price, &meta); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].Code != svc.Code {
			if len(accounting) > 0 {
				_ = json.Unmarshal(accounting, &svc.Accounting)
			}
			svc.Forms = []AdminForm{}
			out = append(out, svc)
		}
		if formID == nil {
			continue
		}
		f := AdminForm{FormID: *formID, Version: *version, Kind: *kind, Audience: *audience, Title: *title, Price: *price}
		var m map[string]any
		if json.Unmarshal(meta, &m) == nil {
			f.Fields, _ = m["fields_rules"].(map[string]any)
		}
		out[len(out)-1].Forms = append(out[len(out)-1].Forms, f)
	}
	return out, rows.Err()
}

// ServiceFact — факт подразделения: семь полей ядра и содержимое.
type ServiceFact struct {
	ID            int64          `json:"id"`
	Service       string         `json:"service"`
	FormID        string         `json:"form_id"`
	SchemaVersion int            `json:"schema_version"`
	Audience      string         `json:"audience"`
	TaskID        *uuid.UUID     `json:"task_id"`
	UserID        *uuid.UUID     `json:"user_id"`
	Writer        string         `json:"writer"` // prorab:<имя> или admin:<id>
	CreatedAt     time.Time      `json:"created_at"`
	Values        map[string]any `json:"values"`
}

const serviceFactSelect = `
	SELECT f.id, s.code, f.form_id, f.schema_version, f.audience, f.task_id, f.user_id, f.writer, f.created_at, f.values
	FROM service_facts f JOIN services s ON s.id = f.service_id`

func scanServiceFact(row pgx.Row) (ServiceFact, error) {
	var (
		f   ServiceFact
		raw []byte
	)
	err := row.Scan(&f.ID, &f.Service, &f.FormID, &f.SchemaVersion, &f.Audience, &f.TaskID, &f.UserID, &f.Writer, &f.CreatedAt, &raw)
	if err == nil {
		err = json.Unmarshal(raw, &f.Values)
	}
	return f, err
}

// LatestServiceFacts — последние факты подразделения по бланку, новые сверху (limit 1…100).
func (e *Engine) LatestServiceFacts(ctx context.Context, service, formID string, limit int) ([]ServiceFact, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := e.pool.Query(ctx, serviceFactSelect+`
		WHERE s.code = $1 AND f.form_id = $2 ORDER BY f.created_at DESC, f.id DESC LIMIT $3`, service, formID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceFact{}
	for rows.Next() {
		f, err := scanServiceFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// WriteServiceFact — факт подразделения от админа: без задачи и пользователя, «кто прислал» — admin:<id>.
func (e *Engine) WriteServiceFact(ctx context.Context, adminID uuid.UUID, service, formID string, values map[string]any) (ServiceFact, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return ServiceFact{}, err
	}
	if values == nil || len(raw) > maxServiceFactBytes {
		return ServiceFact{}, &Refusal{Code: codes.ValidationError, Detail: "values: an object up to 256 KB is required"}
	}
	var out ServiceFact
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		version, audience, why, err := departmentForm(ctx, tx, service, formID)
		if err != nil {
			return err
		}
		if why != "" {
			return &Refusal{Code: codes.ValidationError, Detail: why}
		}
		var id int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO service_facts (service_id, form_id, schema_version, audience, writer, values)
			SELECT s.id, $2, $3, $4, $5, $6 FROM services s WHERE s.code = $1 RETURNING id`,
			service, formID, version, audience, "admin:"+adminID.String(), raw).Scan(&id); err != nil {
			return err
		}
		out, err = scanServiceFact(tx.QueryRow(ctx, serviceFactSelect+` WHERE f.id = $1`, id))
		return err
	})
	return out, err
}

// PublicSettings — настройки ядра «как есть» для панели: только числа и включатели, без секретов,
// токенов, адресов баз и ключей провайдеров.
func (e *Engine) PublicSettings() map[string]any {
	c := e.cfg
	return map[string]any{
		"default_currency":        c.DefaultCurrency,
		"overdraft_factor":        c.OverdraftFactor,
		"demo_deposit":            c.DemoDepositEnabled,
		"demo_deposit_max":        c.DemoDepositMax,
		"promo_max_amount":        c.PromoMaxAmount,
		"promo_max_failures":      c.PromoMaxFailures,
		"promo_failure_window":    c.PromoFailureWindow.String(),
		"topup_mark_ttl":          c.TopupMarkTTL.String(),
		"access_ttl":              c.AccessTTL.String(),
		"refresh_ttl":             c.RefreshTTL.String(),
		"accept_timeout":          c.AcceptTimeout.String(),
		"lock_multiplier":         c.LockMultiplier,
		"lock_ceiling":            c.LockCeiling.String(),
		"default_eta":             c.DefaultETA.String(),
		"max_attempts":            c.MaxAttempts,
		"task_retention":          c.TaskRetention.String(),
		"trace_ttl":               c.TraceTTL.String(),
		"file_storage":            c.StorageDir != "",
		"file_max_bytes":          c.FileMaxBytes,
		"file_max_per_user":       c.FileMaxPerUser,
		"image_max_side":          c.ImageMaxSide,
		"file_link_ttl":           c.FileLinkTTL.String(),
		"catalog_ttl":             c.CatalogTTL.String(),
		"public_url":              c.PublicURL,
		"prorabs_from_config":     len(c.ProrabTokens),
		"promo_issuers_from_conf": len(c.PromoIssuers),
	}
}
