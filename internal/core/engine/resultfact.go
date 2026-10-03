package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// applyResult превращает результат задачи в факт по объявлению формы (ADR-51).
// reject != "" — результат не принят (→ REPORT_INVALID); все проверки идут до первой записи.
func applyResult(ctx context.Context, tx pgx.Tx, t task, tpl template, result map[string]any) (entry map[string]any, reject string, err error) {
	spec := tpl.Result
	if spec == nil {
		return nil, "", nil
	}
	factTpl, err := publishedTemplate(ctx, tx, spec.FactKind, "fact")
	if errors.Is(err, ErrNotFound) {
		return nil, "result fact form " + spec.FactKind + " is not published", nil
	}
	if err != nil {
		return nil, "", err
	}
	// Результат воркера проверяется так же, как ввод пользователя (ADR-24).
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, "", err
	}
	values, err := decodeValues(raw)
	if err != nil {
		return nil, "result must be an object", nil
	}
	if err := checkFactValues(ctx, tx, t.UserID, factTpl, values); err != nil {
		var r *Refusal
		if errors.As(err, &r) {
			return nil, fmt.Sprintf("result does not match %s: %v", spec.FactKind, r.FieldErrors), nil
		}
		return nil, "", err
	}

	status := "draft"
	if spec.AutoPublish() {
		status = "published"
	}

	var factID uuid.UUID
	version := 1
	if spec.VersionsField == "" {
		factID = uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO facts (id, user_id, kind, origin) VALUES ($1, $2, $3, $4)`,
			factID, t.UserID, spec.FactKind, "task:"+t.ID.String()); err != nil {
			return nil, "", err
		}
	} else {
		ref, _ := t.Values[spec.VersionsField].(string)
		if factID, err = uuid.Parse(ref); err != nil {
			return nil, "versions_field is empty", nil
		}
		base := snapshotVersion(t.Materialized, spec.VersionsField)
		if _, err := lockFact(ctx, tx, t.UserID, factID); errors.Is(err, ErrNotFound) {
			return nil, "versioned fact is gone", nil
		} else if err != nil {
			return nil, "", err
		}
		var (
			last       int
			lastStatus string
		)
		if err := tx.QueryRow(ctx, `SELECT version, status FROM fact_versions WHERE fact_id = $1
			ORDER BY version DESC LIMIT 1`, factID).Scan(&last, &lastStatus); err != nil {
			return nil, "", err
		}
		if lastStatus == "deleted" {
			return nil, "versioned fact is deleted", nil
		}
		if last != base {
			return nil, fmt.Sprintf("stale base version: task saw v%d, fact is at v%d", base, last), nil
		}
		version = last + 1
		if status == "published" {
			if _, err := tx.Exec(ctx, `UPDATE fact_versions SET status = 'archived', updated_at = now()
				WHERE fact_id = $1 AND status = 'published'`, factID); err != nil {
				return nil, "", err
			}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO fact_versions (fact_id, version, schema_version, status, values)
		VALUES ($1, $2, $3, $4, $5)`, factID, version, factTpl.Version, status, values); err != nil {
		return nil, "", err
	}
	if err := factChanged(ctx, tx, t.UserID, factID, version, status); err != nil {
		return nil, "", err
	}
	return map[string]any{"fact_id": factID.String(), "version": version, "status": status, "kind": spec.FactKind}, "", nil
}

// snapshotVersion — версия факта, которую материализатор отдал воркеру.
func snapshotVersion(materialized map[string]any, field string) int {
	ref, _ := materialized[field].(map[string]any)
	switch v := ref["version"].(type) {
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}
