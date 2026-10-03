package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/db"
	"offgrid/core/internal/core/forms"
	"offgrid/core/internal/core/fsm"
	"offgrid/core/internal/core/money"
)

// task — строка system_tasks.
type task struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	FormID         string
	SchemaVersion  int
	TargetService  string
	IdempotencyKey string
	Status         fsm.Status
	Values         map[string]any
	Materialized   map[string]any
	WalletID       uuid.UUID
	Reserved       decimal.Decimal
	Rates          *money.Rates
	GroupID        *uuid.UUID
	GroupPos       int
	RetryOf        *uuid.UUID
	LockedBy       *uuid.UUID
	LockedUntil    *time.Time
	Accepted       bool
	ETASeconds     *int
	Attempts       int
	Verdict        codes.Code
	Report         map[string]any
	Result         map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const taskCols = `id, user_id, form_id, schema_version, target_service, idempotency_key, status,
	values, materialized, wallet_id, reserved::text, rates_fixed, group_id, group_pos, retry_of,
	locked_by, locked_until, accepted, eta_seconds, attempts, COALESCE(verdict, ''), report, result,
	created_at, updated_at`

// taskListCols — для списков: снимок фактов (materialized) нужен только воркеру, а весит
// как все факты задачи вместе (склад СПА — сотни килобайт); в список он не читается.
var taskListCols = strings.Replace(taskCols, "values, materialized,", "values, NULL::jsonb,", 1)

func scanTask(row pgx.Row) (task, error) {
	var (
		t                                           task
		status, verdict, reserved                   string
		values, materialized, rates, report, result []byte
	)
	err := row.Scan(&t.ID, &t.UserID, &t.FormID, &t.SchemaVersion, &t.TargetService, &t.IdempotencyKey, &status,
		&values, &materialized, &t.WalletID, &reserved, &rates, &t.GroupID, &t.GroupPos, &t.RetryOf,
		&t.LockedBy, &t.LockedUntil, &t.Accepted, &t.ETASeconds, &t.Attempts, &verdict, &report, &result,
		&t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return t, err
	}
	t.Status = fsm.Status(status)
	t.Verdict = codes.Code(verdict)
	t.Reserved = decimal.RequireFromString(reserved)
	if t.Values, err = decodeValues(values); err != nil {
		return t, err
	}
	for _, p := range []struct {
		raw []byte
		dst *map[string]any
	}{{materialized, &t.Materialized}, {report, &t.Report}, {result, &t.Result}} {
		if len(p.raw) > 0 && string(p.raw) != "null" {
			if *p.dst, err = decodeValues(p.raw); err != nil {
				return t, err
			}
		}
	}
	if len(rates) > 0 && string(rates) != "null" {
		t.Rates = &money.Rates{}
		if err := json.Unmarshal(rates, t.Rates); err != nil {
			return t, err
		}
	}
	return t, nil
}

