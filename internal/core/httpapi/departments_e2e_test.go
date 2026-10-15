package httpapi

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

const echoModule = "../../../pkg/sdk/python/tests/e2e/module/"

func echoFiles(t *testing.T) (string, map[string]any) {
	t.Helper()
	read := func(p string) string {
		b, err := os.ReadFile(echoModule + p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return read("module.yaml"), map[string]any{"forms/note.json": read("forms/note.json"), "forms/run.json": read("forms/run.json")}
}

func (s *stack) catalogHas(token, formID string) bool {
	for _, it := range s.call("GET", "/api/v1/catalog", token, nil, http.StatusOK)["items"].([]any) {
		if it.(map[string]any)["form_id"] == formID {
			return true
		}
	}
	return false
}

// ADR-70: подразделение подключается само по ключу админа, без перезапуска ядра; ключ — и токен прораба;
// выключенное подразделение не видно и задач не получает.
func TestDepartmentSelfRegistration(t *testing.T) {
	s := newStack(t)
	user := s.register("user@example.org")
	admin := s.register("admin@example.org")
	if _, err := s.pool.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE email = 'admin@example.org'`); err != nil {
		t.Fatal(err)
	}
	manifest, files := echoFiles(t)
	body := map[string]any{"module_yaml": manifest, "files": files}

	s.call("POST", "/api/v1/admin/departments/keys", user, map[string]any{"code": "echo"}, http.StatusForbidden)
	s.call("POST", "/api/v1/admin/departments/keys", admin, map[string]any{"code": "Эхо"}, http.StatusUnprocessableEntity)
	key := s.call("POST", "/api/v1/admin/departments/keys", admin, map[string]any{"code": "echo"}, http.StatusCreated)["key"].(string)
	foreign := s.call("POST", "/api/v1/admin/departments/keys", admin, map[string]any{"code": "other"}, http.StatusCreated)["key"].(string)

	s.call("POST", "/gateway/v1/register", "dk_wrong", body, http.StatusUnauthorized)
	s.call("POST", "/gateway/v1/register", foreign, body, http.StatusForbidden)
	broken := map[string]any{"module_yaml": manifest, "files": map[string]any{"forms/note.json": files["forms/note.json"]}}
	s.call("POST", "/gateway/v1/register", key, broken, http.StatusUnprocessableEntity)

	if s.catalogHas(user, "echo.run.v1") {
		t.Fatal("echo is in the catalog before registration")
	}
	reg := s.call("POST", "/gateway/v1/register", key, body, http.StatusOK)
	if reg["service"] != "echo" || len(reg["forms"].([]any)) != 2 {
		t.Fatalf("register: %v", reg)
	}
	if !s.catalogHas(user, "echo.run.v1") {
		t.Fatal("registered department is not in the catalog")
	}

	// Тот же ключ — токен прораба: задача пользователя уходит подразделению.
	s.call("POST", "/api/v1/deposits/demo", user, map[string]any{"amount": 100}, http.StatusOK)
	note := s.call("POST", "/api/v1/facts", user, map[string]any{"form_id": "echo.note.v1", "values": map[string]any{"name": "привет"}}, http.StatusCreated)["fact_id"].(string)
	s.call("POST", "/api/v1/facts/"+note+"/publish", user, nil, http.StatusOK)
	task := map[string]any{"form_id": "echo.run.v1", "idempotency_key": "k1", "values": map[string]any{"note": note, "text": "эхо"}}
	s.call("POST", "/api/v1/tasks", user, task, http.StatusCreated)

	// Выключили: в каталоге нет, новые задачи не создаются, прораб ничего не получает.
	s.call("PUT", "/api/v1/admin/services/echo", user, map[string]any{"enabled": false}, http.StatusForbidden)
	s.call("PUT", "/api/v1/admin/services/echo", admin, map[string]any{"enabled": false}, http.StatusNoContent)
	if s.catalogHas(user, "echo.run.v1") {
		t.Fatal("switched-off department is in the catalog")
	}
	task["idempotency_key"] = "k2"
	s.call("POST", "/api/v1/tasks", user, task, http.StatusNotFound)
	s.call("POST", "/gateway/v1/claim", key, map[string]any{"target_service": "echo", "schema_versions": []int{1}}, http.StatusNoContent)
	svcs := s.call("GET", "/api/v1/admin/services", admin, nil, http.StatusOK)["items"].([]any)
	for _, sv := range svcs {
		if m := sv.(map[string]any); m["code"] == "echo" && m["enabled"] != false {
			t.Fatalf("admin sees echo as %v", m["enabled"])
		}
	}

	// Включили — задача, ждавшая в очереди, уходит прорабу.
	s.call("PUT", "/api/v1/admin/services/echo", admin, map[string]any{"enabled": true}, http.StatusNoContent)
	claimed := s.call("POST", "/gateway/v1/claim", key, map[string]any{"target_service": "echo", "schema_versions": []int{1}}, http.StatusOK)
	if claimed["task_id"] == nil {
		t.Fatalf("claim after enabling: %v", claimed)
	}

	// Горячее обновление: новая версия паспорта — новая версия формы, без перезапуска.
	updated := strings.Replace(files["forms/run.json"].(string), `"title": "Эхо"`, `"title": "Эхо 2"`, 1)
	s.call("POST", "/gateway/v1/register", key, map[string]any{"module_yaml": manifest,
		"files": map[string]any{"forms/note.json": files["forms/note.json"], "forms/run.json": updated}}, http.StatusOK)
	tpl := s.call("GET", "/api/v1/forms/echo.run.v1", user, nil, http.StatusOK)
	if tpl["meta_ui"].(map[string]any)["title"] != "Эхо 2" || tpl["system"].(map[string]any)["schema_version"].(interface{ String() string }).String() != "2" {
		t.Fatalf("hot update: %v", tpl["system"])
	}
}
