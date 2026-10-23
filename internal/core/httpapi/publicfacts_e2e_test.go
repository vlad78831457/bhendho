package httpapi

import (
	"context"
	"net/http"
	"testing"
)

// ADR-79: на публичный факт админа ссылается любой — в задаче, без копии; снимок несёт его тело и файлы.
// Публичным делает только админ и только свой факт; снова приватный — новые ссылки не проходят.
func TestPublicFactsOverHTTP(t *testing.T) {
	s := newStack(t)
	user := s.register("user@example.org")
	admin := s.register("admin@example.org")
	if _, err := s.pool.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE email = 'admin@example.org'`); err != nil {
		t.Fatal(err)
	}

	fileID := s.upload(admin, pngBytes(t, 20, 20), http.StatusCreated)["file"].(map[string]any)["file_id"].(string)
	hero := s.call("POST", "/api/v1/facts", admin, map[string]any{"form_id": "taleweaver.character.v1",
		"values": map[string]any{"name": "Лиса", "photo": fileID}}, http.StatusCreated)["fact_id"].(string)
	s.call("POST", "/api/v1/facts/"+hero+"/publish", admin, nil, http.StatusOK)
	own := s.call("POST", "/api/v1/facts", user, map[string]any{"form_id": "taleweaver.character.v1",
		"values": map[string]any{"name": "Свой"}}, http.StatusCreated)["fact_id"].(string)

	s.call("POST", "/api/v1/deposits/demo", user, map[string]any{"amount": 1000}, http.StatusOK)
	task := func(key string, want int) map[string]any {
		return s.call("POST", "/api/v1/tasks", user, map[string]any{"form_id": "taleweaver.generate_start.v1",
			"idempotency_key": key, "values": map[string]any{"hero": hero}}, want)
	}
	task("private", http.StatusUnprocessableEntity) // приватный чужой факт — как раньше

	visibility := func(token, id, v string, want int) map[string]any {
		return s.call("PUT", "/api/v1/admin/facts/"+id+"/visibility", token, map[string]any{"visibility": v}, want)
	}
	visibility(user, hero, "public", http.StatusForbidden)
	visibility(admin, own, "public", http.StatusUnprocessableEntity) // чужой факт админ публичным не делает
	visibility(admin, hero, "everyone", http.StatusUnprocessableEntity)
	if got := visibility(admin, hero, "public", http.StatusOK); got["visibility"] != "public" {
		t.Fatalf("visibility: %v", got)
	}

	s.call("GET", "/api/v1/public/facts", "", nil, http.StatusUnauthorized)
	items := s.call("GET", "/api/v1/public/facts?kind=taleweaver.character.v1", user, nil, http.StatusOK)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["values"].(map[string]any)["name"] != "Лиса" {
		t.Fatalf("public list: %v", items)
	}
	if items := s.call("GET", "/api/v1/public/facts?kind=taleweaver.tale.v1", user, nil, http.StatusOK)["items"].([]any); len(items) != 0 {
		t.Fatalf("other kind: %v", items)
	}
	s.call("GET", "/api/v1/facts/"+hero, user, nil, http.StatusNotFound) // кабинет показывает только свои

	taskID := task("public", http.StatusCreated)["system"].(map[string]any)["document_id"].(string)
	claimed := s.call("POST", "/gateway/v1/claim", s.prorab, map[string]any{"target_service": "taleweaver", "schema_versions": []int{1}}, http.StatusOK)
	snap := claimed["blank"].(map[string]any)["values"].(map[string]any)["hero"].(map[string]any)
	values := snap["values"].(map[string]any)
	if snap["fact_id"] != hero || values["name"] != "Лиса" || values["photo"].(map[string]any)["file_id"] != fileID {
		t.Fatalf("snapshot: %v", snap)
	}
	code, _, got := s.raw("GET", "/gateway/v1/tasks/"+taskID+"/files/"+fileID, s.prorab, "", nil)
	if code != http.StatusOK || len(got) == 0 {
		t.Fatalf("file of the public fact: %d", code)
	}

	visibility(admin, hero, "private", http.StatusOK)
	task("private-again", http.StatusUnprocessableEntity)
	if items := s.call("GET", "/api/v1/public/facts", user, nil, http.StatusOK)["items"].([]any); len(items) != 0 {
		t.Fatalf("after private: %v", items)
	}
}
