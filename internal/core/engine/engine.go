// Package engine — транзакционная логика ядра: задачи и резервы, материализатор,
// шлюз прорабов, бухгалтер, факты, кошельки, регистрация модулей.
// Любое изменение денег, статуса, трассы и outbox — в одной транзакции (ADR-06/20).
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/config"
	"offgrid/core/internal/core/forms"
)

// Notifier — эфемерная доставка (task.progress мимо БД, ADR-05/46).
type Notifier interface {
	Progress(ctx context.Context, userID, taskID uuid.UUID, percent int, phase string) error
}

// Engine — ядро поверх пула Postgres; безопасно для конкурентного использования.
type Engine struct {
	pool     *pgxpool.Pool
	cfg      config.Config
	notifier Notifier
	now      func() time.Time
	catalog  catalogCache
	files    *fileStore // nil — хранилище файлов не настроено
}

// New создаёт ядро. notifier может быть nil (прогресс тогда не рассылается).
func New(pool *pgxpool.Pool, cfg config.Config, notifier Notifier) *Engine {
	return &Engine{pool: pool, cfg: cfg, notifier: notifier, now: time.Now}
}

// Pool — пул соединений (для realtime и фоновых заданий).
func (e *Engine) Pool() *pgxpool.Pool { return e.pool }

// ErrNotFound — сущности нет или она чужая (не различаем — не раскрываем чужое).
var ErrNotFound = errors.New("not found")

// Refusal — отказ с кодом реестра; денег и статусов не трогает.
type Refusal struct {
	Code        codes.Code
	Detail      string
	FieldErrors []forms.FieldError
}

func (r *Refusal) Error() string {
	if r.Detail == "" {
		return string(r.Code)
	}
	return fmt.Sprintf("%s: %s", r.Code, r.Detail)
}

// Report — секция report конверта в виде, который отдаёт ядро.
func (r *Refusal) Report() map[string]any {
	e, _ := codes.Lookup(r.Code)
	rep := map[string]any{"verdict": string(r.Code), "user_message": e.Message}
	if r.Detail != "" {
		rep["admin_message"] = r.Detail
	}
	if len(r.FieldErrors) > 0 {
		rep["field_errors"] = r.FieldErrors
	}
	return rep
}

func refuse(code codes.Code, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// trace — запись трассы (append-only, ADR-24).
func trace(ctx context.Context, tx pgx.Tx, taskID uuid.UUID, stage, actor string, code codes.Code, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	var c *string
	if code != "" {
		s := string(code)
		c = &s
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO task_trace (task_id, stage, actor, code, detail) VALUES ($1, $2, $3, $4, $5)`,
		taskID, stage, actor, c, detail)
	return err
}

// emit — событие в outbox той же транзакцией (ADR-05); seq назначит диспетчер.
func emit(ctx context.Context, tx pgx.Tx, userID uuid.UUID, typ string, payload any) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO outbox_queue (user_id, type, payload) VALUES ($1, $2, $3)`,
		userID, typ, payload)
	return err
}

// wallet — строка кошелька под блокировкой.
type wallet struct {
	ID         uuid.UUID
	OwnerID    uuid.UUID
	CurrencyID string
	Balance    decimal.Decimal
	Reserved   decimal.Decimal
}

func (w wallet) available() decimal.Decimal { return w.Balance.Sub(w.Reserved) }

const walletCols = `id, owner_id, currency_id, balance::text, reserved::text`

func scanWallet(row pgx.Row) (wallet, error) {
	var w wallet
	var bal, res string
	if err := row.Scan(&w.ID, &w.OwnerID, &w.CurrencyID, &bal, &res); err != nil {
		return w, err
	}
	w.Balance = decimal.RequireFromString(bal)
	w.Reserved = decimal.RequireFromString(res)
	return w, nil
}

func lockWalletByID(ctx context.Context, tx pgx.Tx, id uuid.UUID) (wallet, error) {
	return scanWallet(tx.QueryRow(ctx, `SELECT `+walletCols+` FROM wallets WHERE id = $1 FOR UPDATE`, id))
}

// userWallet возвращает кошелёк пользователя в валюте, создавая его при первом обращении.
func userWallet(ctx context.Context, tx pgx.Tx, userID uuid.UUID, currency string) (wallet, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO wallets (id, owner_kind, owner_id, currency_id) VALUES ($1, 'user', $2, $3)
		ON CONFLICT (owner_kind, owner_id, currency_id) DO NOTHING`,
		uuid.New(), userID, currency); err != nil {
		return wallet{}, err
	}
	return scanWallet(tx.QueryRow(ctx, `SELECT `+walletCols+` FROM wallets
		WHERE owner_kind = 'user' AND owner_id = $1 AND currency_id = $2 FOR UPDATE`, userID, currency))
}

// setWallet пишет новые balance/reserved и событие wallet.changed для владельца-пользователя.
func setWallet(ctx context.Context, tx pgx.Tx, w wallet, userOwned bool, txID int64) error {
	if _, err := tx.Exec(ctx,
		`UPDATE wallets SET balance = $2::numeric, reserved = $3::numeric, updated_at = now() WHERE id = $1`,
		w.ID, w.Balance.String(), w.Reserved.String()); err != nil {
		return err
	}
	if !userOwned {
		return nil
	}
	return emit(ctx, tx, w.OwnerID, "wallet.changed", map[string]any{
		"wallet_id":      w.ID.String(),
		"currency_id":    w.CurrencyID,
		"balance":        w.Balance.StringFixed(4),
		"reserved":       w.Reserved.StringFixed(4),
		"transaction_id": fmt.Sprint(txID),
	})
}

// journal — запись журнала денег (INSERT-only, ADR-06).
type journal struct {
	WalletID     uuid.UUID
	Type         string
	Amount       decimal.Decimal
	Counterparty *uuid.UUID
	TaskID       *uuid.UUID
	DepositID    *uuid.UUID
	Units        any
	Rates        any
}

func record(ctx context.Context, tx pgx.Tx, j journal) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO transactions (wallet_id, type, amount, counterparty_id, task_id, deposit_id, units, rates)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, $7, $8) RETURNING id`,
		j.WalletID, j.Type, j.Amount.String(), j.Counterparty, j.TaskID, j.DepositID, j.Units, j.Rates).Scan(&id)
	return id, err
}

// decodeValues декодирует JSON-объект, сохраняя числа как json.Number.
func decodeValues(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("values must be a JSON object: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}
