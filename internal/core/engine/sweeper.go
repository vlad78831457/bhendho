package engine

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/codes"
)

// sweepBatch — сколько истёкших локов обрабатывает один проход.
const sweepBatch = 100

// SweepExpired возвращает в очередь задачи с истёкшим локом (ADR-08/30):
// не принятая после claim — назад без попытки; принятая — WORKER_TIMEOUT и в очередь,
// а при исчерпанном лимите попыток — failed с LIMIT_EXHAUSTED и снятием резерва.
// Безопасно на нескольких инстансах: SKIP LOCKED.
func (e *Engine) SweepExpired(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+taskCols+` FROM system_tasks
			WHERE status = 'processing' AND locked_until < now()
			ORDER BY locked_until LIMIT $1 FOR UPDATE SKIP LOCKED`, sweepBatch)
		if err != nil {
			return err
		}
		expired, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (task, error) { return scanTask(r) })
		if err != nil {
			return err
		}
		for _, t := range expired {
			n++
			if t.Accepted && t.Attempts >= e.cfg.MaxAttempts {
				if _, err := failTask(ctx, tx, t, codes.LimitExhausted, "system", "", nil); err != nil {
					return err
				}
				continue
			}
			var code codes.Code
			stage := "claim_expired"
			if t.Accepted {
				code, stage = codes.WorkerTimeout, "timeout"
			}
			t, err = scanTask(tx.QueryRow(ctx, `UPDATE system_tasks SET status = 'pending', locked_by = NULL,
				locked_until = NULL, accepted = FALSE, updated_at = now() WHERE id = $1 RETURNING `+taskCols, t.ID))
			if err != nil {
				return err
			}
			if err := trace(ctx, tx, t.ID, stage, "system", code, map[string]any{"attempts": t.Attempts}); err != nil {
				return err
			}
			if err := taskStatusEvent(ctx, tx, t); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// Cleanup — TTL трассы (ADR-24), ретеншн завершённых задач кроме «шаблонов» (ADR-37)
// и доставленных событий outbox. Деньги (transactions) не трогаются никогда.
func (e *Engine) Cleanup(ctx context.Context) error {
	if _, err := e.pool.Exec(ctx, `DELETE FROM task_trace WHERE at < now() - make_interval(secs => $1)`,
		e.cfg.TraceTTL.Seconds()); err != nil {
		return err
	}
	if _, err := e.pool.Exec(ctx, `DELETE FROM system_tasks
		WHERE status IN ('completed', 'failed', 'cancelled') AND finished_at < now() - make_interval(secs => $1)`,
		e.cfg.TaskRetention.Seconds()); err != nil {
		return err
	}
	_, err := e.pool.Exec(ctx, `DELETE FROM outbox_queue WHERE status = 'sent' AND sent_at < now() - make_interval(secs => $1)`,
		e.cfg.TaskRetention.Seconds())
	return err
}

// RunBackground крутит фоновые задания до отмены контекста.
func (e *Engine) RunBackground(ctx context.Context, log *slog.Logger) {
	sweep := time.NewTicker(e.cfg.SweepInterval)
	cleanup := time.NewTicker(time.Hour)
	defer sweep.Stop()
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sweep.C:
			if _, err := e.SweepExpired(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("sweep expired locks", "err", err)
			}
		case <-cleanup.C:
			if err := e.Cleanup(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("cleanup", "err", err)
			}
		}
	}
}