func lockTask(ctx context.Context, tx pgx.Tx, id uuid.UUID) (task, error) {
	t, err := scanTask(tx.QueryRow(ctx, `SELECT `+taskCols+` FROM system_tasks WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// CreateTaskRequest — заполненный бланк сверху (5.7, поток 1): без схемы и цены (ADR-12).
type CreateTaskRequest struct {
	FormID         string          `json:"form_id"`
	Values         json.RawMessage `json:"values"`
	IdempotencyKey string          `json:"idempotency_key"`
	GroupID        *uuid.UUID      `json:"group_id,omitempty"`
}

// CreateTask: валидация → резерв → задача → outbox одной транзакцией (FR-BILL-3, ADR-29).
// Повтор с тем же ключом возвращает существующую задачу (created=false, ADR-20).
func (e *Engine) CreateTask(ctx context.Context, userID uuid.UUID, req CreateTaskRequest) (map[string]any, bool, error) {
	if req.IdempotencyKey == "" || len(req.IdempotencyKey) > 200 {
		return nil, false, refuse(codes.ValidationError, "idempotency_key is required (≤200 chars)")
	}
	if env, err := e.existingByKey(ctx, userID, req.IdempotencyKey); err == nil {
		return env, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	values, err := decodeValues(req.Values)
	if err != nil {
		return nil, false, &Refusal{Code: codes.ValidationError, Detail: err.Error()}
	}

	var (
		created task
		tpl     template
	)
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		tpl, err = publishedTemplate(ctx, tx, req.FormID, "task")
		if err != nil {
			return err
		}
		errs, refs := forms.Validate(tpl.Rules, values)
		if len(errs) > 0 {
			return &Refusal{Code: codes.ValidationError, FieldErrors: errs}
		}
		if err := checkTaskRefs(ctx, tx, userID, refs); err != nil {
			return err
		}
		created, err = e.insertTask(ctx, tx, newTask{
			UserID: userID, Template: tpl, Values: values, Key: req.IdempotencyKey, GroupID: req.GroupID,
		})
		return err
	})
	if db.IsUniqueViolation(err) {
		// Гонка двух одинаковых отправок: победитель уже создал задачу.
		env, err := e.existingByKey(ctx, userID, req.IdempotencyKey)
		return env, false, err
	}
	if err != nil {
		return nil, false, err
	}
	return envelope(created, tpl, false), true, nil
}

func (e *Engine) existingByKey(ctx context.Context, userID uuid.UUID, key string) (map[string]any, error) {
	t, err := scanTask(e.pool.QueryRow(ctx, `SELECT `+taskCols+` FROM system_tasks
		WHERE user_id = $1 AND idempotency_key = $2`, userID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	tpl, err := templateVersion(ctx, e.pool, t.FormID, t.SchemaVersion)
	if err != nil {
		return nil, err
	}
	return envelope(t, tpl, false), nil
}

// checkTaskRefs — при создании: факт опубликован и нужного kind, файл жив (матрица примитивов 5.3).
func checkTaskRefs(ctx context.Context, tx pgx.Tx, userID uuid.UUID, refs forms.Refs) error {
	for _, r := range refs.Facts {
		var kind string
		var published bool
		err := tx.QueryRow(ctx, `
			SELECT f.kind, EXISTS (SELECT 1 FROM fact_versions v WHERE v.fact_id = f.id AND v.status = 'published')
			FROM facts f WHERE f.id = $1 AND f.user_id = $2`, r.ID, userID).Scan(&kind, &published)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != r.ExpectedKind) {
			return &Refusal{Code: codes.ValidationError, FieldErrors: []forms.FieldError{
				{Field: r.Field, Message: "fact not found or of another kind"}}}
		}
		if err != nil {
			return err
		}
		if !published {
			return &Refusal{Code: codes.FactNotPublished, FieldErrors: []forms.FieldError{
				{Field: r.Field, Message: "fact is not published"}}}
		}
	}
	if errs := checkFiles(ctx, tx, userID, refs.Files); len(errs) > 0 {
		return &Refusal{Code: codes.ValidationError, FieldErrors: errs}
	}
	return nil
}

type newTask struct {
	UserID       uuid.UUID
	Template     template
	Values       map[string]any
	Materialized map[string]any
	Key          string
	GroupID      *uuid.UUID
	RetryOf      *uuid.UUID
}

// insertTask — резерв цены шаблона и строка задачи (общая для создания и retry).
func (e *Engine) insertTask(ctx context.Context, tx pgx.Tx, n newTask) (task, error) {
	tpl := n.Template
	w, err := userWallet(ctx, tx, n.UserID, tpl.Currency)
	if err != nil {
		return task{}, err
	}
	if w.available().LessThan(tpl.Price) {
		return task{}, refuse(codes.InsufficientFunds, "need %s, available %s", tpl.Price.StringFixed(4), w.available().StringFixed(4))
	}

	var rates *money.Rates
	if a := tpl.Accounting; a != nil {
		rates = &money.Rates{BaseUnit: a.BaseUnit.Name, RateGC: a.BaseUnit.RateGC, ToBase: map[string]decimal.Decimal{}}
		for _, u := range a.ExtraUnits {
			rates.ToBase[u.Name] = u.RateToBase
		}
	}

	id := uuid.New()
	groupPos := 0
	if n.GroupID != nil {
		// Вектор задач принадлежит одному пользователю; порядок — по добавлению (ADR-36).
		var foreign bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM system_tasks WHERE group_id = $1 AND user_id <> $2)`,
			*n.GroupID, n.UserID).Scan(&foreign); err != nil {
			return task{}, err
		}
		if foreign {
			return task{}, refuse(codes.ValidationError, "group belongs to another user")
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1::text))`, n.GroupID.String()); err != nil {
			return task{}, err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(group_pos), 0) + 1 FROM system_tasks WHERE group_id = $1`,
			*n.GroupID).Scan(&groupPos); err != nil {
			return task{}, err
		}
	}

	var ratesJSON any
	if rates != nil {
		ratesJSON = rates
	}
	var materialized any
	if n.Materialized != nil {
		materialized = n.Materialized
	}
	t, err := scanTask(tx.QueryRow(ctx, `
		INSERT INTO system_tasks (id, user_id, form_id, schema_version, target_service, idempotency_key, status,
			values, materialized, wallet_id, reserved, rates_fixed, priority, group_id, group_pos, retry_of)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', $7, $8, $9, $10::numeric, $11,
			(SELECT priority FROM users WHERE id = $2), $12, $13, $14)
		RETURNING `+taskCols,
		id, n.UserID, tpl.FormID, tpl.Version, tpl.ServiceCode, n.Key, n.Values, materialized,
		w.ID, tpl.Price.String(), ratesJSON, n.GroupID, groupPos, n.RetryOf))
	if err != nil {
		return task{}, err
	}

	txID, err := record(ctx, tx, journal{WalletID: w.ID, Type: "reserve", Amount: tpl.Price, TaskID: &id, Rates: ratesJSON})
	if err != nil {
		return task{}, err
	}
	w.Reserved = w.Reserved.Add(tpl.Price)
	if err := setWallet(ctx, tx, w, true, txID); err != nil {
		return task{}, err
	}
	detail := map[string]any{"reserved": tpl.Price.StringFixed(4), "schema_version": tpl.Version}
	if n.RetryOf != nil {
		detail["retry_of"] = n.RetryOf.String()
	}
	if err := trace(ctx, tx, id, "create", "user:"+n.UserID.String(), "", detail); err != nil {
		return task{}, err
	}
	return t, taskStatusEvent(ctx, tx, t)
}

