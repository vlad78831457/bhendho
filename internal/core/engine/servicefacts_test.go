package engine

import (
	"encoding/json"
	"errors"
	"testing"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/fsm"
)

// commitReport — прораб берёт задачу и сдаёт её с этим отчётом и исходом.
func (s *env) commitReport(outcome, report string) TaskState {
	s.t.Helper()
	c := s.claimOne()
	if c == nil {
		s.t.Fatal("nothing to claim")
	}
	_, err := s.e.Accept(s.ctx, s.acc, c.TaskID, true, 10)
	must(s.t, err)
	var rep map[string]any
	must(s.t, json.Unmarshal([]byte(report), &rep))
	st, err := s.e.Commit(s.ctx, s.acc, c.TaskID, CommitRequest{Outcome: outcome, Result: map[string]any{"pages": 1}, Report: rep})
	must(s.t, err)
	return st
}

func (s *env) serviceFactCount() int {
	var n int
	must(s.t, s.e.pool.QueryRow(s.ctx, `SELECT count(*) FROM service_facts`).Scan(&n))
	return n
}

// ADR-65: факты подразделения уходят в его память с семью полями ядра и не видны пользователю.
func TestServiceFactsStored(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	created, err := s.create(u, s.hero(u, true), "k1")
	must(t, err)
	st := s.commitReport("success", `{"worker_charge": {"lines": [{"unit": "images", "units": 1}]},
		"service_facts": [{"form_id": "tale.spend.v1", "values": {"model": "gpt-x", "tokens": 12400, "prompt": "жил-был лисёнок"}}]}`)
	if st.Status != fsm.Completed {
		t.Fatalf("commit: %+v", st)
	}
	var (
		service, form, audience, prorab string
		version                         int
		factTask, userID                string
		values                          map[string]any
	)
	must(t, s.e.pool.QueryRow(s.ctx, `
		SELECT s.code, f.form_id, f.schema_version, f.audience, f.task_id::text, f.user_id::text, f.writer, f.values
		FROM service_facts f JOIN services s ON s.id = f.service_id`).
		Scan(&service, &form, &version, &audience, &factTask, &userID, &prorab, &values))
	if service != "tale" || form != "tale.spend.v1" || version != 1 || audience != "adm" ||
		factTask != taskID(created).String() || userID != u.String() || prorab != "prorab:"+s.acc.Name || values["prompt"] != "жил-был лисёнок" {
		t.Fatalf("service fact: %s %s v%d %s task %s user %s prorab %s %v", service, form, version, audience, factTask, userID, prorab, values)
	}

	// Пользователь их не видит: ни в отчёте задачи, ни в каталоге, ни как свой бланк.
	env, err := s.e.GetTask(s.ctx, u, taskID(created))
	must(t, err)
	if _, leaked := env["report"].(map[string]any)["service_facts"]; leaked {
		t.Fatal("service facts leaked into the user's report")
	}
	items, err := s.e.Catalog(s.ctx)
	must(t, err)
	for _, it := range items {
		if it.FormID == "tale.spend.v1" {
			t.Fatal("department form in the user catalog")
		}
	}
	if _, err := s.e.CreateFact(s.ctx, u, "tale.spend.v1", []byte(`{"name": "x"}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user created a department fact: %v", err)
	}
}

func TestServiceFactsRejectedAsAWhole(t *testing.T) {
	for name, facts := range map[string]string{
		"user form":    `[{"form_id": "tale.spend.v1", "values": {}}, {"form_id": "tale.character.v1", "values": {"name": "x"}}]`,
		"unknown form": `[{"form_id": "tale.spend.v1", "values": {}}, {"form_id": "other.form", "values": {}}]`,
		"no values":    `[{"form_id": "tale.spend.v1"}]`,
		"not a list":   `{"form_id": "tale.spend.v1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := setup(t)
			u := s.user("1000")
			_, err := s.create(u, s.hero(u, true), "k1")
			must(t, err)
			st := s.commitReport("success", `{"worker_charge": {"lines": [{"unit": "images", "units": 1}]}, "service_facts": `+facts+`}`)
			if st.Status != fsm.Failed || st.Verdict != codes.ReportInvalid {
				t.Fatalf("state: %+v", st)
			}
			if n := s.serviceFactCount(); n != 0 {
				t.Fatalf("%d facts stored from a rejected report", n)
			}
		})
	}
}

func TestServiceFactsNotStoredOnError(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	created, err := s.create(u, s.hero(u, true), "k1")
	must(t, err)
	st := s.commitReport("error", `{"admin_message": "провайдер упал", "service_facts": [{"form_id": "tale.spend.v1", "values": {"tokens": 10}}]}`)
	if st.Status != fsm.Failed {
		t.Fatalf("state: %+v", st)
	}
	if n := s.serviceFactCount(); n != 0 {
		t.Fatalf("%d facts stored on error", n)
	}
	env, err := s.e.GetTask(s.ctx, u, taskID(created))
	must(t, err)
	if rep, _ := env["report"].(map[string]any); rep != nil && rep["service_facts"] != nil {
		t.Fatal("service facts leaked into the report of a failed task")
	}
}
