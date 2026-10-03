// Package oauth — вход через внешние аккаунты (ADR-58). Провайдеров может быть сколько угодно:
// любой OpenID Connect (Google, Microsoft, Яндекс, Сбер ID, Keycloak…) подключается настройкой,
// у провайдеров со своим протоколом (VK ID, позже Apple) — свой адаптер. Поток — Authorization
// Code + PKCE (S256) + одноразовый state; OIDC — ещё и nonce с проверкой подписи id_token.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Profile — что провайдер сообщил о человеке. Subject — неизменный id у провайдера.
type Profile struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// AuthParams — параметры перехода к провайдеру.
type AuthParams struct {
	State, Challenge, Nonce, RedirectURI string
}

// ExchangeParams — возврат от провайдера: код, секреты начатого входа и весь query колбэка
// (у VK ID там ещё device_id).
type ExchangeParams struct {
	Code, Verifier, Nonce, State, RedirectURI string
	Query                                     url.Values
}

// Provider — один способ входа.
type Provider interface {
	ID() string
	Name() string
	// TrustEmail — можно ли по подтверждённому этим провайдером email привязать вход к
	// существующему аккаунту с тем же email.
	TrustEmail() bool
	AuthURL(ctx context.Context, p AuthParams) (string, error)
	Exchange(ctx context.Context, p ExchangeParams) (Profile, error)
}

// ErrProvider — провайдер ответил ошибкой или неверными данными (подпись, аудитория, nonce…).
var ErrProvider = errors.New("oauth provider error")

func providerErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrProvider, fmt.Sprintf(format, a...))
}

// Config — провайдер из настроек ядра.
type Config struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`   // oidc | vkid
	Issuer       string   `json:"issuer"` // OIDC: issuer (discovery); VK ID: базовый адрес
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	TrustEmail   bool     `json:"trust_email"`
}

// Registry — включённые провайдеры.
type Registry struct {
	byID map[string]Provider
}

// Get — провайдер по id.
func (r *Registry) Get(id string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.byID[id]
	return p, ok
}

// List — провайдеры по id (порядок стабильный: кнопки на экране не прыгают).
func (r *Registry) List() []Provider {
	if r == nil {
		return nil
	}
	out := make([]Provider, 0, len(r.byID))
	for _, p := range r.byID {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// NewRegistry собирает провайдеров; без client_id провайдер выключен (пропускается).
func NewRegistry(configs []Config, client *http.Client) (*Registry, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	r := &Registry{byID: map[string]Provider{}}
	for _, c := range configs {
		if c.ClientID == "" {
			continue
		}
		if c.ID == "" || strings.ContainsAny(c.ID, "/?#") {
			return nil, fmt.Errorf("oauth provider: bad id %q", c.ID)
		}
		if _, dup := r.byID[c.ID]; dup {
			return nil, fmt.Errorf("oauth provider %q is configured twice", c.ID)
		}
		if c.Name == "" {
			c.Name = c.ID
		}
		switch c.Kind {
		case "oidc":
			if c.Issuer == "" {
				return nil, fmt.Errorf("oauth provider %q: issuer is required", c.ID)
			}
			r.byID[c.ID] = newOIDC(c, client)
		case "vkid":
			r.byID[c.ID] = newVKID(c, client)
		default:
			return nil, fmt.Errorf("oauth provider %q: unknown kind %q (oidc | vkid)", c.ID, c.Kind)
		}
	}
	return r, nil
}

// Presets — готовые провайдеры: включаются одними ключами (client id и secret).
func Presets(googleID, googleSecret, googleIssuer, vkID, vkSecret, vkBase string) []Config {
	if googleIssuer == "" {
		googleIssuer = "https://accounts.google.com"
	}
	if vkBase == "" {
		vkBase = "https://id.vk.ru"
	}
	return []Config{
		// Google подтверждает email (email_verified) — по нему можно привязаться к аккаунту с паролем.
		{ID: "google", Name: "Google", Kind: "oidc", Issuer: googleIssuer, ClientID: googleID, ClientSecret: googleSecret, TrustEmail: true},
		// VK ID может отдать email, но подтверждённость не сообщает — без автопривязки.
		{ID: "vk", Name: "VK ID", Kind: "vkid", Issuer: vkBase, ClientID: vkID, ClientSecret: vkSecret},
	}
}

// ParseExtra — дополнительные провайдеры из CORE_OAUTH_PROVIDERS (JSON-массив Config).
func ParseExtra(raw string) ([]Config, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []Config
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("CORE_OAUTH_PROVIDERS: %w", err)
	}
	return out, nil
}

// RandomToken — случайная строка base64url (state, PKCE verifier, nonce, одноразовые коды).
func RandomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // криптослучайность недоступна — продолжать нельзя
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Challenge — PKCE S256: base64url(sha256(verifier)).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// postForm — POST application/x-www-form-urlencoded, ответ JSON в out; ошибка провайдера — ErrProvider.
func postForm(ctx context.Context, client *http.Client, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return do(client, req, out)
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	return do(client, req, out)
}

func do(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return providerErr("%s: %v", req.URL.Host, err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&raw); err != nil {
		return providerErr("%s %s: HTTP %d, not JSON", req.Method, req.URL.Path, resp.StatusCode)
	}
	var e struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &e)
	if resp.StatusCode >= 300 || e.Error != "" {
		return providerErr("%s %s: HTTP %d %s %s", req.Method, req.URL.Path, resp.StatusCode, e.Error, e.Description)
	}
	return json.Unmarshal(raw, out)
}
