package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/forms"
	"offgrid/core/internal/core/modules"
)

// querier — пул или транзакция.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// template — версия шаблона формы с сервисом-исполнителем.
type template struct {
	FormID      string
	Version     int
	Kind        string
	Title       string
	MetaUI      map[string]any
	Rules       forms.Rules
	Price       decimal.Decimal
	Currency    string
	Status      string
	ServiceID   *uuid.UUID
	ServiceCode string
	Accounting  *modules.Accounting
	Result      *modules.ResultSpec // факт-результат (ADR-51)
}

const templateSelect = `
	SELECT f.form_id, f.version, f.kind, f.title, f.meta_ui, f.price::text, f.currency_id, f.status,
	       f.service_id, COALESCE(s.code, ''), s.accounting, f.result_spec
	FROM forms f LEFT JOIN services s ON s.id = f.service_id`

func scanTemplate(row pgx.Row) (template, error) {
	var (
		t        template
		meta     []byte
		price    string
		acctJSON []byte
		resJSON  []byte
	)
	if err := row.Scan(&t.FormID, &t.Version, &t.Kind, &t.Title, &meta, &price, &t.Currency, &t.Status,
		&t.ServiceID, &t.ServiceCode, &acctJSON, &resJSON); err != nil {
		return t, err
	}
	t.Price = decimal.RequireFromString(price)
	if err := json.Unmarshal(meta, &t.MetaUI); err != nil {
		return t, err
	}
	rawRules, _ := json.Marshal(t.MetaUI["fields_rules"])
	if err := json.Unmarshal(rawRules, &t.Rules); err != nil {
		return t, fmt.Errorf("form %s v%d: fields_rules: %w", t.FormID, t.Version, err)
	}
	if len(acctJSON) > 0 && string(acctJSON) != "null" {
		t.Accounting = &modules.Accounting{}
		if err := json.Unmarshal(acctJSON, t.Accounting); err != nil {
			return t, err
		}
	}
	if len(resJSON) > 0 && string(resJSON) != "null" {
		t.Result = &modules.ResultSpec{}
		if err := json.Unmarshal(resJSON, t.Result); err != nil {
			return t, err
		}
	}
	return t, nil
}

// publishedTemplate — последняя опубликованная версия формы нужного вида.
func publishedTemplate(ctx context.Context, q querier, formID, kind string) (template, error) {
	t, err := scanTemplate(q.QueryRow(ctx, templateSelect+`
		WHERE f.form_id = $1 AND f.kind = $2 AND f.status = 'published'
		ORDER BY f.version DESC LIMIT 1`, formID, kind))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("form %q: %w", formID, ErrNotFound)
	}
	return t, err
}

// templateVersion — конкретная версия (задача фиксирует schema_version, ADR-19).
func templateVersion(ctx context.Context, q querier, formID string, version int) (template, error) {
	t, err := scanTemplate(q.QueryRow(ctx, templateSelect+`
		WHERE f.form_id = $1 AND f.version = $2`, formID, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("form %q v%d: %w", formID, version, ErrNotFound)
	}
	return t, err
}

// CatalogItem — строка каталога (5.7, поток 1).
type CatalogItem struct {
	FormID        string `json:"form_id"`
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	Service       string `json:"service,omitempty"`
	Price         string `json:"price"`
	CurrencyID    string `json:"currency_id"`
	Category      any    `json:"category,omitempty"`
}

// catalogCache — каталог инстанса на CatalogTTL: он меняется только при регистрации модулей,
// а читается на каждом входе в приложение (ADR-55). Регистрация в этом инстансе сбрасывает кэш;
// изменения из других инстансов видны не позже чем через TTL.
type catalogCache struct {
	mu    sync.Mutex
	at    time.Time
	items []CatalogItem
}

func (c *catalogCache) reset() {
	c.mu.Lock()
	c.items = nil
	c.mu.Unlock()
}

// Catalog — опубликованные формы (read-only, FR-IF-1).
func (e *Engine) Catalog(ctx context.Context) ([]CatalogItem, error) {
	if e.cfg.CatalogTTL <= 0 {
		return e.loadCatalog(ctx)
	}
	e.catalog.mu.Lock()
	defer e.catalog.mu.Unlock()
	if e.catalog.items != nil && e.now().Sub(e.catalog.at) < e.cfg.CatalogTTL {
		return e.catalog.items, nil
	}
	items, err := e.loadCatalog(ctx)
	if err != nil {
		return nil, err
	}
	e.catalog.items, e.catalog.at = items, e.now()
	return items, nil
}

func (e *Engine) loadCatalog(ctx context.Context) ([]CatalogItem, error) {
	rows, err := e.pool.Query(ctx, templateSelect+`
		WHERE f.status = 'published' ORDER BY f.kind DESC, f.form_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CatalogItem{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, CatalogItem{
			FormID: t.FormID, SchemaVersion: t.Version, Kind: t.Kind, Title: t.Title,
			Service: t.ServiceCode, Price: t.Price.StringFixed(4), CurrencyID: t.Currency,
			Category: t.MetaUI["category"],
		})
	}
	return out, rows.Err()
}

// Template — шаблон бланка для фронта (5.7, поток 2): system + meta_ui, values пустые.
func (e *Engine) Template(ctx context.Context, formID string) (map[string]any, error) {
	rows, err := e.pool.Query(ctx, templateSelect+`
		WHERE f.form_id = $1 AND f.status = 'published' ORDER BY f.version DESC LIMIT 1`, formID)
	if err != nil {
		return nil, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, func(r pgx.CollectableRow) (template, error) { return scanTemplate(r) })
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"system": map[string]any{
			"form_id": t.FormID, "schema_version": t.Version, "kind": t.Kind,
			"target_service": t.ServiceCode, "price": t.Price.StringFixed(4), "currency_id": t.Currency,
		},
		"meta_ui": metaWithPrice(t),
	}, nil
}

func metaWithPrice(t template) map[string]any {
	meta := make(map[string]any, len(t.MetaUI)+1)
	for k, v := range t.MetaUI {
		meta[k] = v
	}
	if t.Kind == "task" {
		p, _ := t.Price.Float64()
		meta["price_gc"] = p
	}
	return meta
}
