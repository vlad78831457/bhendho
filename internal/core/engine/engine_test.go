package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/codes"
	"offgrid/core/internal/core/config"
	"offgrid/core/internal/core/contracts"
	"offgrid/core/internal/core/db/dbtest"
	"offgrid/core/internal/core/fsm"
	"offgrid/core/internal/core/modules"
)

const testManifest = `
module_id: test/tale
name: Test Tale
version: 1.0.0
mode: worker_service
worker: {runtime: python, target_service: tale}
capabilities: {}
forms_provided:
  - {form_id: tale.character.v1, kind: fact, title: Персонаж, schema_file: forms/character.json}
  - {form_id: tale.start.v1, kind: task, title: Сказка, schema_file: forms/start.json, base_price_gc: 150}
worker_accounting:
  base_unit: {name: tokens, rate_gc: 0.001}
  extra_units:
    - {name: images, rate_to_base: 20000}
    - {name: tts_seconds, rate_to_base: 150}
`

const characterForm = `{"title": "Персонаж", "fields_rules": {
	"name": {"type": "string", "required": true},
	"role": {"type": "select", "options": ["hero", "companion"]}}}`

const startForm = `{"title": "Сказка", "category": "Сказки", "fields_rules": {
	"hero":   {"type": "fact_ref", "required": true, "expected_fact_kind": "tale.character.v1"},
	"wishes": {"type": "text", "max": 200},
	"photo":  {"type": "file"}}}`

type env struct {
	t   *testing.T
	ctx context.Context
	e   *Engine
	v   *contracts.Validator
	acc ServiceAccount
}

func writeModule(t *testing.T, manifest, start string) string {
	t.Helper()
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "forms"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "module.yaml"), []byte(manifest), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "forms", "character.json"), []byte(characterForm), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "forms", "start.json"), []byte(start), 0o644))
	return dir
}

