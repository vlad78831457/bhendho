package httpapi

import (
	"context"
	"net/http"
	"testing"
)

// ADR-74: витрина — свои опубликованные факты админа, язык и порядок; открыта; нет языка — язык по умолчанию.
func TestShowcaseOverHTTP(t *testing.T) {
	s := newStack(t)
	user := s.register("user@example.org")
	admin := s.register("admin@example.org")
	if _, err := s.pool.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE email = 'admin@example.org'`); err != nil {
		t.Fatal(err)
	}
	fact := func(token, name string, publish bool) string {
		id := s.call("POST", "/api/v1/facts", token, map[string]any{"form_id": "taleweaver.character.v1",
			"values": map[string]any{"name": name}}, http.StatusCreated)["fact_id"].(string)
		if publish {
			s.call("POST", "/api/v1/facts/"+id+"/publish", token, nil, http.StatusOK)
		}
		return id
	}
	a, b := fact(admin, "Лисёнок", true), fact(admin, "Ёжик", true)
	draft, foreign := fact(admin, "Черновик", false), fact(user, "Чужой", true)

	add := func(token, id, lang string, pos, want int) map[string]any {
		return s.call("POST", "/api/v1/admin/showcase", token, map[string]any{"fact_id": id, "language": lang, "position": pos}, want)
	}
	add(user, a, "ru", 0, http.StatusForbidden)
	add(admin, draft, "ru", 0, http.StatusUnprocessableEntity)
	add(admin, foreign, "ru", 0, http.StatusUnprocessableEntity)
	add(admin, a, "Русский", 0, http.StatusUnprocessableEntity)
	add(admin, a, "ru", 2, http.StatusCreated)
	second := add(admin, b, "ru", 1, http.StatusCreated)
	add(admin, a, "en", 0, http.StatusCreated)

	names := func(lang string) []string {
		var out []string
		for _, it := range s.call("GET", "/api/v1/showcase?lang="+lang, "", nil, http.StatusOK)["items"].([]any) {
			out = append(out, it.(map[string]any)["values"].(map[string]any)["name"].(string))
		}
		return out
	}
	if got := names("ru"); len(got) != 2 || got[0] != "Ёжик" || got[1] != "Лисёнок" {
		t.Fatalf("ru showcase: %v", got)
	}
	if got := names("en"); len(got) != 1 || got[0] != "Лисёнок" {
		t.Fatalf("en showcase: %v", got)
	}
	if got := names("de"); len(got) != 2 { // нет мест на немецком — витрина языка по умолчанию
		t.Fatalf("fallback: %v", got)
	}
	s.call("DELETE", "/api/v1/admin/showcase/"+second["id"].(string), admin, nil, http.StatusNoContent)
	if got := names("ru"); len(got) != 1 {
		t.Fatalf("after remove: %v", got)
	}
	if items := s.call("GET", "/api/v1/admin/showcase", admin, nil, http.StatusOK)["items"].([]any); len(items) != 2 {
		t.Fatalf("admin list: %v", items)
	}
}
