package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"offgrid/core/internal/core/auth/oauth/oauthtest"
)

// link — привязка входа из кабинета: адрес провайдера JSON-ом → провайдер → callback → страница кабинета.
func (s *stack) link(token, provider string, as oauthtest.User) string {
	s.t.Helper()
	s.idp.Auto = &as
	out := s.call("POST", "/api/v1/me/identities/"+provider+"/link", token, map[string]any{"return_to": "/cabinet"}, http.StatusOK)
	to := s.browse(out["url"].(string))
	return s.browse(to)
}

func TestCabinetOverHTTP(t *testing.T) {
	s := newStack(t)
	if f := s.call("GET", "/api/v1/features", "", nil, http.StatusOK); f["demo_deposit"] != true {
		t.Fatalf("features: %v", f)
	}

	// Вошёл через VK: ни email, ни пароля — единственный вход не отвязать.
	_, vk := s.exchange(s.oauthLogin("vk", "/", oauthtest.User{Subject: "501", Name: "Иван Петров"}), http.StatusOK)
	me := s.call("GET", "/api/v1/me", vk, nil, http.StatusOK)
	if me["has_password"] != false || me["email"] != "" {
		t.Fatalf("vk me: %v", me)
	}
	ids := s.call("GET", "/api/v1/me/identities", vk, nil, http.StatusOK)["items"].([]any)
	if len(ids) != 1 || ids[0].(map[string]any)["provider"] != "vk" {
		t.Fatalf("identities: %v", ids)
	}
	s.call("DELETE", "/api/v1/me/identities/vk/501", vk, nil, http.StatusConflict)
	s.call("POST", "/api/v1/me/password", vk, map[string]any{"new_password": "longpassword"}, http.StatusConflict) // нет email

	// Привязал Google: второй способ входа и подтверждённый email.
	if page := s.link(vk, "google", oauthtest.User{Subject: "g-501", Email: "Ivan@Example.org", EmailVerified: true}); page != "/cabinet#oauth_linked=google" {
		t.Fatalf("link: %s", page)
	}
	if me := s.call("GET", "/api/v1/me", vk, nil, http.StatusOK); me["email"] != "ivan@example.org" {
		t.Fatalf("email after linking Google: %v", me)
	}
	// Теперь вход через Google попадает в того же пользователя.
	vkID := me["user_id"]
	if id, _ := s.exchange(s.oauthLogin("google", "/", oauthtest.User{Subject: "g-501", Email: "ivan@example.org", EmailVerified: true}), http.StatusOK); id != vkID {
		t.Fatal("linked Google login went to another user")
	}
	// VK больше не последний вход — отвязывается; повторно — 404.
	s.call("DELETE", "/api/v1/me/identities/vk/501", vk, nil, http.StatusNoContent)
	s.call("DELETE", "/api/v1/me/identities/vk/501", vk, nil, http.StatusNotFound)

	// Чужой внешний аккаунт не перепривязывается.
	other := s.register("other@example.org")
	s.oauthLogin("google", "/", oauthtest.User{Subject: "g-other", Email: "zzz@example.org", EmailVerified: true})
	if page := s.link(other, "google", oauthtest.User{Subject: "g-501"}); page != "/cabinet#oauth_link_error=taken" {
		t.Fatalf("taken: %s", page)
	}
	// Отказ при привязке — ошибка привязки, а не входа.
	out := s.call("POST", "/api/v1/me/identities/google/link", other, nil, http.StatusOK)
	u, _ := url.Parse(out["url"].(string))
	if page := s.browse("/api/v1/auth/oauth/google/callback?error=access_denied&state=" + url.QueryEscape(u.Query().Get("state"))); page != "/#oauth_link_error=denied" {
		t.Fatalf("denied link: %s", page)
	}
	s.call("POST", "/api/v1/me/identities/nope/link", other, nil, http.StatusNotFound)
	s.call("POST", "/api/v1/me/identities/google/link", "", nil, http.StatusUnauthorized)

	// Пароль: задать без текущего (его не было), затем менять только с верным текущим.
	s.call("POST", "/api/v1/me/password", vk, map[string]any{"new_password": "short"}, http.StatusUnprocessableEntity)
	s.call("POST", "/api/v1/me/password", vk, map[string]any{"new_password": "first-password"}, http.StatusNoContent)
	s.call("POST", "/api/v1/auth/login", "", map[string]any{"email": "ivan@example.org", "password": "first-password"}, http.StatusOK)
	s.call("POST", "/api/v1/me/password", vk, map[string]any{"current_password": "wrong-password", "new_password": "second-password"}, http.StatusForbidden)
	s.call("POST", "/api/v1/me/password", vk, map[string]any{"current_password": "first-password", "new_password": "second-password"}, http.StatusNoContent)
	s.call("POST", "/api/v1/auth/login", "", map[string]any{"email": "ivan@example.org", "password": "first-password"}, http.StatusUnauthorized)

	// Имя.
	if p := s.call("PATCH", "/api/v1/me", vk, map[string]any{"display_name": "  Ваня  "}, http.StatusOK); p["display_name"] != "Ваня" {
		t.Fatalf("name: %v", p)
	}
	s.call("PATCH", "/api/v1/me", vk, map[string]any{"display_name": strings.Repeat("я", 101)}, http.StatusUnprocessableEntity)

	// Список фактов для кабинета: поля опубликованной версии, без удалённых.
	hero := s.call("POST", "/api/v1/facts", vk, map[string]any{"form_id": "taleweaver.character.v1", "values": map[string]any{"name": "Лис"}}, http.StatusCreated)
	heroID := hero["fact_id"].(string)
	gone := s.call("POST", "/api/v1/facts", vk, map[string]any{"form_id": "taleweaver.character.v1", "values": map[string]any{"name": "Волк"}}, http.StatusCreated)
	s.call("POST", "/api/v1/facts/"+heroID+"/publish", vk, nil, http.StatusOK)
	s.call("DELETE", "/api/v1/facts/"+gone["fact_id"].(string), vk, nil, http.StatusNoContent)
	items := s.call("GET", "/api/v1/facts?kind=taleweaver.character.v1&fields=name,nope&live=true", vk, nil, http.StatusOK)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("live heroes: %v", items)
	}
	h := items[0].(map[string]any)
	if h["fact_id"] != heroID || h["status"] != "published" || h["created_at"] == nil ||
		h["summary"].(map[string]any)["name"] != "Лис" || len(h["summary"].(map[string]any)) != 1 {
		t.Fatalf("hero card: %v", h)
	}
	if all := s.call("GET", "/api/v1/facts?kind=taleweaver.character.v1", vk, nil, http.StatusOK)["items"].([]any); len(all) != 2 {
		t.Fatalf("all heroes (with deleted): %d", len(all))
	}

	// История кошелька знает источник пополнения.
	s.call("POST", "/api/v1/deposits/demo", vk, map[string]any{"amount": 100}, http.StatusOK)
	tx := s.call("GET", "/api/v1/wallets/transactions", vk, nil, http.StatusOK)["items"].([]any)
	if len(tx) != 1 || tx[0].(map[string]any)["source"] != "demo" {
		t.Fatalf("transactions: %v", tx)
	}
}
