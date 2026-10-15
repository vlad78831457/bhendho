package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
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

	// Тарифы (ADR-69): цена бланка меняется без перезапуска и сбрасывается.
	s.call("GET", "/api/v1/admin/tariffs", user, nil, http.StatusForbidden)
	tr := s.call("PUT", "/api/v1/admin/tariffs", admin, map[string]any{"scope": "form_price", "target": "taleweaver.generate_start.v1", "value": "175"}, http.StatusOK)
	if !hasPrice(tr, "taleweaver.generate_start.v1", "175.0000", true) {
		t.Fatalf("tariff set: %v", tr["forms"])
	}
	if tpl := s.call("GET", "/api/v1/forms/taleweaver.generate_start.v1", user, nil, http.StatusOK); fmt.Sprint(tpl["meta_ui"].(map[string]any)["price_gc"]) != "175" {
		t.Fatalf("template price: %v", tpl["meta_ui"])
	}
	tr = s.call("PUT", "/api/v1/admin/tariffs", admin, map[string]any{"scope": "form_price", "target": "taleweaver.generate_start.v1", "value": nil}, http.StatusOK)
	if !hasPrice(tr, "taleweaver.generate_start.v1", "150.0000", false) {
		t.Fatalf("tariff reset: %v", tr["forms"])
	}
	s.call("PUT", "/api/v1/admin/tariffs", admin, map[string]any{"scope": "overdraft_factor", "value": "0.5"}, http.StatusUnprocessableEntity)

	// Разметка подразделений (ADR-72) — открыта, как каталог; у выключенного подразделения её нет.
	views := s.call("GET", "/api/v1/views", "", nil, http.StatusOK)["items"].([]any)
	if len(views) != 1 || len(views[0].(map[string]any)["views"].([]any)) != 3 {
		t.Fatalf("views: %v", views)
	}
	s.call("PUT", "/api/v1/admin/services/taleweaver", admin, map[string]any{"enabled": false}, http.StatusNoContent)
	if off := s.call("GET", "/api/v1/views", "", nil, http.StatusOK)["items"].([]any); len(off) != 0 {
		t.Fatalf("views of a switched-off department: %v", off)
	}
	s.call("PUT", "/api/v1/admin/services/taleweaver", admin, map[string]any{"enabled": true}, http.StatusNoContent)

	// Настройки — числа и включатели, без секретов.
	st := s.call("GET", "/api/v1/admin/settings", admin, nil, http.StatusOK)
	raw, _ := json.Marshal(st)
	if st["overdraft_factor"] != "1.5" || strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "postgres://") {
		t.Fatalf("settings: %s", raw)
	}
}

func hasPrice(tariffs map[string]any, formID, price string, override bool) bool {
	for _, f := range tariffs["forms"].([]any) {
		m := f.(map[string]any)
		if m["form_id"] == formID {
			return m["price"] == price && m["override"] == override
		}
	}
	return false
}
