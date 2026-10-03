package engine

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/codes"
)

// WalletView — кошелёк для потока вниз (5.7, поток 3).
type WalletView struct {
	ID         uuid.UUID `json:"wallet_id"`
	CurrencyID string    `json:"currency_id"`
	Balance    string    `json:"balance"`
	Reserved   string    `json:"reserved"`
	Available  string    `json:"available"`
}

// Wallets — кошельки пользователя; GC-кошелёк создаётся при первом обращении (FR-BILL-1).
// Обычный путь — чистое чтение: без блокировки строки и без записи в WAL, поэтому просмотр
// кошелька не ждёт денежных операций и не нагружает fsync (ADR-55).
func (e *Engine) Wallets(ctx context.Context, userID uuid.UUID) ([]WalletView, error) {
	out, err := listWallets(ctx, e.pool, userID)
	if err != nil {
		return nil, err
	}
	for _, w := range out {
		if w.CurrencyID == e.cfg.DefaultCurrency {
			return out, nil
		}
	}
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := userWallet(ctx, tx, userID, e.cfg.DefaultCurrency); err != nil {
			return err
		}
		var err error
		out, err = listWallets(ctx, tx, userID)
		return err
	})
	return out, err
}

func listWallets(ctx context.Context, q querier, userID uuid.UUID) ([]WalletView, error) {
	rows, err := q.Query(ctx, `SELECT `+walletCols+` FROM wallets
		WHERE owner_kind = 'user' AND owner_id = $1 ORDER BY currency_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WalletView{}
	for rows.Next() {
		w, err := scanWallet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, WalletView{
			ID: w.ID, CurrencyID: w.CurrencyID,
			Balance: w.Balance.StringFixed(4), Reserved: w.Reserved.StringFixed(4),
			Available: w.available().StringFixed(4),
		})
	}
	return out, rows.Err()
}

// Transaction — запись журнала для пользователя.
type Transaction struct {
	ID        int64      `json:"id"`
	Type      string     `json:"type"`
	Amount    string     `json:"amount"`
	TaskID    *uuid.UUID `json:"task_id,omitempty"`
	FormID    string     `json:"form_id,omitempty"` // бланк задачи (пока задача не удалена ретеншном)
	Source    string     `json:"source,omitempty"`  // откуда пополнение: demo, promo:<эмитент>
	CreatedAt string     `json:"created_at"`
}

// Transactions — журнал кошелька пользователя, новые сверху.
func (e *Engine) Transactions(ctx context.Context, userID uuid.UUID, limit int) ([]Transaction, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT t.id, t.type, t.amount::text, t.task_id, COALESCE(st.form_id, ''), COALESCE(d.provider, ''),
		       to_char(t.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM transactions t JOIN wallets w ON w.id = t.wallet_id
		LEFT JOIN system_tasks st ON st.id = t.task_id
		LEFT JOIN deposits d ON d.id = t.deposit_id
		WHERE w.owner_kind = 'user' AND w.owner_id = $1
		ORDER BY t.id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Transaction, error) {
		var t Transaction
		err := r.Scan(&t.ID, &t.Type, &t.Amount, &t.TaskID, &t.FormID, &t.Source, &t.CreatedAt)
		return t, err
	})
}

// Features — что включено в этом развёртывании (фронт прячет выключенное).
func (e *Engine) Features() map[string]bool {
	return map[string]bool{"demo_deposit": e.cfg.DemoDepositEnabled, "files": e.files != nil}
}

// DemoDeposit — встроенный демо-провайдер (ADR-26 п.5): тот же контракт депозита,
// адаптер мгновенно отвечает DEPOSIT_PAID. Курс 1:1 (ADR-28).
func (e *Engine) DemoDeposit(ctx context.Context, userID uuid.UUID, amount decimal.Decimal) (WalletView, error) {
	if !e.cfg.DemoDepositEnabled {
		return WalletView{}, fmt.Errorf("demo deposit disabled: %w", ErrNotFound)
	}
	max := decimal.RequireFromString(e.cfg.DemoDepositMax)
	if !amount.IsPositive() || amount.GreaterThan(max) || amount.Exponent() < -4 {
		return WalletView{}, refuse(codes.ValidationError, "amount must be in (0, %s] with at most 4 decimals", max)
	}
	var view WalletView
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		view, _, err = creditDeposit(ctx, tx, userID, e.cfg.DefaultCurrency, amount, "demo", "")
		return err
	})
	return view, err
}

// creditDeposit — оплаченный депозит в одной транзакции: запись deposits, событие DEPOSIT_PAID
// (dedup_key не даёт зачислить одно и то же дважды), строка журнала и новый баланс.
// Общая дорога денег для демо-провайдера и промокодов (ADR-26, ADR-57).
func creditDeposit(ctx context.Context, tx pgx.Tx, userID uuid.UUID, currency string, amount decimal.Decimal,
	provider, dedupKey string) (WalletView, uuid.UUID, error) {
	w, err := userWallet(ctx, tx, userID, currency)
	if err != nil {
		return WalletView{}, uuid.Nil, err
	}
	depositID := uuid.New()
	if dedupKey == "" {
		dedupKey = provider + ":" + depositID.String()
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deposits (id, user_id, provider, status, amount_fiat, fiat_currency, amount_internal, currency_id, rate, paid_at)
		VALUES ($1, $2, $5, 'paid', $3::numeric, 'RUB', $3::numeric, $4, 1, now())`,
		depositID, userID, amount.String(), w.CurrencyID, provider); err != nil {
		return WalletView{}, uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_events (deposit_id, event, dedup_key) VALUES ($1, 'DEPOSIT_PAID', $2)`,
		depositID, dedupKey); err != nil {
		return WalletView{}, uuid.Nil, err
	}
	txID, err := record(ctx, tx, journal{WalletID: w.ID, Type: "deposit", Amount: amount, DepositID: &depositID})
	if err != nil {
		return WalletView{}, uuid.Nil, err
	}
	w.Balance = w.Balance.Add(amount)
	if err := setWallet(ctx, tx, w, true, txID); err != nil {
		return WalletView{}, uuid.Nil, err
	}
	return WalletView{ID: w.ID, CurrencyID: w.CurrencyID, Balance: w.Balance.StringFixed(4),
		Reserved: w.Reserved.StringFixed(4), Available: w.available().StringFixed(4)}, depositID, nil
}
