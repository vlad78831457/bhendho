// Package outbox — демон доставки (ADR-05/23/46): берёт события из outbox_queue,
// назначает сквозной seq на пользователя и будит инстансы через NOTIFY.
package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel — канал NOTIFY для доставленных событий: payload {"u": user_id, "s": max_seq}.
const Channel = "offgrid_events"

// dispatchLockID — один активный диспетчер на кластер: seq выдаётся строго по порядку id.
const dispatchLockID = 7_001_002

// Dispatcher — фоновая доставка outbox.
type Dispatcher struct {
	pool     *pgxpool.Pool
	batch    int
	interval time.Duration
	log      *slog.Logger
}

// New создаёт диспетчер.
func New(pool *pgxpool.Pool, batch int, interval time.Duration, log *slog.Logger) *Dispatcher {
	return &Dispatcher{pool: pool, batch: batch, interval: interval, log: log}
}

// DispatchOnce доставляет одну пачку; возвращает число событий.
// Весь шаг — один оператор: выбрать → пронумеровать по пользователям → сдвинуть счётчики →
// пометить sent → NOTIFY. Уведомления уходят только после COMMIT.
func (d *Dispatcher) DispatchOnce(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, dispatchLockID).Scan(&locked); err != nil || !locked {
			return err
		}
		return tx.QueryRow(ctx, `
			WITH picked AS (
				SELECT id, user_id FROM outbox_queue
				WHERE status = 'pending' ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED
			), numbered AS (
				SELECT id, user_id,
				       row_number() OVER (PARTITION BY user_id ORDER BY id) AS rn,
				       count(*)     OVER (PARTITION BY user_id)             AS cnt
				FROM picked
			), bumped AS (
				INSERT INTO user_seq (user_id, last_seq)
				SELECT user_id, max(cnt) FROM numbered GROUP BY user_id
				ON CONFLICT (user_id) DO UPDATE SET last_seq = user_seq.last_seq + EXCLUDED.last_seq
				RETURNING user_id, last_seq
			), upd AS (
				UPDATE outbox_queue o
				SET status = 'sent', sent_at = now(), seq = b.last_seq - n.cnt + n.rn
				FROM numbered n JOIN bumped b ON b.user_id = n.user_id
				WHERE o.id = n.id
				RETURNING o.user_id, o.seq
			), notified AS (
				SELECT pg_notify($2, json_build_object('u', user_id, 's', max(seq))::text)
				FROM upd GROUP BY user_id
			)
			SELECT (SELECT count(*) FROM upd) + 0 * (SELECT count(*) FROM notified)`,
			d.batch, Channel).Scan(&n)
	})
	return n, err
}

// Run доставляет до отмены контекста; полная пачка — сразу следующая.
func (d *Dispatcher) Run(ctx context.Context) {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		n, err := d.DispatchOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			d.log.Error("outbox dispatch", "err", err)
		}
		if n >= d.batch {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
