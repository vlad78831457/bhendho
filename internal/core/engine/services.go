package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/modules"
)

// RegisterModule регистрирует сервис модуля и его формы (ADR-18/34).
// Идемпотентна: изменившаяся форма получает новую версию, прежняя снимается (ADR-19).
func (e *Engine) RegisterModule(ctx context.Context, m modules.Module) error {
	defer e.catalog.reset()
	if m.Manifest.Mode != "worker_service" {
		return fmt.Errorf("%s: mode %q: in-process Go modules are not registered from disk", m.Manifest.ModuleID, m.Manifest.Mode)
	}
	return pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		// Инстансы ядра стартуют параллельно — регистрация идёт по одной.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('offgrid.module_registry'))`); err != nil {
			return err
		}
		serviceID, err := e.upsertService(ctx, tx, m)
		if err != nil {
			return err
		}
		for _, f := range m.Forms {
			if err := upsertForm(ctx, tx, serviceID, e.cfg.DefaultCurrency, f); err != nil {
				return err
			}
		}
		return nil
	})
}

func (e *Engine) upsertService(ctx context.Context, tx pgx.Tx, m modules.Module) (uuid.UUID, error) {
	var accounting any
	if m.Manifest.Accounting != nil {
		accounting = m.Manifest.Accounting
	}
	code := m.TargetService()
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM services WHERE code = $1 FOR UPDATE`, code).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		id = uuid.New()
		walletID := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO wallets (id, owner_kind, owner_id, currency_id) VALUES ($1, 'service', $2, $3)`,
			walletID, id, e.cfg.DefaultCurrency); err != nil {
			return id, err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO services (id, code, module_id, module_version, title, currency_id, accounting, target_wallet_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, code, m.Manifest.ModuleID, m.Manifest.Version, m.Manifest.Name, e.cfg.DefaultCurrency, accounting, walletID)
		return id, err
	case err != nil:
		return id, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE services SET module_id = $2, module_version = $3, title = $4, accounting = $5, updated_at = now()
		WHERE id = $1`, id, m.Manifest.ModuleID, m.Manifest.Version, m.Manifest.Name, accounting)
	return id, err
}

func upsertForm(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, currency string, f modules.Form) error {
	var (
		version   int
		metaRaw   []byte
		priceStr  string
		resultRaw []byte
	)
	var resultSpec any
	if f.Ref.Result != nil {
		resultSpec = f.Ref.Result
	}
	err := tx.QueryRow(ctx, `
		SELECT version, meta_ui, price::text, result_spec FROM forms
		WHERE form_id = $1 ORDER BY version DESC LIMIT 1 FOR UPDATE`, f.Ref.FormID).Scan(&version, &metaRaw, &priceStr, &resultRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil {
		var current map[string]any
		if err := json.Unmarshal(metaRaw, &current); err != nil {
			return err
		}
		fresh, err := roundTrip(f.MetaUI)
		if err != nil {
			return err
		}
		sameResult, err := sameJSON(resultRaw, resultSpec)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(current, fresh) && sameResult && decimal.RequireFromString(priceStr).Equal(f.Ref.BasePrice) {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE forms SET status = 'withdrawn' WHERE form_id = $1 AND status = 'published'`, f.Ref.FormID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO forms (form_id, version, kind, service_id, title, meta_ui, price, currency_id, status, result_spec)
		VALUES ($1, $2, $3, $4, $5, $6, $7::numeric, $8, 'published', $9)`,
		f.Ref.FormID, version+1, f.Ref.Kind, serviceID, f.Ref.Title, f.MetaUI, f.Ref.BasePrice.String(), currency, resultSpec)
	return err
}

func roundTrip(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, json.Unmarshal(raw, &out)
}

// ServiceAccount — аутентифицированный прораб (ADR-18/30).
type ServiceAccount struct {
	ID          uuid.UUID
	ServiceID   uuid.UUID
	ServiceCode string
	Name        string
}

// IssueServiceToken создаёт прораба сервиса и возвращает его токен (показывается один раз).
func (e *Engine) IssueServiceToken(ctx context.Context, serviceCode, name string) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := "sa_" + hex.EncodeToString(b[:])
	tag, err := e.pool.Exec(ctx, `
		INSERT INTO service_accounts (id, service_id, name, token_hash)
		SELECT $1, id, $3, $4 FROM services WHERE code = $2`,
		uuid.New(), serviceCode, name, hashToken(token))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("service %q: %w", serviceCode, ErrNotFound)
	}
	return token, nil
}

// AuthenticateService находит прораба по токену.
func (e *Engine) AuthenticateService(ctx context.Context, token string) (ServiceAccount, error) {
	var a ServiceAccount
	err := e.pool.QueryRow(ctx, `
		SELECT a.id, a.service_id, s.code, a.name FROM service_accounts a
		JOIN services s ON s.id = a.service_id
		WHERE a.token_hash = $1 AND a.revoked_at IS NULL`, hashToken(token)).
		Scan(&a.ID, &a.ServiceID, &a.ServiceCode, &a.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// sameJSON сравнивает сохранённый JSONB с новым значением по смыслу.
func sameJSON(stored []byte, fresh any) (bool, error) {
	if fresh == nil {
		return len(stored) == 0 || string(stored) == "null", nil
	}
	if len(stored) == 0 {
		return false, nil
	}
	var a, b any
	if err := json.Unmarshal(stored, &a); err != nil {
		return false, err
	}
	raw, err := json.Marshal(fresh)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, err
	}
	return reflect.DeepEqual(a, b), nil
}

// ProrabToken — учётка прораба, заданная оператором в конфиге (ADR-52).
type ProrabToken struct {
	Service, Name, Token string
}

// EnsureServiceToken создаёт или обновляет прораба с заданным токеном (хранится только хэш).
func (e *Engine) EnsureServiceToken(ctx context.Context, p ProrabToken) error {
	if len(p.Token) < 32 {
		return fmt.Errorf("prorab %s/%s: token must be at least 32 characters", p.Service, p.Name)
	}
	tag, err := e.pool.Exec(ctx, `
		INSERT INTO service_accounts (id, service_id, name, token_hash)
		SELECT $1, id, $3, $4 FROM services WHERE code = $2
		ON CONFLICT (service_id, name) DO UPDATE SET token_hash = EXCLUDED.token_hash, revoked_at = NULL`,
		uuid.New(), p.Service, p.Name, hashToken(p.Token))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("prorab %s/%s: service %w", p.Service, p.Name, ErrNotFound)
	}
	return nil
}
