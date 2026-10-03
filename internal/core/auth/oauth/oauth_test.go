package oauth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"offgrid/core/internal/core/auth/oauth"
	"offgrid/core/internal/core/auth/oauth/oauthtest"
)

const redirect = "http://app.test/api/v1/auth/oauth/x/callback"

func fake(t *testing.T, user oauthtest.User) (*oauthtest.Fake, string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	f := oauthtest.New("http://"+srv.Listener.Addr().String(), "", "client-1")
	f.Auto = &user
	srv.Config.Handler = f.Handler()
	srv.Start()
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func provider(t *testing.T, c oauth.Config) oauth.Provider {
	t.Helper()
	reg, err := oauth.NewRegistry([]oauth.Config{c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reg.Get(c.ID)
	if !ok {
		t.Fatalf("provider %q not registered", c.ID)
	}
	return p
}

// authorize — браузер: переход к провайдеру и query, с которым он вернул на redirect.
func authorize(t *testing.T, p oauth.Provider, a oauth.AuthParams) url.Values {
	t.Helper()
	to, err := p.AuthURL(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(to)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %d %v", resp.StatusCode, err)
	}
	return loc.Query()
}

func start() (oauth.AuthParams, string) {
	verifier := oauth.RandomToken()
	return oauth.AuthParams{State: oauth.RandomToken(), Challenge: oauth.Challenge(verifier), Nonce: oauth.RandomToken(), RedirectURI: redirect}, verifier
}

func TestOIDCLogin(t *testing.T) {
	f, base := fake(t, oauthtest.User{Subject: "g-42", Email: "Anna@Example.org", EmailVerified: true, Name: "Анна"})
	p := provider(t, oauth.Config{ID: "google", Kind: "oidc", Issuer: base, ClientID: "client-1", ClientSecret: "s", TrustEmail: true})
	ctx := context.Background()

	a, verifier := start()
	q := authorize(t, p, a)
	if q.Get("state") != a.State {
		t.Fatal("state not returned")
	}
	x := oauth.ExchangeParams{Code: q.Get("code"), Verifier: verifier, Nonce: a.Nonce, State: a.State, RedirectURI: redirect, Query: q}
	prof, err := p.Exchange(ctx, x)
	if err != nil {
		t.Fatal(err)
	}
	if prof != (oauth.Profile{Subject: "g-42", Email: "Anna@Example.org", EmailVerified: true, Name: "Анна"}) {
		t.Fatalf("profile: %+v", prof)
	}
	if _, err := p.Exchange(ctx, x); !errors.Is(err, oauth.ErrProvider) {
		t.Fatalf("code reused: %v", err)
	}

	a, verifier = start()
	q = authorize(t, p, a)
	if _, err := p.Exchange(ctx, oauth.ExchangeParams{Code: q.Get("code"), Verifier: "wrong", Nonce: a.Nonce, RedirectURI: redirect, Query: q}); !errors.Is(err, oauth.ErrProvider) {
		t.Fatalf("wrong PKCE verifier: %v", err)
	}

	a, verifier = start()
	q = authorize(t, p, a)
	if _, err := p.Exchange(ctx, oauth.ExchangeParams{Code: q.Get("code"), Verifier: verifier, Nonce: "other", RedirectURI: redirect, Query: q}); !errors.Is(err, oauth.ErrProvider) {
		t.Fatalf("nonce mismatch: %v", err)
	}

	// Подпись не тем ключом (kid тот же) — id_token отвергается.
	a, verifier = start()
	q = authorize(t, p, a)
	f.Rotate()
	if _, err := p.Exchange(ctx, oauth.ExchangeParams{Code: q.Get("code"), Verifier: verifier, Nonce: a.Nonce, RedirectURI: redirect, Query: q}); !errors.Is(err, oauth.ErrProvider) {
		t.Fatalf("bad signature: %v", err)
	}
}

func TestOIDCWrongAudience(t *testing.T) {
	f, base := fake(t, oauthtest.User{Subject: "g-1"})
	f.Audience = "another-app" // id_token выдан другому приложению
	p := provider(t, oauth.Config{ID: "google", Kind: "oidc", Issuer: base, ClientID: "client-1"})
	a, verifier := start()
	q := authorize(t, p, a)
	if _, err := p.Exchange(context.Background(), oauth.ExchangeParams{Code: q.Get("code"), Verifier: verifier, Nonce: a.Nonce, RedirectURI: redirect, Query: q}); !errors.Is(err, oauth.ErrProvider) {
		t.Fatalf("foreign audience accepted: %v", err)
	}
}

func TestVKIDLogin(t *testing.T) {
	_, base := fake(t, oauthtest.User{Subject: "123456789", Email: "ivan@vk.test", EmailVerified: true, Name: "Иван Петров"})
	p := provider(t, oauth.Config{ID: "vk", Kind: "vkid", Issuer: base + "/vk", ClientID: "client-1"})
	ctx := context.Background()

	a, verifier := start()
	q := authorize(t, p, a)
	if q.Get("device_id") == "" {
		t.Fatal("VK callback without device_id")
	}
	prof, err := p.Exchange(ctx, oauth.ExchangeParams{Code: q.Get("code"), Verifier: verifier, State: a.State, RedirectURI: redirect, Query: q})
	if err != nil {
		t.Fatal(err)
	}
	// VK не сообщает подтверждённость email — считаем неподтверждённым.
	if prof != (oauth.Profile{Subject: "123456789", Email: "ivan@vk.test", Name: "Иван Петров"}) {
		t.Fatalf("profile: %+v", prof)
	}

	a, verifier = start()
	q = authorize(t, p, a)
	q.Del("device_id")
	if _, err := p.Exchange(ctx, oauth.ExchangeParams{Code: q.Get("code"), Verifier: verifier, RedirectURI: redirect, Query: q}); !errors.Is(err, oauth.ErrProvider) {
		t.Fatalf("no device_id: %v", err)
	}
}

func TestRegistry(t *testing.T) {
	presets := oauth.Presets("", "", "", "vk-app", "", "")
	extra, err := oauth.ParseExtra(`[{"id":"yandex","name":"Яндекс","kind":"oidc","issuer":"https://login.yandex.ru","client_id":"y"}]`)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := oauth.NewRegistry(append(presets, extra...), nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range reg.List() {
		ids = append(ids, p.ID())
	}
	if len(ids) != 2 || ids[0] != "vk" || ids[1] != "yandex" { // google без ключа выключен
		t.Fatalf("providers: %v", ids)
	}
	for name, cfg := range map[string][]oauth.Config{
		"unknown kind": {{ID: "x", Kind: "saml", ClientID: "c"}},
		"no issuer":    {{ID: "x", Kind: "oidc", ClientID: "c"}},
		"bad id":       {{ID: "a/b", Kind: "vkid", ClientID: "c"}},
		"duplicate":    {{ID: "x", Kind: "vkid", ClientID: "c"}, {ID: "x", Kind: "vkid", ClientID: "d"}},
	} {
		if _, err := oauth.NewRegistry(cfg, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := oauth.ParseExtra("{not json"); err == nil {
		t.Error("bad JSON accepted")
	}
	var nilReg *oauth.Registry
	if _, ok := nilReg.Get("google"); ok || nilReg.List() != nil {
		t.Error("nil registry must be empty")
	}
}