func setup(t *testing.T, tweak ...func(*config.Config)) *env {
	t.Helper()
	pool, url := dbtest.New(t)
	cfg := config.ForTests(url)
	for _, f := range tweak {
		f(&cfg)
	}
	v, err := contracts.Load()
	must(t, err)
	ctx := context.Background()
	e := New(pool, cfg, nil)
	mod, err := modules.Load(writeModule(t, testManifest, startForm), v)
	must(t, err)
	must(t, e.RegisterModule(ctx, mod))
	token, err := e.IssueServiceToken(ctx, "tale", "prorab-1")
	must(t, err)
	acc, err := e.AuthenticateService(ctx, token)
	must(t, err)
	return &env{t: t, ctx: ctx, e: e, v: v, acc: acc}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (s *env) user(deposit string) uuid.UUID {
	s.t.Helper()
	id := uuid.New()
	_, err := s.e.pool.Exec(s.ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`, id, id.String()+"@test")
	must(s.t, err)
	if deposit != "" {
		_, err := s.e.DemoDeposit(s.ctx, id, decimal.RequireFromString(deposit))
		must(s.t, err)
	}
	return id
}

func (s *env) hero(user uuid.UUID, publish bool) uuid.UUID {
	s.t.Helper()
	f, err := s.e.CreateFact(s.ctx, user, "tale.character.v1", []byte(`{"name": "Лисёнок", "role": "hero"}`))
	must(s.t, err)
	if publish {
		_, err = s.e.PublishFact(s.ctx, user, f.ID, 0)
		must(s.t, err)
	}
	return f.ID
}

func (s *env) create(user, hero uuid.UUID, key string) (map[string]any, error) {
	env, _, err := s.e.CreateTask(s.ctx, user, CreateTaskRequest{
		FormID: "tale.start.v1", IdempotencyKey: key,
		Values: json.RawMessage(`{"hero": "` + hero.String() + `", "wishes": "осенний лес"}`),
	})
	return env, err
}

func taskID(env map[string]any) uuid.UUID {
	return uuid.MustParse(env["system"].(map[string]any)["document_id"].(string))
}

func (s *env) wallet(user uuid.UUID) (balance, reserved string) {
	s.t.Helper()
	ws, err := s.e.Wallets(s.ctx, user)
	must(s.t, err)
	return ws[0].Balance, ws[0].Reserved
}

func (s *env) status(id uuid.UUID) (fsm.Status, codes.Code, int) {
	s.t.Helper()
	var st, verdict string
	var attempts int
	must(s.t, s.e.pool.QueryRow(s.ctx, `SELECT status, COALESCE(verdict, ''), attempts FROM system_tasks WHERE id = $1`, id).
		Scan(&st, &verdict, &attempts))
	return fsm.Status(st), codes.Code(verdict), attempts
}

// checkMoney — сумма балансов и резервов сходится с журналом (критерий приёмки 5.2).
func (s *env) checkMoney() {
	s.t.Helper()
	var bad int
	must(s.t, s.e.pool.QueryRow(s.ctx, `
		SELECT count(*) FROM wallets w
		LEFT JOIN LATERAL (
			SELECT
				COALESCE(sum(amount) FILTER (WHERE type IN ('deposit', 'income')), 0)
				  - COALESCE(sum(amount) FILTER (WHERE type = 'charge'), 0) AS balance,
				COALESCE(sum(amount) FILTER (WHERE type = 'reserve'), 0)
				  - COALESCE(sum(amount) FILTER (WHERE type IN ('release', 'charge')), 0) AS reserved
			FROM transactions t WHERE t.wallet_id = w.id) j ON TRUE
		WHERE w.balance <> j.balance OR (w.owner_kind = 'user' AND w.reserved <> j.reserved)`).Scan(&bad))
	if bad != 0 {
		s.t.Fatalf("%d wallets diverge from the transaction journal", bad)
	}
}

func (s *env) claimOne() *ClaimedTask {
	s.t.Helper()
	c, err := s.e.Claim(s.ctx, s.acc, []int{1})
	must(s.t, err)
	return c
}

func (s *env) expireLock(id uuid.UUID) {
	s.t.Helper()
	_, err := s.e.pool.Exec(s.ctx, `UPDATE system_tasks SET locked_until = now() - interval '1 second' WHERE id = $1`, id)
	must(s.t, err)
}

func refusalCode(err error) codes.Code {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

func TestHappyPathMultiUnitBilling(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	hero := s.hero(u, true)

	created, err := s.create(u, hero, "k1")
	must(t, err)
	raw, _ := json.Marshal(created)
	must(t, s.v.ValidateBlank(raw))
	if bal, res := s.wallet(u); bal != "1000.0000" || res != "150.0000" {
		t.Fatalf("after create: balance %s reserved %s", bal, res)
	}

	c := s.claimOne()
	if c == nil || c.TaskID != taskID(created) {
		t.Fatalf("claim = %+v", c)
	}
	raw, _ = json.Marshal(c.Blank)
	must(t, s.v.ValidateBlank(raw))
	snapHero := c.Blank["values"].(map[string]any)["hero"].(map[string]any)
	if snapHero["values"].(map[string]any)["name"] != "Лисёнок" || snapHero["version"] != json.Number("1") {
		t.Fatalf("materialized hero = %v", snapHero)
	}

	st, err := s.e.Accept(s.ctx, s.acc, c.TaskID, true, 10)
	must(t, err)
	if st.Attempts != 1 || st.Status != fsm.Processing {
		t.Fatalf("accept state = %+v", st)
	}
	must(t, s.e.Progress(s.ctx, s.acc, c.TaskID, 30, "text_ready"))

	var report map[string]any
	must(t, json.Unmarshal([]byte(`{"worker_charge": {"lines": [
		{"unit": "tokens", "units": 12400}, {"unit": "images", "units": 4}, {"unit": "tts_seconds", "units": 96}]},
		"admin_message": "ok", "user_message": "должно быть вырезано"}`), &report))
	st, err = s.e.Commit(s.ctx, s.acc, c.TaskID, CommitRequest{Outcome: "success", Result: map[string]any{"pages": 4}, Report: report})
	must(t, err)
	if st.Status != fsm.Completed || st.Verdict != codes.Success {
		t.Fatalf("commit state = %+v", st)
	}
	if bal, res := s.wallet(u); bal != "893.2000" || res != "0.0000" {
		t.Fatalf("after commit: balance %s reserved %s (want 893.2 / 0)", bal, res)
	}
	var income string
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT balance::text FROM wallets WHERE owner_kind = 'service'`).Scan(&income))
	if income != "106.8000" {
		t.Fatalf("service income = %s", income)
	}

	final, err := s.e.GetTask(s.ctx, u, c.TaskID)
	must(t, err)
	rep := final["report"].(map[string]any)
	if rep["user_message"] != "Готово" {
		t.Fatalf("worker text must not reach the user: %v", rep["user_message"])
	}
	raw, _ = json.Marshal(final)
	must(t, s.v.ValidateBlank(raw))
	s.checkMoney()

	var events int
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT count(*) FROM outbox_queue WHERE user_id = $1 AND type = 'task.result'`, u).Scan(&events))
	if events != 1 {
		t.Fatalf("task.result events = %d", events)
	}
}

func TestIdempotentCreate(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	hero := s.hero(u, true)
	a, err := s.create(u, hero, "same")
	must(t, err)
	b, err := s.create(u, hero, "same")
	must(t, err)
	if taskID(a) != taskID(b) {
		t.Fatal("repeat with the same key must return the same task")
	}
	if _, res := s.wallet(u); res != "150.0000" {
		t.Fatalf("reserved twice: %s", res)
	}
	s.checkMoney()
}

func TestConcurrentIdempotentCreate(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	hero := s.hero(u, true)
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env, err := s.create(u, hero, "race")
			if err == nil {
				ids <- taskID(env)
			}
		}()
	}
	wg.Wait()
	close(ids)
	first := uuid.Nil
	for id := range ids {
		if first == uuid.Nil {
			first = id
		} else if id != first {
			t.Fatal("different tasks for one idempotency key")
		}
	}
	if _, res := s.wallet(u); res != "150.0000" {
		t.Fatalf("reserved = %s", res)
	}
	s.checkMoney()
}

func TestCreateRefusals(t *testing.T) {
	s := setup(t)
	poor := s.user("")
	hero := s.hero(poor, true)
	if _, err := s.create(poor, hero, "k"); refusalCode(err) != codes.InsufficientFunds {
		t.Fatalf("want INSUFFICIENT_FUNDS, got %v", err)
	}

	u := s.user("1000")
	draft := s.hero(u, false)
	if _, err := s.create(u, draft, "k"); refusalCode(err) != codes.FactNotPublished {
		t.Fatalf("want FACT_NOT_PUBLISHED, got %v", err)
	}

	_, _, err := s.e.CreateTask(s.ctx, u, CreateTaskRequest{FormID: "tale.start.v1", IdempotencyKey: "k2",
		Values: json.RawMessage(`{"wishes": 5, "price": 0}`)})
	var r *Refusal
	if !errors.As(err, &r) || r.Code != codes.ValidationError || len(r.FieldErrors) != 3 {
		t.Fatalf("want 3 field errors, got %v", err)
	}

	other := s.user("1000")
	foreign := s.hero(other, true)
	if _, err := s.create(u, foreign, "k3"); refusalCode(err) != codes.ValidationError {
		t.Fatalf("foreign fact must be refused, got %v", err)
	}

	if bal, res := s.wallet(u); bal != "1000.0000" || res != "0.0000" {
		t.Fatalf("refusals must not touch money: %s / %s", bal, res)
	}
	var tasks int
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT count(*) FROM system_tasks`).Scan(&tasks))
	if tasks != 0 {
		t.Fatalf("refusals created %d tasks", tasks)
	}
	s.checkMoney()
}

