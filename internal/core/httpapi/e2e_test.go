package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"offgrid/core/internal/core/auth"
	"offgrid/core/internal/core/auth/oauth"
	"offgrid/core/internal/core/auth/oauth/oauthtest"
	"offgrid/core/internal/core/config"
	"offgrid/core/internal/core/contracts"
	"offgrid/core/internal/core/db/dbtest"
	"offgrid/core/internal/core/engine"
	"offgrid/core/internal/core/files"
	"offgrid/core/internal/core/modules"
	"offgrid/core/internal/core/outbox"
	"offgrid/core/internal/core/realtime"
)

type stack struct {
	t       *testing.T
	srv     *httptest.Server
	v       *contracts.Validator
	prorab  string
	issuer  string
	idp     *oauthtest.Fake // поддельные Google (OIDC) и VK ID (/vk), ADR-58
	cleanup context.CancelFunc
}

func newStack(t *testing.T) *stack {
	t.Helper()
	pool, url := dbtest.New(t)
	cfg := config.ForTests(url)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	v, err := contracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	hub := realtime.NewHub(pool, cfg.WSPingInterval, nil, log)
	eng := engine.New(pool, cfg, hub)
	eng.WithFiles(files.Local{Dir: t.TempDir()})

	// Эталонный манифест Taleweaver из contracts/ + формы для теста.
	dir := t.TempDir()
	manifest, err := os.ReadFile("../../../contracts/manifests/examples/taleweaver.module.yaml")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("module.yaml", string(manifest))
	write("forms/character.json", `{"title": "Персонаж", "fields_rules": {"name": {"type": "string", "required": true},
		"photo": {"type": "file", "allowed_file_types": ["image/jpeg", "image/png"]}}}`)
	write("forms/generate_start.json", `{"title": "Начать сказку", "category": "Сказки", "fields_rules": {
		"hero": {"type": "fact_ref", "required": true, "expected_fact_kind": "taleweaver.character.v1"},
		"mode": {"type": "select", "options": ["narrative", "interactive"]}}}`)
	write("forms/generate_continue.json", `{"title": "Продолжить", "fields_rules": {"choice_index": {"type": "int", "min": 0}}}`)
	mod, err := modules.Load(dir, v)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := eng.RegisterModule(ctx, mod); err != nil {
		t.Fatal(err)
	}
	token, err := eng.IssueServiceToken(ctx, "taleweaver", "prorab-e2e")
	if err != nil {
		t.Fatal(err)
	}

	go hub.Listen(ctx)
	go outbox.New(pool, cfg.OutboxBatch, cfg.OutboxInterval, log).Run(ctx)
	<-hub.Ready()

	issuer, err := eng.IssuePromoIssuerToken(ctx, "shop-e2e")
	if err != nil {
		t.Fatal(err)
	}
	idpSrv := httptest.NewUnstartedServer(nil)
	idp := oauthtest.New("http://"+idpSrv.Listener.Addr().String(), "", "app-1")
	idpSrv.Config.Handler = idp.Handler()
	idpSrv.Start()
	t.Cleanup(idpSrv.Close)
	providers, err := oauth.NewRegistry(oauth.Presets("app-1", "secret", idpSrv.URL, "app-1", "", idpSrv.URL+"/vk"), nil)
	if err != nil {
		t.Fatal(err)
	}
	authSvc := auth.New(pool, cfg.JWTSecret, cfg.AccessTTL, cfg.RefreshTTL).WithOAuth(providers)
	srv := httptest.NewServer(New(eng, authSvc, hub, log).WithFileLinks(files.NewSigner(cfg.JWTSecret), cfg.FileLinkTTL).Handler())
	t.Cleanup(func() { cancel(); srv.Close() })
	return &stack{t: t, srv: srv, v: v, prorab: token, issuer: issuer, idp: idp, cleanup: cancel}
}

func (s *stack) call(method, path, token string, body any, wantStatus int) map[string]any {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, s.srv.URL+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		s.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	out := map[string]any{}
	if len(raw) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		_ = dec.Decode(&out)
	}
	return out
}

func (s *stack) register(email string) string {
	s.t.Helper()
	out := s.call("POST", "/api/v1/auth/register", "", map[string]any{"email": email, "password": "longpassword"}, http.StatusCreated)
	return out["tokens"].(map[string]any)["access_token"].(string)
}

// socket читает события и проверяет каждое по реестру событий.
type socket struct {
	t    *testing.T
	conn *websocket.Conn
	v    *contracts.Validator
}

func (s *stack) dial(token string) *socket {
	s.t.Helper()
	url := "ws" + strings.TrimPrefix(s.srv.URL, "http") + "/api/v1/ws?access_token=" + token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { conn.CloseNow() })
	return &socket{t: s.t, conn: conn, v: s.v}
}

func (w *socket) next() map[string]any {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, raw, err := w.conn.Read(ctx)
	if err != nil {
		w.t.Fatalf("ws read: %v", err)
	}
	if err := w.v.ValidateEvent(raw); err != nil {
		w.t.Fatalf("event violates the registry: %v\n%s", err, raw)
	}
	var ev map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&ev)
	return ev
}

