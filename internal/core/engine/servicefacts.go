package engine

// Факты подразделений (ADR-65): прораб присылает их в отчёте задачи (`report.service_facts`), ядро не понимает
// содержимого — проверяет только, что бланк принадлежит этому подразделению и он не для пользователя,
// ставит семь полей само и хранит как прислали. Из отчёта, который видит пользователь, они вырезаются.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	maxServiceFacts     = 100       // фактов в одном отчёте
	maxServiceFactBytes = 256 << 10 // содержимое одного факта: промпты и ответы бывают длинными
)

type serviceFact struct {
	formID   string
	version  int
	audience string
	values   []byte
}

// storeServiceFacts — сначала проверяет все факты, потом пишет: при отказе в базе не остаётся половины.
func storeServiceFacts(ctx context.Context, tx pgx.Tx, t task, prorab string, raw any) (reject string, err error) {
	if raw == nil {
		return "", nil
	}
	items, ok := raw.([]any)
	if !ok {
		return "service_facts must be a list", nil
	}
	if len(items) > maxServiceFacts {
		return fmt.Sprintf("service_facts: at most %d per report", maxServiceFacts), nil
	}
	facts := make([]serviceFact, 0, len(items))
	for i, it := range items {
		m, _ := it.(map[string]any)
		formID, _ := m["form_id"].(string)
		values, isObj := m["values"].(map[string]any)
		if formID == "" || !isObj {
			return fmt.Sprintf("service_facts[%d]: form_id and values object are required", i), nil
		}
		f := serviceFact{formID: formID}
		var why string
		if f.version, f.audience, why, err = departmentForm(ctx, tx, t.TargetService, formID); err != nil {
			return "", err
		}
		if why != "" {
			return fmt.Sprintf("service_facts[%d]: %s", i, why), nil
		}
		if f.values, err = json.Marshal(values); err != nil {
			return "", err
		}
		if len(f.values) > maxServiceFactBytes {
			return fmt.Sprintf("service_facts[%d]: values over %d bytes", i, maxServiceFactBytes), nil
		}
		facts = append(facts, f)
	}
	for _, f := range facts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO service_facts (service_id, form_id, schema_version, audience, task_id, user_id, writer, values)
			SELECT s.id, $2, $3, $4, $5, $6, $7, $8 FROM services s WHERE s.code = $1`,
			t.TargetService, f.formID, f.version, f.audience, t.ID, t.UserID, "prorab:"+prorab, f.values); err != nil {
			return "", err
		}
	}
	return "", nil
}

// departmentForm — опубликованная форма факта подразделения не для пользователя: версия и «кому».
// why — почему форма не годится (пусто — годится).
func departmentForm(ctx context.Context, q querier, service, formID string) (version int, audience, why string, err error) {
	err = q.QueryRow(ctx, `
		SELECT f.version, f.audience FROM forms f JOIN services s ON s.id = f.service_id
		WHERE f.form_id = $1 AND f.kind = 'fact' AND f.status = 'published' AND s.code = $2
		ORDER BY f.version DESC LIMIT 1`, formID, service).Scan(&version, &audience)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", fmt.Sprintf("%q is not a fact form of this department", formID), nil
	}
	if err != nil {
		return 0, "", "", err
	}
	if audience == "user" {
		return 0, "", fmt.Sprintf("%q is for the user — send it as the task result", formID), nil
	}
	return version, audience, "", nil
}