// GetTask — конверт задачи владельца.
func (e *Engine) GetTask(ctx context.Context, userID, taskID uuid.UUID) (map[string]any, error) {
	t, err := scanTask(e.pool.QueryRow(ctx, `SELECT `+taskCols+` FROM system_tasks WHERE id = $1 AND user_id = $2`, taskID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	tpl, err := templateVersion(ctx, e.pool, t.FormID, t.SchemaVersion)
	if err != nil {
		return nil, err
	}
	return envelope(t, tpl, false), nil
}

// TaskPage — страница истории задач (FR-FORM-6, FR-IF-6).
type TaskPage struct {
	Items []map[string]any `json:"items"`
	Total int              `json:"total"`
}

// ListTasks — история задач пользователя с фильтром по статусу и пагинацией.
func (e *Engine) ListTasks(ctx context.Context, userID uuid.UUID, status string, limit, offset int) (TaskPage, error) {
	page := TaskPage{Items: []map[string]any{}}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM system_tasks WHERE user_id = $1 AND ($2 = '' OR status = $2)`,
		userID, status).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := e.pool.Query(ctx, `SELECT `+taskListCols+` FROM system_tasks WHERE user_id = $1 AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC LIMIT $3 OFFSET $4`, userID, status, limit, offset)
	if err != nil {
		return page, err
	}
	tasks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (task, error) { return scanTask(r) })
	if err != nil {
		return page, err
	}
	cache := map[string]template{}
	for _, t := range tasks {
		key := fmt.Sprintf("%s@%d", t.FormID, t.SchemaVersion)
		tpl, ok := cache[key]
		if !ok {
			if tpl, err = templateVersion(ctx, e.pool, t.FormID, t.SchemaVersion); err != nil {
				return page, err
			}
			cache[key] = tpl
		}
		page.Items = append(page.Items, envelope(t, tpl, false))
	}
	return page, nil
}

// Command применяет пользовательскую команду по матрице 5.9. Недопустимая команда —
// COMMAND_REFUSED в report и запись в трассе; деньги и статус не меняются.
func (e *Engine) Command(ctx context.Context, userID, taskID uuid.UUID, cmd fsm.Command, idempotencyKey string) (map[string]any, error) {
	var (
		refusal *Refusal
		result  task
		tpl     template
	)
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		t, err := lockTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if t.UserID != userID {
			return ErrNotFound
		}
		if tpl, err = templateVersion(ctx, tx, t.FormID, t.SchemaVersion); err != nil {
			return err
		}
		actor := "user:" + userID.String()
		if !fsm.Known(cmd) || !fsm.Allowed(t.Status, cmd, t.Verdict) {
			refusal = refuse(codes.CommandRefused, "command %q is not allowed in status %s", cmd, t.Status)
			return trace(ctx, tx, t.ID, "command", actor, codes.CommandRefused,
				map[string]any{"command": string(cmd), "status": string(t.Status)})
		}
		switch cmd {
		case fsm.Cancel:
			if err := releaseReserve(ctx, tx, t); err != nil {
				return err
			}
			t, err = scanTask(tx.QueryRow(ctx, `UPDATE system_tasks SET status = 'cancelled', finished_at = now(),
				updated_at = now() WHERE id = $1 RETURNING `+taskCols, t.ID))
			if err != nil {
				return err
			}
			if err := trace(ctx, tx, t.ID, "command", actor, "", map[string]any{"command": "cancel"}); err != nil {
				return err
			}
			result = t
			return taskStatusEvent(ctx, tx, t)
		case fsm.Retry:
			// Пересоздание с тем же снимком и новым резервом (ADR-14, ADR-22).
			if idempotencyKey == "" {
				refusal = refuse(codes.ValidationError, "retry requires idempotency_key")
				return nil
			}
			retryOf := t.ID
			result, err = e.insertTask(ctx, tx, newTask{
				UserID: userID, Template: tpl, Values: t.Values, Materialized: t.Materialized,
				Key: idempotencyKey, RetryOf: &retryOf,
			})
			if err != nil {
				return err
			}
			return trace(ctx, tx, t.ID, "command", actor, "", map[string]any{"command": "retry", "new_task": result.ID.String()})
		}
		return nil
	})
	if db.IsUniqueViolation(err) {
		return e.existingByKey(ctx, userID, idempotencyKey)
	}
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return nil, refusal
	}
	return envelope(result, tpl, false), nil
}

// releaseReserve снимает резерв задачи целиком (failed/cancelled, ADR-29 п.5).
func releaseReserve(ctx context.Context, tx pgx.Tx, t task) error {
	w, err := lockWalletByID(ctx, tx, t.WalletID)
	if err != nil {
		return err
	}
	id := t.ID
	txID, err := record(ctx, tx, journal{WalletID: w.ID, Type: "release", Amount: t.Reserved, TaskID: &id})
	if err != nil {
		return err
	}
	w.Reserved = w.Reserved.Sub(t.Reserved)
	return setWallet(ctx, tx, w, true, txID)
}

// failTask — processing/pending → failed с кодом реестра; резерв снимается целиком.
func failTask(ctx context.Context, tx pgx.Tx, t task, code codes.Code, actor, adminMessage string, extra map[string]any) (task, error) {
	if err := releaseReserve(ctx, tx, t); err != nil {
		return t, err
	}
	entry, _ := codes.Lookup(code)
	report := map[string]any{"verdict": string(code), "user_message": entry.Message}
	if adminMessage != "" {
		report["admin_message"] = adminMessage
	}
	for k, v := range extra {
		report[k] = v
	}
	t, err := scanTask(tx.QueryRow(ctx, `
		UPDATE system_tasks SET status = 'failed', verdict = $2, report = $3, locked_by = NULL, locked_until = NULL,
			finished_at = now(), updated_at = now()
		WHERE id = $1 RETURNING `+taskCols, t.ID, string(code), report))
	if err != nil {
		return t, err
	}
	if err := trace(ctx, tx, t.ID, "fail", actor, code, map[string]any{"admin_message": adminMessage}); err != nil {
		return t, err
	}
	return t, taskStatusEvent(ctx, tx, t)
}

func taskStatusEvent(ctx context.Context, tx pgx.Tx, t task) error {
	payload := map[string]any{"task_id": t.ID.String(), "status": string(t.Status)}
	if t.Verdict != "" {
		payload["verdict"] = string(t.Verdict)
	}
	return emit(ctx, tx, t.UserID, "task.status", payload)
}

// envelope собирает конверт задачи (ADR-10). forWorker — values из материализованного снимка.
func envelope(t task, tpl template, forWorker bool) map[string]any {
	system := map[string]any{
		"document_id":     t.ID.String(),
		"form_id":         t.FormID,
		"schema_version":  t.SchemaVersion,
		"kind":            "task",
		"status":          string(t.Status),
		"created_at":      t.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":      t.UpdatedAt.UTC().Format(time.RFC3339),
		"owner_id":        t.UserID.String(),
		"target_service":  t.TargetService,
		"idempotency_key": t.IdempotencyKey,
		"attempts":        t.Attempts,
		"locked_by":       nil,
		"locked_until":    nil,
	}
	if t.LockedBy != nil {
		system["locked_by"] = t.LockedBy.String()
	}
	if t.LockedUntil != nil {
		system["locked_until"] = t.LockedUntil.UTC().Format(time.RFC3339)
	}
	values := t.Values
	if forWorker && t.Materialized != nil {
		values = t.Materialized
	}
	env := map[string]any{
		"system":   system,
		"meta_ui":  metaWithPrice(tpl),
		"commands": fsm.Envelope(t.Status, t.Verdict),
		"values":   values,
		"report":   nil,
	}
	if t.Report != nil {
		env["report"] = t.Report
	}
	if t.Result != nil && !forWorker {
		env["result"] = t.Result
	}
	return env
}
