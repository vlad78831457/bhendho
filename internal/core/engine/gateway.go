package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/forms"
	"offgrid/core/internal/core/fsm"
	"offgrid/core/internal/core/money"
)

// TaskState — ответ шлюза о состоянии задачи (worker_gateway.openapi.yaml#TaskState).
type TaskState struct {
	TaskID      uuid.UUID  `json:"task_id"`
	Status      fsm.Status `json:"status"`
	Verdict     codes.Code `json:"verdict,omitempty"`
	LockedUntil *time.Time `json:"locked_until"`
	Attempts    int        `json:"attempts"`
}

func stateOf(t task) TaskState {
	return TaskState{TaskID: t.ID, Status: t.Status, Verdict: t.Verdict, LockedUntil: t.LockedUntil, Attempts: t.Attempts}
}

// ErrNotOwner — задача не у этого прораба; вместе с ней отдаётся текущее состояние.
var ErrNotOwner = errors.New("task is not locked by this account")

// ClaimedTask — выданная прорабу задача.
type ClaimedTask struct {
	TaskID  uuid.UUID      `json:"task_id"`
	Attempt int            `json:"attempt"`
	Blank   map[string]any `json:"blank"`
}

// maxClaimScan — сколько кандидатов подряд может отсеять материализатор за один claim.
const maxClaimScan = 10

// Claim выдаёт следующую задачу сервиса прораба (SKIP LOCKED, ADR-08/19/36).
// Перед выдачей материализатор собирает снимок; пропавшие данные → failed с полным
// снятием резерва (FACT_MISSING / FILE_MISSING), и берётся следующий кандидат.
func (e *Engine) Claim(ctx context.Context, acc ServiceAccount, versions []int) (*ClaimedTask, error) {
	if len(versions) == 0 {
		return nil, refuse(codes.ValidationError, "schema_versions is required")
	}
	for i := 0; i < maxClaimScan; i++ {
		var (
			claimed *ClaimedTask
			skipped bool
		)
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			t, err := scanTask(tx.QueryRow(ctx, `
				SELECT `+taskCols+` FROM system_tasks t
				WHERE t.status = 'pending' AND t.target_service = $1 AND t.schema_version = ANY($2)
				  AND (t.group_id IS NULL OR NOT EXISTS (
				        SELECT 1 FROM system_tasks p
				        WHERE p.group_id = t.group_id AND p.group_pos < t.group_pos
				          AND p.status NOT IN ('completed', 'failed', 'cancelled')))
				ORDER BY t.priority DESC, t.created_at
				LIMIT 1 FOR UPDATE SKIP LOCKED`, acc.ServiceCode, versions))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			tpl, err := templateVersion(ctx, tx, t.FormID, t.SchemaVersion)
			if err != nil {
				return err
			}
			if t.Materialized == nil {
				snap, refusal, err := materialize(ctx, tx, t, tpl)
				if err != nil {
					return err
				}
				if refusal != nil {
					skipped = true
					_, err := failTask(ctx, tx, t, refusal.Code, "materializer", refusal.Detail, nil)
					return err
				}
				t.Materialized = snap
			}
			t, err = scanTask(tx.QueryRow(ctx, `
				UPDATE system_tasks SET status = 'processing', materialized = $2, locked_by = $3,
					locked_until = now() + make_interval(secs => $4), accepted = FALSE, eta_seconds = NULL, updated_at = now()
				WHERE id = $1 RETURNING `+taskCols,
				t.ID, t.Materialized, acc.ID, e.cfg.AcceptTimeout.Seconds()))
			if err != nil {
				return err
			}
			if err := trace(ctx, tx, t.ID, "claim", "prorab:"+acc.Name, "", map[string]any{"account": acc.ID.String()}); err != nil {
				return err
			}
			if err := taskStatusEvent(ctx, tx, t); err != nil {
				return err
			}
			claimed = &ClaimedTask{TaskID: t.ID, Attempt: t.Attempts + 1, Blank: envelope(t, tpl, true)}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if !skipped {
			return claimed, nil
		}
	}
	return nil, nil
}