func TestFactMissingAtClaim(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	hero := s.hero(u, true)
	created, err := s.create(u, hero, "k")
	must(t, err)
	must(t, s.e.DeleteFact(s.ctx, u, hero))

	if c := s.claimOne(); c != nil {
		t.Fatalf("task with a missing fact must not reach the worker: %+v", c)
	}
	if st, verdict, _ := s.status(taskID(created)); st != fsm.Failed || verdict != codes.FactMissing {
		t.Fatalf("status %s verdict %s", st, verdict)
	}
	if _, res := s.wallet(u); res != "0.0000" {
		t.Fatalf("reserve not released: %s", res)
	}
	if _, err := s.e.Command(s.ctx, u, taskID(created), fsm.Retry, "r1"); refusalCode(err) != codes.CommandRefused {
		t.Fatalf("failed by data must not be retried: %v", err)
	}
	s.checkMoney()
}

func TestCancelAndRefusedCommands(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	hero := s.hero(u, true)
	created, err := s.create(u, hero, "k")
	must(t, err)
	id := taskID(created)

	if _, err := s.e.Command(s.ctx, u, id, fsm.Retry, "r"); refusalCode(err) != codes.CommandRefused {
		t.Fatalf("retry from pending: %v", err)
	}
	env, err := s.e.Command(s.ctx, u, id, fsm.Cancel, "")
	must(t, err)
	if env["system"].(map[string]any)["status"] != "cancelled" {
		t.Fatalf("cancel: %v", env["system"])
	}
	if _, err := s.e.Command(s.ctx, u, id, fsm.Cancel, ""); refusalCode(err) != codes.CommandRefused {
		t.Fatalf("second cancel: %v", err)
	}
	if _, err := s.e.Command(s.ctx, u, id, "delete", ""); refusalCode(err) != codes.CommandRefused {
		t.Fatalf("unknown command: %v", err)
	}
	stranger := s.user("")
	if _, err := s.e.Command(s.ctx, stranger, id, fsm.Cancel, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign task: %v", err)
	}

	var refusedTraces int
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT count(*) FROM task_trace WHERE task_id = $1 AND code = 'COMMAND_REFUSED'`, id).Scan(&refusedTraces))
	if refusedTraces != 3 {
		t.Fatalf("every refused command must leave a trace, got %d", refusedTraces)
	}
	if bal, res := s.wallet(u); bal != "1000.0000" || res != "0.0000" {
		t.Fatalf("money after cancel: %s / %s", bal, res)
	}
	s.checkMoney()
}

func TestTimeoutsLimitAndRetry(t *testing.T) {
	s := setup(t, func(c *config.Config) { c.MaxAttempts = 2 })
	u := s.user("1000")
	hero := s.hero(u, true)
	created, err := s.create(u, hero, "k")
	must(t, err)
	id := taskID(created)

	for attempt := 1; attempt <= 2; attempt++ {
		c := s.claimOne()
		if c == nil || c.Attempt != attempt {
			t.Fatalf("attempt %d: claim = %+v", attempt, c)
		}
		_, err := s.e.Accept(s.ctx, s.acc, id, true, 5)
		must(t, err)
		s.expireLock(id)
		n, err := s.e.SweepExpired(s.ctx)
		must(t, err)
		if n != 1 {
			t.Fatalf("sweep handled %d", n)
		}
	}
	st, verdict, attempts := s.status(id)
	if st != fsm.Failed || verdict != codes.LimitExhausted || attempts != 2 {
		t.Fatalf("after limit: %s %s %d", st, verdict, attempts)
	}
	var timeouts int
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT count(*) FROM task_trace WHERE task_id = $1 AND code = 'WORKER_TIMEOUT'`, id).Scan(&timeouts))
	if timeouts != 1 {
		t.Fatalf("WORKER_TIMEOUT traces = %d", timeouts)
	}
	if _, res := s.wallet(u); res != "0.0000" {
		t.Fatalf("reserve after LIMIT_EXHAUSTED: %s", res)
	}

	retried, err := s.e.Command(s.ctx, u, id, fsm.Retry, "retry-1")
	must(t, err)
	newID := taskID(retried)
	if newID == id {
		t.Fatal("retry must create a new task")
	}
	again, err := s.e.Command(s.ctx, u, id, fsm.Retry, "retry-1")
	must(t, err)
	if taskID(again) != newID {
		t.Fatal("retry must be idempotent by key")
	}
	must(t, s.e.DeleteFact(s.ctx, u, hero))
	c := s.claimOne()
	if c == nil || c.TaskID != newID {
		t.Fatalf("retry must reuse the snapshot even after the fact is gone: %+v", c)
	}
	if _, res := s.wallet(u); res != "150.0000" {
		t.Fatalf("retry reserve: %s", res)
	}
	s.checkMoney()
}

