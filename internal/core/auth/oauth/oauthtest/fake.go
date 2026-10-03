// Package oauthtest — поддельный провайдер входа для тестов и локального стенда (ADR-58):
// OpenID Connect (как Google) в корне и VK ID под /vk. Проверяет то же, что настоящие:
// client_id, redirect_uri, PKCE; id_token подписан RS256 и публикуется в JWKS.
// Настоящих аккаунтов нет: на странице входа человек сам вписывает, кем войти.
package oauthtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// User — кем войти.
type User struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

type grant struct {
	user                          User
	clientID, redirect, challenge string
	nonce                         string
	vk                            bool
	deviceID                      string
}

// Fake — поддельный провайдер. Issuer — адрес для ядра (discovery, токены), PublicURL — адрес
// для браузера (страница входа); в тестах совпадают.
type Fake struct {
	Issuer, PublicURL string
	ClientID          string
	// Auto — не показывать страницу входа, сразу вернуть с этим пользователем (для тестов на Go).
	Auto *User
	// Audience — подменить aud в id_token (проверка, что ядро не примет токен чужого приложения).
	Audience string

	key           *rsa.PrivateKey
	mu            sync.Mutex
	codes, tokens map[string]grant
}

// New — провайдер с новым ключом подписи.
func New(issuer, publicURL, clientID string) *Fake {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	if publicURL == "" {
		publicURL = issuer
	}
	return &Fake{Issuer: strings.TrimRight(issuer, "/"), PublicURL: strings.TrimRight(publicURL, "/"), ClientID: clientID,
		key: k, codes: map[string]grant{}, tokens: map[string]grant{}}
}

// Rotate — новый ключ подписи под тем же kid: подписи старым ключом больше не сходятся.
func (f *Fake) Rotate() {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	f.mu.Lock()
	f.key = k
	f.mu.Unlock()
}

// Handler — все адреса провайдера.
func (f *Fake) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"issuer":                 f.Issuer,
			"authorization_endpoint": f.PublicURL + "/authorize",
			"token_endpoint":         f.Issuer + "/token",
			"jwks_uri":               f.Issuer + "/jwks",
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		pub := f.key.PublicKey
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) { f.authorize(w, r, false) })
	mux.HandleFunc("/vk/authorize", func(w http.ResponseWriter, r *http.Request) { f.authorize(w, r, true) })
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("POST /vk/oauth2/auth", f.vkToken)
	mux.HandleFunc("POST /vk/oauth2/user_info", f.vkUserInfo)
	return mux
}

var page = template.Must(template.New("p").Parse(`<!doctype html><html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>Тестовый вход</title>
<style>body{font:16px system-ui;max-width:420px;margin:40px auto;padding:0 16px}label{display:block;margin:10px 0}
input[type=text],input[type=email]{width:100%;padding:6px}button{padding:8px 14px;margin-right:8px}</style></head><body>
<h1>Тестовый вход: {{.Provider}}</h1>
<p>Это поддельный провайдер для разработки — пароль не нужен, настоящих аккаунтов нет.</p>
<form method="post">
{{range $k, $v := .Hidden}}<input type="hidden" name="{{$k}}" value="{{$v}}">{{end}}
<label>Идентификатор у провайдера <input type="text" name="sub" value="user-1" required></label>
<label>Email <input type="email" name="email" value="tester@example.org"></label>
<label><input type="checkbox" name="email_verified" value="true" checked> email подтверждён</label>
<label>Имя <input type="text" name="name" value="Тестовый Пользователь"></label>
<button name="action" value="allow">Войти</button><button name="action" value="deny" formnovalidate>Отказаться</button>
</form></body></html>`))

func (f *Fake) authorize(w http.ResponseWriter, r *http.Request, vk bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := r.Form
	redirect, state := q.Get("redirect_uri"), q.Get("state")
	if q.Get("client_id") != f.ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || state == "" || !strings.HasPrefix(redirect, "http") {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	var user User
	switch {
	case r.Method == http.MethodGet && f.Auto != nil:
		user = *f.Auto
	case r.Method == http.MethodGet:
		hidden := map[string]string{}
		for _, k := range []string{"client_id", "response_type", "redirect_uri", "state", "nonce", "code_challenge", "code_challenge_method", "scope"} {
			hidden[k] = q.Get(k)
		}
		name := "OpenID Connect"
		if vk {
			name = "VK ID"
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, map[string]any{"Provider": name, "Hidden": hidden})
		return
	case q.Get("action") == "deny":
		back(w, r, redirect, url.Values{"error": {"access_denied"}, "state": {state}})
		return
	default:
		user = User{Subject: q.Get("sub"), Email: q.Get("email"), EmailVerified: q.Get("email_verified") == "true", Name: q.Get("name")}
	}
	code := token()
	g := grant{user: user, clientID: q.Get("client_id"), redirect: redirect, challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), vk: vk}
	params := url.Values{"code": {code}, "state": {state}}
	if vk {
		g.deviceID = token()
		params.Set("device_id", g.deviceID)
		params.Set("type", "code_v2")
	}
	f.mu.Lock()
	f.codes[code] = g
	f.mu.Unlock()
	back(w, r, redirect, params)
}

func back(w http.ResponseWriter, r *http.Request, redirect string, params url.Values) {
	sep := "?"
	if strings.Contains(redirect, "?") {
		sep = "&"
	}
	http.Redirect(w, r, redirect+sep+params.Encode(), http.StatusFound)
}

// take — код меняется один раз и только с верным client_id, redirect_uri и PKCE verifier.
func (f *Fake) take(r *http.Request, vk bool) (grant, bool) {
	f.mu.Lock()
	g, ok := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	return g, ok && g.vk == vk && g.clientID == r.PostForm.Get("client_id") && g.redirect == r.PostForm.Get("redirect_uri") &&
		g.challenge == b64(sum[:])
}

func (f *Fake) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	g, ok := f.take(r, false)
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	now := time.Now()
	aud := f.ClientID
	if f.Audience != "" {
		aud = f.Audience
	}
	claims := jwt.MapClaims{
		"iss": f.Issuer, "aud": aud, "sub": g.user.Subject, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
		"nonce": g.nonce, "email": g.user.Email, "email_verified": g.user.EmailVerified, "name": g.user.Name,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = "k1"
	f.mu.Lock()
	idToken, err := t.SignedString(f.key)
	f.mu.Unlock()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": token(), "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
}

func (f *Fake) vkToken(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	g, ok := f.take(r, true)
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("device_id") != g.deviceID {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	access := token()
	f.mu.Lock()
	f.tokens[access] = g
	f.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600, "user_id": g.user.Subject})
}

func (f *Fake) vkUserInfo(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	g, ok := f.tokens[r.PostForm.Get("access_token")]
	f.mu.Unlock()
	if !ok || r.PostForm.Get("client_id") != f.ClientID {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	first, last, _ := strings.Cut(g.user.Name, " ")
	// Как у VK: user_id — число, если идентификатор числовой.
	var uid any = g.user.Subject
	if n, ok := new(big.Int).SetString(g.user.Subject, 10); ok {
		uid = json.Number(n.String())
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{
		"user_id": uid, "email": g.user.Email, "first_name": first, "last_name": last,
	}})
}

func token() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return b64(b[:])
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