// materialize — самодостаточный снимок: тела опубликованных версий фактов и манифесты
// файлов; «только написанное в бланке» (ADR-14/15/16/35).
func materialize(ctx context.Context, tx pgx.Tx, t task, tpl template) (map[string]any, *Refusal, error) {
	var refusal *Refusal
	file := func(ref forms.Ref) (any, error) {
		var (
			bucket, filename, mime string
			object                 uuid.UUID
			size                   int64
		)
		err := tx.QueryRow(ctx, `SELECT bucket, object_uuid, filename, size_bytes, mime FROM files
			WHERE id = $1 AND user_id = $2 AND status = 'active'`, ref.ID, t.UserID).
			Scan(&bucket, &object, &filename, &size, &mime)
		if errors.Is(err, pgx.ErrNoRows) {
			if ref.Required {
				refusal = refuse(codes.FileMissing, "%s: file %s is deleted", ref.Field, ref.ID)
				return nil, nil
			}
			return map[string]any{"file_id": ref.ID.String(), "lost": "LOST_REF"}, nil
		}
		if err != nil {
			return nil, err
		}
		// Байты прораб берёт по file_id через шлюз (GET /gateway/v1/tasks/{id}/files/{file_id}).
		return map[string]any{
			"file_id": ref.ID.String(), "path": bucket + "/" + object.String(),
			"filename": filename, "size": size, "mime": mime,
		}, nil
	}
	snap, err := forms.Rewrite(tpl.Rules, t.Values, func(typ string, ref forms.Ref) (any, error) {
		if refusal != nil {
			return nil, nil
		}
		if typ == "file" {
			return file(ref)
		}
		var (
			version, schema int
			kind            string
			raw             []byte
		)
		err := tx.QueryRow(ctx, `
			SELECT v.version, v.schema_version, f.kind, v.values FROM facts f
			JOIN fact_versions v ON v.fact_id = f.id AND v.status = 'published'
			WHERE f.id = $1 AND f.user_id = $2
			ORDER BY v.version DESC LIMIT 1`, ref.ID, t.UserID).Scan(&version, &schema, &kind, &raw)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != ref.ExpectedKind) {
			refusal = refuse(codes.FactMissing, "%s: fact %s is not published anymore", ref.Field, ref.ID)
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		values, err := decodeValues(raw)
		if err != nil {
			return nil, err
		}
		// Файлы внутри факта (фото персонажа) — тоже манифестами: воркеру нужен доступ к ним по
		// этой задаче. Ссылки факта на другие факты остаются id — снимок не разворачивается вглубь.
		factTpl, err := templateVersion(ctx, tx, kind, schema)
		if err != nil {
			return nil, err
		}
		values, err = forms.Rewrite(factTpl.Rules, values, func(typ string, inner forms.Ref) (any, error) {
			if typ == "file" && refusal == nil {
				inner.Field = ref.Field + "." + inner.Field
				return file(inner)
			}
			return inner.ID.String(), nil
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"fact_id": ref.ID.String(), "kind": kind, "version": version, "values": values}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return snap, refusal, nil
}

// ownedProcessing проверяет, что задача в работе у этого прораба.
func ownedProcessing(t task, acc ServiceAccount) bool {
	return t.Status == fsm.Processing && t.LockedBy != nil && *t.LockedBy == acc.ID
}

// Accept — «принято» + ETA либо отказ (ADR-30). Принятая работа — попытка;
// ожидание отчёта = множитель × ETA с потолком. Отказ возвращает задачу в очередь без попытки.
func (e *Engine) Accept(ctx context.Context, acc ServiceAccount, taskID uuid.UUID, accepted bool, etaSeconds int) (TaskState, error) {
	var state TaskState
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		t, err := lockTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		state = stateOf(t)
		if !ownedProcessing(t, acc) {
			return ErrNotOwner
		}
		if t.Accepted {
			return nil // повторная доставка «принято» — ответ текущим состоянием (ADR-20)
		}
		actor := "prorab:" + acc.Name
		if !accepted {
			t, err = scanTask(tx.QueryRow(ctx, `UPDATE system_tasks SET status = 'pending', locked_by = NULL,
				locked_until = NULL, updated_at = now() WHERE id = $1 RETURNING `+taskCols, t.ID))
			if err != nil {
				return err
			}
			state = stateOf(t)
			if err := trace(ctx, tx, t.ID, "accept", actor, "", map[string]any{"accepted": false}); err != nil {
				return err
			}
			return taskStatusEvent(ctx, tx, t)
		}
		// Потолок до умножения: завышенная оценка не купит бесконечное время и не переполнит Duration.
		if ceil := int(e.cfg.LockCeiling.Seconds()); etaSeconds > ceil {
			etaSeconds = ceil
		}
		eta := time.Duration(etaSeconds) * time.Second
		source := "prorab"
		if etaSeconds <= 0 {
			eta, err = e.serviceAverage(ctx, tx, t.TargetService)
			if err != nil {
				return err
			}
			source = "service_average"
		}
		wait := time.Duration(e.cfg.LockMultiplier) * eta
		if wait > e.cfg.LockCeiling {
			wait = e.cfg.LockCeiling
		}
		t, err = scanTask(tx.QueryRow(ctx, `UPDATE system_tasks SET accepted = TRUE, attempts = attempts + 1,
			eta_seconds = $2, locked_until = now() + make_interval(secs => $3), started_at = now(), updated_at = now()
			WHERE id = $1 RETURNING `+taskCols, t.ID, int(eta.Seconds()), wait.Seconds()))
		if err != nil {
			return err
		}
		state = stateOf(t)
		return trace(ctx, tx, t.ID, "accept", actor, "", map[string]any{
			"accepted": true, "eta_seconds": int(eta.Seconds()), "eta_source": source,
			"wait_seconds": int(wait.Seconds()), "attempt": t.Attempts,
		})
	})
	return state, err
}

// serviceAverage — среднее время завершённых задач сервиса или DefaultETA (ADR-30 п.4).
func (e *Engine) serviceAverage(ctx context.Context, tx pgx.Tx, service string) (time.Duration, error) {
	var secs *float64
	err := tx.QueryRow(ctx, `
		SELECT avg(extract(epoch FROM finished_at - started_at)) FROM (
			SELECT finished_at, started_at FROM system_tasks
			WHERE target_service = $1 AND status = 'completed' AND started_at IS NOT NULL
			ORDER BY finished_at DESC LIMIT 100) recent`, service).Scan(&secs)
	if err != nil {
		return 0, err
	}
	if secs == nil || *secs < 1 {
		return e.cfg.DefaultETA, nil
	}
	return time.Duration(*secs * float64(time.Second)), nil
}

// Heartbeat — «жив»; ответ — текущее состояние (например, чтобы бросить отменённое).
func (e *Engine) Heartbeat(ctx context.Context, acc ServiceAccount, taskID uuid.UUID) (TaskState, error) {
	t, err := scanTask(e.pool.QueryRow(ctx, `SELECT `+taskCols+` FROM system_tasks WHERE id = $1`, taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskState{}, ErrNotFound
	}
	if err != nil {
		return TaskState{}, err
	}
	if !ownedProcessing(t, acc) {
		return stateOf(t), ErrNotOwner
	}
	return stateOf(t), nil
}

// Progress — эфемерный прогресс мимо БД (FR-FORM-4).
func (e *Engine) Progress(ctx context.Context, acc ServiceAccount, taskID uuid.UUID, percent int, phase string) error {
	if percent < 0 || percent > 100 || phase == "" {
		return refuse(codes.ValidationError, "percent must be 0..100 and phase non-empty")
	}
	state, err := e.Heartbeat(ctx, acc, taskID)
	if err != nil {
		return err
	}
	if e.notifier == nil {
		return nil
	}
	var userID uuid.UUID
	if err := e.pool.QueryRow(ctx, `SELECT user_id FROM system_tasks WHERE id = $1`, state.TaskID).Scan(&userID); err != nil {
		return err
	}
	return e.notifier.Progress(ctx, userID, taskID, percent, phase)
}

// CommitRequest — отчёт попытки (worker_gateway.openapi.yaml#CommitRequest).
type CommitRequest struct {
	Outcome string         `json:"outcome"`
	Result  map[string]any `json:"result"`
	Report  map[string]any `json:"report"`
}

// reservedReportKeys — поля report, которые ставит только шлюз (ADR-24: текст воркера
// к пользователю не проходит, вердикт — системный).
var reservedReportKeys = []string{"verdict", "user_message", "field_errors"}

// Commit фиксирует отчёт по состоянию строки (ADR-20): чужая или уже закрытая задача —
// ответ текущим состоянием без изменений. Успех — бухгалтер списывает min(факт, резерв).
func (e *Engine) Commit(ctx context.Context, acc ServiceAccount, taskID uuid.UUID, req CommitRequest) (TaskState, error) {
	var state TaskState
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		t, err := lockTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		state = stateOf(t)
		if !ownedProcessing(t, acc) || !t.Accepted {
			return nil // дедуп по состоянию строки: воркер выбрасывает свой результат
		}
		actor := "prorab:" + acc.Name
		report := map[string]any{}
		for k, v := range req.Report {
			report[k] = v
		}
		adminMessage, _ := report["admin_message"].(string)
		for _, k := range reservedReportKeys {
			delete(report, k)
		}

		switch req.Outcome {
		case "error":
			t, err = failTask(ctx, tx, t, codes.WorkerError, actor, adminMessage, report)
		case "rejected":
			// Данные корректны по форме, но неприменимы по смыслу (ADR-53): retry не предлагается.
			t, err = failTask(ctx, tx, t, codes.DataRejected, actor, adminMessage, report)
		case "success":
			t, err = e.settle(ctx, tx, t, acc, report, req.Result)
		default:
			t, err = failTask(ctx, tx, t, codes.ReportInvalid, actor, fmt.Sprintf("unknown outcome %q", req.Outcome), nil)
		}
		if err != nil {
			return err
		}
		state = stateOf(t)
		return nil
	})
	return state, err
}