// until читает события до нужного типа (и условия), возвращает его и все durable-seq по пути.
func (w *socket) until(typ string, match func(payload map[string]any) bool) (map[string]any, []int64) {
	w.t.Helper()
	var seqs []int64
	for i := 0; i < 50; i++ {
		ev := w.next()
		if ev["type"] != "task.progress" && ev["type"] != "system.snapshot" {
			n, _ := ev["seq"].(json.Number).Int64()
			seqs = append(seqs, n)
		}
		if ev["type"] == typ && (match == nil || match(ev["payload"].(map[string]any))) {
			return ev, seqs
		}
	}
	w.t.Fatalf("event %s not received", typ)
	return nil, nil
}

func TestEndToEnd(t *testing.T) {
	s := newStack(t)

	s.call("GET", "/healthz", "", nil, http.StatusOK)
	s.call("GET", "/readyz", "", nil, http.StatusOK)
	s.call("GET", "/api/v1/wallets", "", nil, http.StatusUnauthorized)

	cat := s.call("GET", "/api/v1/catalog", "", nil, http.StatusOK)
	if len(cat["items"].([]any)) != 3 {
		t.Fatalf("catalog: %v", cat)
	}
	tpl := s.call("GET", "/api/v1/forms/taleweaver.generate_start.v1", "", nil, http.StatusOK)
	if tpl["meta_ui"].(map[string]any)["price_gc"] != json.Number("150") {
		t.Fatalf("template: %v", tpl)
	}

	token := s.register("parent@example.org")
	me := s.call("GET", "/api/v1/me", token, nil, http.StatusOK)
	if me["email"] != "parent@example.org" {
		t.Fatalf("me: %v", me)
	}
	ws := s.dial(token)
	snap := ws.next()
	if snap["type"] != "system.snapshot" {
		t.Fatalf("first event must be a snapshot: %v", snap)
	}

	s.call("POST", "/api/v1/deposits/demo", token, map[string]any{"amount": 1000}, http.StatusOK)
	ws.until("wallet.changed", nil)

	fact := s.call("POST", "/api/v1/facts", token, map[string]any{
		"form_id": "taleweaver.character.v1", "values": map[string]any{"name": "Лисёнок"}}, http.StatusCreated)
	factID := fact["fact_id"].(string)
	s.call("POST", "/api/v1/facts/"+factID+"/publish", token, nil, http.StatusOK)

	create := map[string]any{"form_id": "taleweaver.generate_start.v1", "idempotency_key": "tale-1",
		"values": map[string]any{"hero": factID, "mode": "interactive"}}
	task := s.call("POST", "/api/v1/tasks", token, create, http.StatusCreated)
	taskID := task["system"].(map[string]any)["document_id"].(string)
	again := s.call("POST", "/api/v1/tasks", token, create, http.StatusOK)
	if again["system"].(map[string]any)["document_id"] != taskID {
		t.Fatal("idempotent repeat returned another task")
	}
	raw, _ := json.Marshal(task)
	if err := s.v.ValidateBlank(raw); err != nil {
		t.Fatalf("task envelope: %v", err)
	}

	// Прораб: чужой сервис — 403, пустой токен — 401.
	s.call("POST", "/gateway/v1/claim", "", map[string]any{"target_service": "taleweaver", "schema_versions": []int{1}}, http.StatusUnauthorized)
	s.call("POST", "/gateway/v1/claim", s.prorab, map[string]any{"target_service": "spa", "schema_versions": []int{1}}, http.StatusForbidden)
	claimed := s.call("POST", "/gateway/v1/claim", s.prorab, map[string]any{"target_service": "taleweaver", "schema_versions": []int{1}}, http.StatusOK)
	if claimed["task_id"] != taskID {
		t.Fatalf("claimed %v", claimed["task_id"])
	}
	hero := claimed["blank"].(map[string]any)["values"].(map[string]any)["hero"].(map[string]any)
	if hero["values"].(map[string]any)["name"] != "Лисёнок" {
		t.Fatalf("worker must get the materialized snapshot: %v", hero)
	}
	s.call("POST", "/gateway/v1/claim", s.prorab, map[string]any{"target_service": "taleweaver", "schema_versions": []int{1}}, http.StatusNoContent)

	s.call("POST", "/gateway/v1/tasks/"+taskID+"/accept", s.prorab, map[string]any{"accepted": true, "eta_seconds": 30}, http.StatusOK)
	s.call("POST", "/gateway/v1/tasks/"+taskID+"/progress", s.prorab, map[string]any{"percent": 30, "phase": "text_ready"}, http.StatusAccepted)
	progress, _ := ws.until("task.progress", nil)
	if progress["payload"].(map[string]any)["phase"] != "text_ready" {
		t.Fatalf("progress: %v", progress)
	}
	s.call("POST", "/gateway/v1/tasks/"+taskID+"/heartbeat", s.prorab, nil, http.StatusOK)

	state := s.call("POST", "/gateway/v1/tasks/"+taskID+"/commit", s.prorab, map[string]any{
		"outcome": "success",
		"result":  map[string]any{"pages": 4},
		"report": map[string]any{"worker_charge": map[string]any{"lines": []any{
			map[string]any{"unit": "tokens", "units": 12400},
			map[string]any{"unit": "images", "units": 4},
			map[string]any{"unit": "tts_seconds", "units": 96},
		}}},
	}, http.StatusOK)
	if state["status"] != "completed" {
		t.Fatalf("commit: %v", state)
	}

	// Коммит пишет wallet.changed, task.status и task.result одной транзакцией.
	var (
		seqs       []int64
		lastWallet map[string]any
	)
	for i := 0; ; i++ {
		if i > 50 {
			t.Fatal("task.result not received")
		}
		ev := ws.next()
		if ev["type"] == "task.progress" {
			continue
		}
		n, _ := ev["seq"].(json.Number).Int64()
		seqs = append(seqs, n)
		if ev["type"] == "wallet.changed" {
			lastWallet = ev["payload"].(map[string]any)
		}
		if ev["type"] == "task.result" {
			break
		}
	}
	if lastWallet == nil || lastWallet["balance"] != "893.2000" {
		t.Fatalf("wallet after commit: %v", lastWallet)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("seq must grow by one without gaps: %v", seqs)
		}
	}

	done := s.call("GET", "/api/v1/tasks/"+taskID, token, nil, http.StatusOK)
	if done["report"].(map[string]any)["verdict"] != "SUCCESS" {
		t.Fatalf("task: %v", done["report"])
	}
	refused := s.call("POST", "/api/v1/tasks/"+taskID+"/commands", token, map[string]any{"command": "cancel"}, http.StatusConflict)
	if refused["report"].(map[string]any)["verdict"] != "COMMAND_REFUSED" {
		t.Fatalf("refused command: %v", refused)
	}
	page := s.call("GET", "/api/v1/tasks?status=completed", token, nil, http.StatusOK)
	if page["total"] != json.Number("1") {
		t.Fatalf("history: %v", page)
	}
	s.call("GET", "/api/v1/tasks?status=bogus", token, nil, http.StatusUnprocessableEntity)

	// Переподключение: snapshot несёт seq последнего доставленного события и баланс.
	ws2 := s.dial(token)
	snap2 := ws2.next()
	if snap2["seq"] != json.Number(itoa(seqs[len(seqs)-1])) {
		t.Fatalf("snapshot seq %v, last delivered %d", snap2["seq"], seqs[len(seqs)-1])
	}

	// Чужой пользователь не видит задачу и упирается в деньги.
	other := s.register("other@example.org")
	s.call("GET", "/api/v1/tasks/"+taskID, other, nil, http.StatusNotFound)
	otherFact := s.call("POST", "/api/v1/facts", other, map[string]any{
		"form_id": "taleweaver.character.v1", "values": map[string]any{"name": "Ёжик"}}, http.StatusCreated)
	s.call("POST", "/api/v1/facts/"+otherFact["fact_id"].(string)+"/publish", other, nil, http.StatusOK)
	poor := s.call("POST", "/api/v1/tasks", other, map[string]any{"form_id": "taleweaver.generate_start.v1",
		"idempotency_key": "x", "values": map[string]any{"hero": otherFact["fact_id"]}}, http.StatusPaymentRequired)
	if poor["report"].(map[string]any)["verdict"] != "INSUFFICIENT_FUNDS" {
		t.Fatalf("poor: %v", poor)
	}
	invalid := s.call("POST", "/api/v1/tasks", other, map[string]any{"form_id": "taleweaver.generate_start.v1",
		"idempotency_key": "y", "values": map[string]any{"mode": "epic"}}, http.StatusUnprocessableEntity)
	if len(invalid["report"].(map[string]any)["field_errors"].([]any)) != 2 {
		t.Fatalf("field errors: %v", invalid)
	}
}

func TestRefreshFlow(t *testing.T) {
	s := newStack(t)
	out := s.call("POST", "/api/v1/auth/register", "", map[string]any{"email": "r@example.org", "password": "longpassword"}, http.StatusCreated)
	refresh := out["tokens"].(map[string]any)["refresh_token"].(string)
	s.call("POST", "/api/v1/auth/login", "", map[string]any{"email": "r@example.org", "password": "nope-nope"}, http.StatusUnauthorized)
	rotated := s.call("POST", "/api/v1/auth/refresh", "", map[string]any{"refresh_token": refresh}, http.StatusOK)
	access := rotated["tokens"].(map[string]any)["access_token"].(string)
	s.call("GET", "/api/v1/me", access, nil, http.StatusOK)
	s.call("POST", "/api/v1/auth/refresh", "", map[string]any{"refresh_token": refresh}, http.StatusUnauthorized)
	s.call("POST", "/api/v1/auth/register", "", map[string]any{"email": "r@example.org", "password": "longpassword"}, http.StatusConflict)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