func TestDeclineAndClaimExpiryDoNotConsumeAttempts(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	created, err := s.create(u, s.hero(u, true), "k")
	must(t, err)
	id := taskID(created)

	s.claimOne()
	st, err := s.e.Accept(s.ctx, s.acc, id, false, 0)
	must(t, err)
	if st.Status != fsm.Pending || st.Attempts != 0 {
		t.Fatalf("decline: %+v", st)
	}
	s.claimOne()
	s.expireLock(id)
	_, err = s.e.SweepExpired(s.ctx)
	must(t, err)
	if st, _, attempts := s.status(id); st != fsm.Pending || attempts != 0 {
		t.Fatalf("claim expiry: %s attempts %d", st, attempts)
	}
}

func TestCommitDedupAndOwnership(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	created, err := s.create(u, s.hero(u, true), "k")
	must(t, err)
	id := taskID(created)
	s.claimOne()

	token, err := s.e.IssueServiceToken(s.ctx, "tale", "prorab-2")
	must(t, err)
	other, err := s.e.AuthenticateService(s.ctx, token)
	must(t, err)
	if _, err := s.e.Accept(s.ctx, other, id, true, 5); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("foreign accept: %v", err)
	}

	commit := CommitRequest{Outcome: "success", Report: map[string]any{"worker_charge": map[string]any{
		"lines": []any{map[string]any{"unit": "tokens", "units": json.Number("1000")}}}}}
	if st, _ := s.e.Commit(s.ctx, s.acc, id, commit); st.Status != fsm.Processing {
		t.Fatalf("commit before accept must be ignored: %+v", st)
	}
	_, err = s.e.Accept(s.ctx, s.acc, id, true, 5)
	must(t, err)
	if st, _ := s.e.Commit(s.ctx, other, id, commit); st.Status != fsm.Processing {
		t.Fatalf("foreign commit must be ignored: %+v", st)
	}
	st, err := s.e.Commit(s.ctx, s.acc, id, commit)
	must(t, err)
	st2, err := s.e.Commit(s.ctx, s.acc, id, commit)
	must(t, err)
	if st.Status != fsm.Completed || st2.Status != fsm.Completed {
		t.Fatalf("commit: %+v / %+v", st, st2)
	}
	if bal, _ := s.wallet(u); bal != "999.0000" {
		t.Fatalf("double charge? balance %s", bal)
	}
	s.checkMoney()
}

