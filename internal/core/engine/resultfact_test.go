package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/fsm"
	"offgrid/core/internal/core/modules"
)

const storyManifest = `
module_id: test/story
name: Story
version: 1.0.0
mode: worker_service
worker: {runtime: python, target_service: story}
capabilities: {}
forms_provided:
  - {form_id: story.state.v1, kind: fact, title: Состояние, schema_file: forms/state.json}
  - form_id: story.start.v1
    kind: task
    title: Начать
    schema_file: forms/start.json
    base_price_gc: 10
    result: {fact_kind: story.state.v1, publish: %s}
  - form_id: story.continue.v1
    kind: task
    title: Продолжить
    schema_file: forms/continue.json
    base_price_gc: 5
    result: {fact_kind: story.state.v1, versions_field: story, publish: auto}
`

func storySetup(t *testing.T, publish string) *env {
	t.Helper()
	s := setup(t)
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "forms"), 0o755))
	files := map[string]string{
		"module.yaml":         fmt.Sprintf(storyManifest, publish),
		"forms/state.json":    `{"title": "Состояние", "fields_rules": {"title": {"type": "string", "required": true}, "pages": {"type": "int", "min": 0}}}`,
		"forms/start.json":    `{"title": "Начать", "fields_rules": {"theme": {"type": "string"}}}`,
		"forms/continue.json": `{"title": "Продолжить", "fields_rules": {"story": {"type": "fact_ref", "required": true, "expected_fact_kind": "story.state.v1"}, "choice": {"type": "int"}}}`,
	}
	for name, body := range files {
		must(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	mod, err := modules.Load(dir, s.v)
	must(t, err)
	must(t, s.e.RegisterModule(s.ctx, mod))
	token, err := s.e.IssueServiceToken(s.ctx, "story", "prorab-story")
	must(t, err)
	s.acc, err = s.e.AuthenticateService(s.ctx, token)
	must(t, err)
	return s
}

// run создаёт задачу, прогоняет её через прораба и возвращает состояние после commit.
func (s *env) run(user uuid.UUID, form, key string, values map[string]any, result map[string]any) (uuid.UUID, TaskState) {
	s.t.Helper()
	raw, _ := json.Marshal(values)
	env, _, err := s.e.CreateTask(s.ctx, user, CreateTaskRequest{FormID: form, IdempotencyKey: key, Values: raw})
	must(s.t, err)
	id := taskID(env)
	c, err := s.e.Claim(s.ctx, s.acc, []int{1})
	must(s.t, err)
	if c == nil || c.TaskID != id {
		s.t.Fatalf("claim %+v, want %s", c, id)
	}
	_, err = s.e.Accept(s.ctx, s.acc, id, true, 5)
	must(s.t, err)
	st, err := s.e.Commit(s.ctx, s.acc, id, CommitRequest{Outcome: "success", Result: result})
	must(s.t, err)
	return id, st
}

func (s *env) resultFact(user, id uuid.UUID) map[string]any {
	s.t.Helper()
	env, err := s.e.GetTask(s.ctx, user, id)
	must(s.t, err)
	rf, _ := env["report"].(map[string]any)["result_fact"].(map[string]any)
	return rf
}

func TestResultFactChainWithAutoPublish(t *testing.T) {
	s := storySetup(t, "auto")
	u := s.user("100")

	start, st := s.run(u, "story.start.v1", "s1", map[string]any{"theme": "лес"}, map[string]any{"title": "Лисёнок", "pages": 2})
	if st.Status != fsm.Completed {
		t.Fatalf("start: %+v", st)
	}
	rf := s.resultFact(u, start)
	if rf["version"] != json.Number("1") || rf["status"] != "published" {
		t.Fatalf("result fact: %v", rf)
	}
	storyID := uuid.MustParse(rf["fact_id"].(string))
	fact, err := s.e.GetFact(s.ctx, u, storyID)
	must(t, err)
	if fact.Origin != "task:"+start.String() || fact.Kind != "story.state.v1" {
		t.Fatalf("fact origin/kind: %+v", fact)
	}

	cont, st := s.run(u, "story.continue.v1", "c1", map[string]any{"story": storyID.String(), "choice": 1},
		map[string]any{"title": "Лисёнок", "pages": 4})
	if st.Status != fsm.Completed {
		t.Fatalf("continue: %+v", st)
	}
	rf = s.resultFact(u, cont)
	if rf["fact_id"] != storyID.String() || rf["version"] != json.Number("2") {
		t.Fatalf("continue must version the same fact: %v", rf)
	}
	fact, err = s.e.GetFact(s.ctx, u, storyID)
	must(t, err)
	if *fact.PublishedVersion != 2 || fact.Versions[1].Status != "archived" {
		t.Fatalf("versions: %+v", fact)
	}
	if fact.Versions[0].Values["pages"] != json.Number("4") {
		t.Fatalf("latest values: %v", fact.Versions[0].Values)
	}
	s.checkMoney()
}

func TestResultFactDraftByDefault(t *testing.T) {
	s := storySetup(t, "draft")
	u := s.user("100")
	start, _ := s.run(u, "story.start.v1", "s1", nil, map[string]any{"title": "Черновик"})
	if rf := s.resultFact(u, start); rf["status"] != "draft" {
		t.Fatalf("ADR-15: result arrives as draft, got %v", rf)
	}
}

func TestInvalidResultIsRejected(t *testing.T) {
	s := storySetup(t, "auto")
	u := s.user("100")
	id, st := s.run(u, "story.start.v1", "s1", nil, map[string]any{"pages": -1, "extra": true})
	if st.Status != fsm.Failed || st.Verdict != codes.ReportInvalid {
		t.Fatalf("state %+v", st)
	}
	var facts int
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT count(*) FROM facts WHERE origin = $1`, "task:"+id.String()).Scan(&facts))
	if facts != 0 {
		t.Fatal("rejected result must not leave a fact")
	}
	if bal, res := s.wallet(u); bal != "100.0000" || res != "0.0000" {
		t.Fatalf("money: %s / %s", bal, res)
	}
	s.checkMoney()
}

func TestStaleBaseVersionIsRejected(t *testing.T) {
	s := storySetup(t, "auto")
	u := s.user("100")
	start, _ := s.run(u, "story.start.v1", "s1", nil, map[string]any{"title": "Т"})
	story := s.resultFact(u, start)["fact_id"].(string)

	// Две ветки из одного снимка v1: вторая фиксация — на устаревшей базе.
	var ids []uuid.UUID
	for _, key := range []string{"a", "b"} {
		raw, _ := json.Marshal(map[string]any{"story": story})
		env, _, err := s.e.CreateTask(s.ctx, u, CreateTaskRequest{FormID: "story.continue.v1", IdempotencyKey: key, Values: raw})
		must(t, err)
		ids = append(ids, taskID(env))
	}
	for _, id := range ids {
		c, err := s.e.Claim(s.ctx, s.acc, []int{1})
		must(t, err)
		if c == nil {
			t.Fatal("claim")
		}
		_, err = s.e.Accept(s.ctx, s.acc, id, true, 5)
		must(t, err)
	}
	first, err := s.e.Commit(s.ctx, s.acc, ids[0], CommitRequest{Outcome: "success", Result: map[string]any{"title": "A"}})
	must(t, err)
	second, err := s.e.Commit(s.ctx, s.acc, ids[1], CommitRequest{Outcome: "success", Result: map[string]any{"title": "B"}})
	must(t, err)
	if first.Status != fsm.Completed || second.Verdict != codes.ReportInvalid {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	s.checkMoney()
}

func TestEnsureServiceToken(t *testing.T) {
	s := setup(t)
	ctx := context.Background()
	if err := s.e.EnsureServiceToken(ctx, ProrabToken{Service: "tale", Name: "p", Token: "short"}); err == nil {
		t.Fatal("short token accepted")
	}
	if err := s.e.EnsureServiceToken(ctx, ProrabToken{Service: "nope", Name: "p", Token: uuid.NewString()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown service: %v", err)
	}
	first, second := uuid.NewString(), uuid.NewString()
	must(t, s.e.EnsureServiceToken(ctx, ProrabToken{Service: "tale", Name: "p", Token: first}))
	if _, err := s.e.AuthenticateService(ctx, first); err != nil {
		t.Fatal(err)
	}
	must(t, s.e.EnsureServiceToken(ctx, ProrabToken{Service: "tale", Name: "p", Token: second}))
	if _, err := s.e.AuthenticateService(ctx, first); !errors.Is(err, ErrNotFound) {
		t.Fatal("rotated token must stop working")
	}
	if _, err := s.e.AuthenticateService(ctx, second); err != nil {
		t.Fatal(err)
	}
}
