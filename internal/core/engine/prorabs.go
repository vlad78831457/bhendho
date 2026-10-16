package engine

// Уровни прорабов (ADR-76): в подразделении несколько прорабов — «мок», «глупенькие», «среднички», «умные».
// Кто берёт какой бланк, решает админ приоритетом; пользователь ничего не выбирает.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/codes"
)

// ProrabPriority — приоритет прораба на бланк задачи; Explicit=false — строки нет, действует 1.
type ProrabPriority struct {
	Prorab   string `json:"prorab"`
	FormID   string `json:"form_id"`
	Priority int    `json:"priority"`
	Explicit bool   `json:"explicit"`
}

// ProrabPriorities — действующие прорабы подразделения × его бланки задач.
func (e *Engine) ProrabPriorities(ctx context.Context, service string) ([]ProrabPriority, error) {
	rows, err := e.pool.Query(ctx, `
		SELECT a.name, f.form_id, COALESCE(pp.priority, 1), pp.priority IS NOT NULL
		FROM services s
		JOIN service_accounts a ON a.service_id = s.id AND a.revoked_at IS NULL
		JOIN forms f ON f.service_id = s.id AND f.kind = 'task' AND f.status = 'published'
		LEFT JOIN prorab_priorities pp ON pp.service_id = s.id AND pp.prorab = a.name AND pp.form_id = f.form_id
		WHERE s.code = $1 ORDER BY a.name, f.form_id`, service)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ProrabPriority, error) {
		var p ProrabPriority
		return p, r.Scan(&p.Prorab, &p.FormID, &p.Priority, &p.Explicit)
	})
}

// SetProrabPriority — админ задаёт приоритет; 0 — прораб этот бланк не берёт.
func (e *Engine) SetProrabPriority(ctx context.Context, admin uuid.UUID, service, prorab, formID string, priority int) error {
	if priority < 0 || priority > 1000 {
		return refuse(codes.ValidationError, "priority: 0 (off) … 1000")
	}
	tag, err := e.pool.Exec(ctx, `
		INSERT INTO prorab_priorities (service_id, prorab, form_id, priority, updated_by)
		SELECT s.id, $2, $3, $4, $5 FROM services s
		WHERE s.code = $1
		  AND EXISTS (SELECT 1 FROM service_accounts a WHERE a.service_id = s.id AND a.name = $2 AND a.revoked_at IS NULL)
		  AND EXISTS (SELECT 1 FROM forms f WHERE f.service_id = s.id AND f.form_id = $3 AND f.kind = 'task')
		ON CONFLICT (service_id, prorab, form_id) DO UPDATE SET priority = EXCLUDED.priority,
			updated_by = EXCLUDED.updated_by, updated_at = now()`,
		service, prorab, formID, priority, "admin:"+admin.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return refuse(codes.ValidationError, "%s / %s / %s: no such prorab or task form", service, prorab, formID)
	}
	return nil
}