func TestInvalidReportsAndWorkerError(t *testing.T) {
	cases := map[string]struct {
		req  CommitRequest
		code codes.Code
	}{
		"unknown unit": {CommitRequest{Outcome: "success", Report: map[string]any{"worker_charge": map[string]any{
			"lines": []any{map[string]any{"unit": "gpu_seconds", "units": json.Number("1")}}}}}, codes.ReportInvalid},
		"metered without charge": {CommitRequest{Outcome: "success"}, codes.ReportInvalid},
		"garbage outcome":        {CommitRequest{Outcome: "maybe"}, codes.ReportInvalid},
		"worker error":           {CommitRequest{Outcome: "error", Report: map[string]any{"admin_message": "provider 503"}}, codes.WorkerError},
		"data rejected":          {CommitRequest{Outcome: "rejected", Report: map[string]any{"admin_message": "graph is directed"}}, codes.DataRejected},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := setup(t)
			u := s.user("1000")
			created, err := s.create(u, s.hero(u, true), "k")
			must(t, err)
			id := taskID(created)
			s.claimOne()
			_, err = s.e.Accept(s.ctx, s.acc, id, true, 5)
			must(t, err)
			st, err := s.e.Commit(s.ctx, s.acc, id, tc.req)
			must(t, err)
			if st.Status != fsm.Failed || st.Verdict != tc.code {
				t.Fatalf("state %+v, want failed/%s", st, tc.code)
			}
			if bal, res := s.wallet(u); bal != "1000.0000" || res != "0.0000" {
				t.Fatalf("failed task must not cost money: %s / %s", bal, res)
			}
			_, err = s.e.Command(s.ctx, u, id, fsm.Retry, "r")
			if codes.Retryable(tc.code) && err != nil {
				t.Fatalf("failure by execution must allow retry: %v", err)
			}
			if !codes.Retryable(tc.code) && refusalCode(err) != codes.CommandRefused {
				t.Fatalf("failure by data must not allow retry: %v", err)
			}
			s.checkMoney()
		})
	}
}

func TestGroupOrderAndVersionRouting(t *testing.T) {
	s := setup(t)
	u := s.user("1000")
	hero := s.hero(u, true)
	group := uuid.New()
	var ids []uuid.UUID
	for _, key := range []string{"g1", "g2"} {
		env, _, err := s.e.CreateTask(s.ctx, u, CreateTaskRequest{FormID: "tale.start.v1", IdempotencyKey: key, GroupID: &group,
			Values: json.RawMessage(`{"hero": "` + hero.String() + `"}`)})
		must(t, err)
		ids = append(ids, taskID(env))
	}
	if c, err := s.e.Claim(s.ctx, s.acc, []int{99}); err != nil || c != nil {
		t.Fatalf("claim of an unknown version: %+v %v", c, err)
	}
	c := s.claimOne()
	if c == nil || c.TaskID != ids[0] {
		t.Fatalf("group must start with the first task: %+v", c)
	}
	if c2 := s.claimOne(); c2 != nil {
		t.Fatalf("second task of the group must wait: %+v", c2)
	}
	_, err := s.e.Accept(s.ctx, s.acc, ids[0], true, 5)
	must(t, err)
	_, err = s.e.Commit(s.ctx, s.acc, ids[0], CommitRequest{Outcome: "error"})
	must(t, err)
	if c3 := s.claimOne(); c3 == nil || c3.TaskID != ids[1] {
		t.Fatalf("after the first finished the second goes: %+v", c3)
	}
}

