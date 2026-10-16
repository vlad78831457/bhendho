package engine

import (
	"testing"

	"offgrid/core/internal/core/fsm"
	"offgrid/core/internal/core/modules"
)

// Разбор 6.1: чужое подразделение не перехватывает бланк, заведённый другим.
func TestFormBelongsToItsDepartment(t *testing.T) {
	s := setup(t)
	evil := `
module_id: test/evil
name: Evil
version: 1.0.0
mode: worker_service
worker: {runtime: python, target_service: evil}
capabilities: {}
forms_provided:
  - {form_id: tale.start.v1, kind: task, title: Перехват, schema_file: forms/start.json, base_price_gc: 1}
`
	mod, err := modules.Load(writeModule(t, evil, startForm), s.v)
	must(t, err)
	if err := s.e.RegisterModule(s.ctx, mod); refusalCode(err) == "" {
		t.Fatalf("takeover must be refused: %v", err)
	}
	var service string
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT s.code FROM forms f JOIN services s ON s.id = f.service_id
		WHERE f.form_id = 'tale.start.v1' AND f.status = 'published'`).Scan(&service))
	if service != "tale" {
		t.Fatalf("form now belongs to %s", service)
	}
}

// Разбор 6.1: плохие факты подразделения не оставляют в базе факт-результат провалившейся задачи.
func TestRejectedServiceFactsLeaveNoResult(t *testing.T) {
	s := storySetup(t, "auto") // форма с факт-результатом
	u := s.user("100")
	env, _, err := s.e.CreateTask(s.ctx, u, CreateTaskRequest{FormID: "story.start.v1", IdempotencyKey: "k1",
		Values: []byte(`{"theme": "лес"}`)})
	must(t, err)
	id := taskID(env)
	c, err := s.e.Claim(s.ctx, s.acc, []int{1})
	must(t, err)
	if c == nil {
		t.Fatal("nothing to claim")
	}
	_, err = s.e.Accept(s.ctx, s.acc, id, true, 5)
	must(t, err)
	st, err := s.e.Commit(s.ctx, s.acc, id, CommitRequest{Outcome: "success",
		Result: map[string]any{"title": "Лисёнок", "pages": 2},
		Report: map[string]any{"service_facts": []any{map[string]any{"form_id": "unknown", "values": map[string]any{}}}}})
	must(t, err)
	if st.Status != fsm.Failed {
		t.Fatalf("state: %+v", st)
	}
	var facts int
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT count(*) FROM facts WHERE kind = 'story.state.v1'`).Scan(&facts))
	if facts != 0 {
		t.Fatalf("the failed task left %d result facts", facts)
	}
}
