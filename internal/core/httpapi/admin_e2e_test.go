package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Панель админа (ADR-66): только роль admin; факты подразделения пишут прораб и админ, ядро их не понимает.
func TestAdminPanelOverHTTP(t *testing.T) {
	s := newStack(t)
	user := s.register("user@example.org")
	admin := s.register("admin@example.org")
	if _, err := s.pool.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE email = 'admin@example.org'`); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"/api/v1/admin/services", "/api/v1/admin/settings", "/api/v1/admin/services/taleweaver/facts?form_id=taleweaver.spend.v1"} {
		s.call("GET", p, "", nil, http.StatusUnauthorized)
		s.call("GET", p, user, nil, http.StatusForbidden)
	}
	s.call("POST", "/api/v1/admin/services/taleweaver/facts", user, map[string]any{"form_id": "taleweaver.spend.v1", "values": map[string]any{}}, http.StatusForbidden)

	// Схема бланков: подразделение и все его формы, с «кому» и полями.
	svcs := s.call("GET", "/api/v1/admin/services", admin, nil, http.StatusOK)["items"].([]any)
	tale := svcs[0].(map[string]any)
	audience := map[string]string{}
	for _, f := range tale["forms"].([]any) {
		m := f.(map[string]any)
		audience[m["form_id"].(string)] = m["audience"].(string)
	}
	if tale["code"] != "taleweaver" || audience["taleweaver.spend.v1"] != "adm" || audience["taleweaver.character.v1"] != "user" {
		t.Fatalf("services: %v", tale)
	}

	// Админ пишет факт подразделения; бланк пользователя или чужой — нельзя.
	fact := s.call("POST", "/api/v1/admin/services/taleweaver/facts", admin,
		map[string]any{"form_id": "taleweaver.spend.v1", "values": map[string]any{"model": "gpt-x", "tokens": 120}}, http.StatusCreated)
	if !strings.HasPrefix(fact["writer"].(string), "admin:") || fact["task_id"] != nil || fact["user_id"] != nil || fact["audience"] != "adm" {
		t.Fatalf("admin fact: %v", fact)
	}
	s.call("POST", "/api/v1/admin/services/taleweaver/facts", admin, map[string]any{"form_id": "taleweaver.character.v1", "values": map[string]any{}}, http.StatusUnprocessableEntity)
	s.call("POST", "/api/v1/admin/services/taleweaver/facts", admin, map[string]any{"form_id": "nope.v1", "values": map[string]any{}}, http.StatusUnprocessableEntity)

	// Последние факты — новые сверху; без form_id — 422.
	s.call("POST", "/api/v1/admin/services/taleweaver/facts", admin,
		map[string]any{"form_id": "taleweaver.spend.v1", "values": map[string]any{"model": "gpt-y", "tokens": 7}}, http.StatusCreated)
	items := s.call("GET", "/api/v1/admin/services/taleweaver/facts?form_id=taleweaver.spend.v1", admin, nil, http.StatusOK)["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["values"].(map[string]any)["model"] != "gpt-y" {
		t.Fatalf("latest facts: %v", items)
	}
	if one := s.call("GET", "/api/v1/admin/services/taleweaver/facts?form_id=taleweaver.spend.v1&limit=1", admin, nil, http.StatusOK)["items"].([]any); len(one) != 1 {
		t.Fatalf("limit: %v", one)
	}
	s.call("GET", "/api/v1/admin/services/taleweaver/facts", admin, nil, http.StatusUnprocessableEntity)

	// Настройки — числа и включатели, без секретов.
	st := s.call("GET", "/api/v1/admin/settings", admin, nil, http.StatusOK)
	raw, _ := json.Marshal(st)
	if st["overdraft_factor"] != "1.5" || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "postgres://") {
		t.Fatalf("settings: %s", raw)
	}
}