// settle — processing → completed: счёт воркера в единицах → деньги (ADR-29/47).
func (e *Engine) settle(ctx context.Context, tx pgx.Tx, t task, acc ServiceAccount, report, result map[string]any) (task, error) {
	actor := "prorab:" + acc.Name
	lines, err := parseCharge(report["worker_charge"])
	if err != nil {
		return failTask(ctx, tx, t, codes.ReportInvalid, actor, err.Error(), nil)
	}
	bill, err := money.Settle(lines, t.Rates, t.Reserved)
	if err != nil {
		return failTask(ctx, tx, t, codes.ReportInvalid, actor, err.Error(), nil)
	}
	if result == nil {
		result = map[string]any{}
	}
	tpl, err := templateVersion(ctx, tx, t.FormID, t.SchemaVersion)
	if err != nil {
		return t, err
	}
	resultFact, reject, err := applyResult(ctx, tx, t, tpl, result)
	if err != nil {
		return t, err
	}
	if reject != "" {
		return failTask(ctx, tx, t, codes.ReportInvalid, actor, reject, nil)
	}
	if resultFact != nil {
		report["result_fact"] = resultFact
	}

	var serviceWalletID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT target_wallet_id FROM services WHERE code = $1`, t.TargetService).Scan(&serviceWalletID); err != nil {
		return t, err
	}
	userW, err := lockWalletByID(ctx, tx, t.WalletID)
	if err != nil {
		return t, err
	}
	serviceW, err := lockWalletByID(ctx, tx, serviceWalletID)
	if err != nil {
		return t, err
	}

	id := t.ID
	var units any
	if len(lines) > 0 {
		units = chargeLines(lines)
	}
	var rates any
	if t.Rates != nil {
		rates = t.Rates
	}
	chargeTx, err := record(ctx, tx, journal{WalletID: userW.ID, Type: "charge", Amount: bill.Charged,
		Counterparty: &serviceW.ID, TaskID: &id, Units: units, Rates: rates})
	if err != nil {
		return t, err
	}
	if _, err := record(ctx, tx, journal{WalletID: serviceW.ID, Type: "income", Amount: bill.Charged,
		Counterparty: &userW.ID, TaskID: &id}); err != nil {
		return t, err
	}
	lastTx := chargeTx
	if bill.Released.IsPositive() {
		if lastTx, err = record(ctx, tx, journal{WalletID: userW.ID, Type: "release", Amount: bill.Released, TaskID: &id}); err != nil {
			return t, err
		}
	}
	userW.Balance = userW.Balance.Sub(bill.Charged)
	userW.Reserved = userW.Reserved.Sub(t.Reserved)
	if err := setWallet(ctx, tx, userW, true, lastTx); err != nil {
		return t, err
	}
	serviceW.Balance = serviceW.Balance.Add(bill.Charged)
	if err := setWallet(ctx, tx, serviceW, false, chargeTx); err != nil {
		return t, err
	}

	entry, _ := codes.Lookup(codes.Success)
	report["verdict"] = string(codes.Success)
	report["user_message"] = entry.Message
	if len(lines) > 0 {
		report["worker_charge"] = map[string]any{
			"lines":      chargeLines(lines),
			"base_units": mustFloat(bill.BaseUnits),
			"total_gc":   mustFloat(bill.Charged),
		}
	}
	t, err = scanTask(tx.QueryRow(ctx, `
		UPDATE system_tasks SET status = 'completed', verdict = 'SUCCESS', report = $2, result = $3,
			locked_until = NULL, finished_at = now(), updated_at = now()
		WHERE id = $1 RETURNING `+taskCols, t.ID, report, result))
	if err != nil {
		return t, err
	}
	if err := trace(ctx, tx, t.ID, "commit", actor, codes.Success, map[string]any{
		"base_units": bill.BaseUnits.String(), "total": bill.Total.String(),
		"charged": bill.Charged.String(), "released": bill.Released.String(),
	}); err != nil {
		return t, err
	}
	if err := taskStatusEvent(ctx, tx, t); err != nil {
		return t, err
	}
	return t, emit(ctx, tx, t.UserID, "task.result", map[string]any{"task_id": t.ID.String(), "result": result})
}

// parseCharge — строки счёта воркера; мусор → ошибка (→ REPORT_INVALID).
func parseCharge(v any) ([]money.Line, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("worker_charge must be an object")
	}
	raw, ok := m["lines"].([]any)
	if !ok || len(raw) == 0 {
		return nil, errors.New("worker_charge.lines must be a non-empty list")
	}
	lines := make([]money.Line, 0, len(raw))
	for i, r := range raw {
		lm, ok := r.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("worker_charge.lines[%d] must be an object", i)
		}
		unit, _ := lm["unit"].(string)
		units, err := toDecimal(lm["units"])
		if unit == "" || err != nil {
			return nil, fmt.Errorf("worker_charge.lines[%d]: unit and numeric units are required", i)
		}
		lines = append(lines, money.Line{Unit: unit, Units: units})
	}
	return lines, nil
}

func toDecimal(v any) (decimal.Decimal, error) {
	switch n := v.(type) {
	case json.Number:
		return decimal.NewFromString(n.String())
	case float64:
		return decimal.NewFromFloat(n), nil
	default:
		return decimal.Zero, errors.New("not a number")
	}
}

func mustFloat(d decimal.Decimal) float64 {
	f, _ := d.Float64()
	return f
}

// chargeLines — строки счёта с числами без потери точности (json.Number — число в JSON).
func chargeLines(lines []money.Line) []map[string]any {
	out := make([]map[string]any, len(lines))
	for i, l := range lines {
		out[i] = map[string]any{"unit": l.Unit, "units": json.Number(l.Units.String())}
	}
	return out
}