func TestConcurrentClaimsAreExclusive(t *testing.T) {
	s := setup(t)
	u := s.user("10000")
	hero := s.hero(u, true)
	const n = 20
	for i := 0; i < n; i++ {
		_, err := s.create(u, hero, uuid.NewString())
		must(t, err)
	}
	var (
		mu   sync.Mutex
		seen = map[uuid.UUID]int{}
		wg   sync.WaitGroup
	)
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				c, err := s.e.Claim(s.ctx, s.acc, []int{1})
				if err != nil {
					t.Error(err)
					return
				}
				if c == nil {
					return
				}
				mu.Lock()
				seen[c.TaskID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d distinct tasks, want %d", len(seen), n)
	}
	for id, k := range seen {
		if k != 1 {
			t.Fatalf("task %s claimed %d times", id, k)
		}
	}
}

func TestModuleReRegistrationVersionsForms(t *testing.T) {
	s := setup(t)
	mod, err := modules.Load(writeModule(t, testManifest, startForm), s.v)
	must(t, err)
	must(t, s.e.RegisterModule(s.ctx, mod))
	var versions int
	must(t, s.e.pool.QueryRow(s.ctx, `SELECT count(*) FROM forms WHERE form_id = 'tale.start.v1'`).Scan(&versions))
	if versions != 1 {
		t.Fatalf("unchanged form must not get a new version, have %d", versions)
	}

	u := s.user("1000")
	old, err := s.create(u, s.hero(u, true), "before")
	must(t, err)

	changed := `{"title": "Сказка", "fields_rules": {
		"hero": {"type": "fact_ref", "required": true, "expected_fact_kind": "tale.character.v1"},
		"mood": {"type": "select", "options": ["calm", "fun"]}}}`
	mod, err = modules.Load(writeModule(t, testManifest, changed), s.v)
	must(t, err)
	must(t, s.e.RegisterModule(s.ctx, mod))
	cat, err := s.e.Catalog(s.ctx)
	must(t, err)
	for _, item := range cat {
		if item.FormID == "tale.start.v1" && item.SchemaVersion != 2 {
			t.Fatalf("catalog must show v2, got %+v", item)
		}
	}
	got, err := s.e.GetTask(s.ctx, u, taskID(old))
	must(t, err)
	if got["system"].(map[string]any)["schema_version"] != 1 {
		t.Fatal("existing task keeps its schema_version (ADR-19)")
	}
	if c, _ := s.e.Claim(s.ctx, s.acc, []int{2}); c != nil {
		t.Fatal("v2 workers must not take v1 tasks")
	}
	if c := s.claimOne(); c == nil {
		t.Fatal("v1 workers still drain their queue")
	}
}

// Каталог кэшируется на TTL, а регистрация модуля в этом инстансе кэш сбрасывает (ADR-55).
func TestCatalogCacheAndReset(t *testing.T) {
	s := setup(t, func(c *config.Config) { c.CatalogTTL = time.Hour })
	first, err := s.e.Catalog(s.ctx)
	must(t, err)
	_, err = s.e.pool.Exec(s.ctx, `UPDATE forms SET status = 'withdrawn' WHERE form_id = 'tale.start.v1'`)
	must(t, err)
	cached, err := s.e.Catalog(s.ctx)
	must(t, err)
	if len(cached) != len(first) {
		t.Fatalf("within TTL the catalog must come from memory: %d vs %d", len(cached), len(first))
	}
	s.e.catalog.reset()
	fresh, err := s.e.Catalog(s.ctx)
	must(t, err)
	if len(fresh) != len(first)-1 {
		t.Fatalf("after reset the withdrawn form must disappear: %d items, was %d", len(fresh), len(first))
	}
	mod, err := modules.Load(writeModule(t, testManifest, startForm), s.v)
	must(t, err)
	must(t, s.e.RegisterModule(s.ctx, mod))
	if s.e.catalog.items != nil {
		t.Fatal("RegisterModule must reset the catalog cache")
	}
}
