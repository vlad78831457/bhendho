package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"offgrid/core/internal/core/auth/oauth/oauthtest"
)

// browse — браузер без автоперехода: запрос и адрес, куда отправили (Location).
func (s *stack) browse(to string) string {
	s.t.Helper()
	if strings.HasPrefix(to, "/") {
		to = s.srv.URL + to
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(to)
	if err != nil {
		s.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		s.t.Fatalf("GET %s: status %d, want 302", to, resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// oauthLogin — полный вход через провайдера: start → провайдер → callback → страница приложения.
// Возвращает адрес страницы приложения (с #oauth=… или #oauth_error=…).
func (s *stack) oauthLogin(provider, returnTo string, as oauthtest.User) string {
	s.t.Helper()
	s.idp.Auto = &as
	to := s.browse("/api/v1/auth/oauth/" + provider + "/start?return_to=" + url.QueryEscape(returnTo))
	to = s.browse(to) // провайдер «впускает» и возвращает на callback
	if !strings.HasPrefix(to, s.srv.URL+"/api/v1/auth/oauth/"+provider+"/callback?") {
		s.t.Fatalf("provider returned to %s", to)
	}
	return s.browse(to)
}

// exchange — одноразовый код из #oauth=… → user_id и access token.
func (s *stack) exchange(page string, wantStatus int) (string, string) {
	s.t.Helper()
	_, frag, _ := strings.Cut(page, "#oauth=")
	code, _ := url.QueryUnescape(frag)
	out := s.call("POST", "/api/v1/auth/oauth/exchange", "", map[string]any{"code": code}, wantStatus)
	if wantStatus != http.StatusOK {
		return "", ""
	}
	return out["user_id"].(string), out["tokens"].(map[string]any)["access_token"].(string)
}

func TestOAuthOverHTTP(t *testing.T) {
	s := newStack(t)

	out := s.call("GET", "/api/v1/auth/providers", "", nil, http.StatusOK)
	provs := out["providers"].([]any)
	if len(provs) != 2 || provs[0].(map[string]any)["id"] != "google" || provs[1].(map[string]any)["id"] != "vk" {
		t.Fatalf("providers: %v", provs)
	}
	s.call("GET", "/api/v1/auth/oauth/nope/start", "", nil, http.StatusNotFound)

	// Google подтверждает email — вход привязывается к аккаунту с паролем на тот же адрес.
	anna := s.register("anna@example.org")
	annaID := s.call("GET", "/api/v1/me", anna, nil, http.StatusOK)["user_id"]
	page := s.oauthLogin("google", "/tale?x=1", oauthtest.User{Subject: "g-1", Email: "Anna@Example.org", EmailVerified: true, Name: "Анна"})
	if !strings.HasPrefix(page, "/tale?x=1#oauth=") {
		t.Fatalf("back to %s", page)
	}
	id, token := s.exchange(page, http.StatusOK)
	if id != annaID {
		t.Fatalf("google login not linked to the password account: %v vs %v", id, annaID)
	}
	if me := s.call("GET", "/api/v1/me", token, nil, http.StatusOK); me["email"] != "anna@example.org" {
		t.Fatalf("me: %v", me)
	}
	s.exchange(page, http.StatusUnauthorized) // код входа одноразовый

	// VK email не подтверждает — тот же адрес не даёт войти в чужой аккаунт: новый пользователь без email.
	vkUser := oauthtest.User{Subject: "777", Email: "anna@example.org", Name: "Иван Петров"}
	vkID, vkToken := s.exchange(s.oauthLogin("vk", "/", vkUser), http.StatusOK)
	if vkID == annaID {
		t.Fatal("unverified VK email took over the password account")
	}
	if me := s.call("GET", "/api/v1/me", vkToken, nil, http.StatusOK); me["email"] != "" || me["display_name"] != "Иван Петров" {
		t.Fatalf("vk me: %v", me)
	}
	if again, _ := s.exchange(s.oauthLogin("vk", "/", vkUser), http.StatusOK); again != vkID {
		t.Fatal("second VK login created another user")
	}
	// Пароль у такого пользователя нет — вход по паролю невозможен.
	s.call("POST", "/api/v1/auth/login", "", map[string]any{"email": "", "password": "longpassword"}, http.StatusUnauthorized)

	// Новый Google-аккаунт с подтверждённым свободным email — новый пользователь с этим email.
	newID, newToken := s.exchange(s.oauthLogin("google", "/", oauthtest.User{Subject: "g-2", Email: "boris@example.org", EmailVerified: true}), http.StatusOK)
	if me := s.call("GET", "/api/v1/me", newToken, nil, http.StatusOK); me["email"] != "boris@example.org" || me["user_id"] != newID {
		t.Fatalf("new google user: %v", me)
	}
	// После этого на тот же адрес не зарегистрироваться паролем.
	s.call("POST", "/api/v1/auth/register", "", map[string]any{"email": "boris@example.org", "password": "longpassword"}, http.StatusConflict)

	// Возврат только на свой сайт.
	if page := s.oauthLogin("google", "https://evil.test/steal", oauthtest.User{Subject: "g-1", Email: "anna@example.org", EmailVerified: true}); !strings.HasPrefix(page, "/#oauth=") {
		t.Fatalf("open redirect: %s", page)
	}

	// Отказ на стороне провайдера и чужой/старый state.
	to := s.browse("/api/v1/auth/oauth/google/start?return_to=/tale")
	u, _ := url.Parse(to)
	state := u.Query().Get("state")
	if page := s.browse("/api/v1/auth/oauth/google/callback?error=access_denied&state=" + url.QueryEscape(state)); page != "/tale#oauth_error=denied" {
		t.Fatalf("denied: %s", page)
	}
	if page := s.browse("/api/v1/auth/oauth/google/callback?code=x&state=" + url.QueryEscape(state)); page != "/#oauth_error=expired" {
		t.Fatalf("reused state: %s", page)
	}
	// state одного провайдера не подходит другому.
	u, _ = url.Parse(s.browse("/api/v1/auth/oauth/google/start"))
	if page := s.browse("/api/v1/auth/oauth/vk/callback?code=x&state=" + url.QueryEscape(u.Query().Get("state"))); page != "/#oauth_error=expired" {
		t.Fatalf("cross-provider state: %s", page)
	}
}
