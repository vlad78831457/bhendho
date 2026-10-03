package oauth

// OpenID Connect: адреса — из discovery (issuer/.well-known/openid-configuration), личность —
// из id_token: подпись по ключам JWKS, издатель, аудитория (наш client_id), срок и nonce.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type oidcProvider struct {
	cfg    Config
	client *http.Client

	mu      sync.Mutex
	meta    *discovery
	keys    map[string]any
	fetched time.Time
}

func newOIDC(c Config, client *http.Client) *oidcProvider {
	if len(c.Scopes) == 0 {
		c.Scopes = []string{"openid", "email", "profile"}
	}
	return &oidcProvider{cfg: c, client: client}
}

func (p *oidcProvider) ID() string       { return p.cfg.ID }
func (p *oidcProvider) Name() string     { return p.cfg.Name }
func (p *oidcProvider) TrustEmail() bool { return p.cfg.TrustEmail }

// discover — адреса провайдера; кэшируются на процесс.
func (p *oidcProvider) discover(ctx context.Context) (*discovery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.meta != nil {
		return p.meta, nil
	}
	var d discovery
	if err := getJSON(ctx, p.client, strings.TrimRight(p.cfg.Issuer, "/")+"/.well-known/openid-configuration", &d); err != nil {
		return nil, err
	}
	if d.Issuer == "" || d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return nil, providerErr("%s: incomplete discovery document", p.cfg.ID)
	}
	p.meta = &d
	return p.meta, nil
}

func (p *oidcProvider) AuthURL(ctx context.Context, a AuthParams) (string, error) {
	d, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {a.RedirectURI},
		"scope":                 {strings.Join(p.cfg.Scopes, " ")},
		"state":                 {a.State},
		"nonce":                 {a.Nonce},
		"code_challenge":        {a.Challenge},
		"code_challenge_method": {"S256"},
	}
	return d.AuthorizationEndpoint + sep(d.AuthorizationEndpoint) + q.Encode(), nil
}

func sep(u string) string {
	if strings.Contains(u, "?") {
		return "&"
	}
	return "?"
}

func (p *oidcProvider) Exchange(ctx context.Context, x ExchangeParams) (Profile, error) {
	d, err := p.discover(ctx)
	if err != nil {
		return Profile{}, err
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {x.Code},
		"redirect_uri":  {x.RedirectURI},
		"client_id":     {p.cfg.ClientID},
		"code_verifier": {x.Verifier},
	}
	if p.cfg.ClientSecret != "" {
		form.Set("client_secret", p.cfg.ClientSecret)
	}
	if err := postForm(ctx, p.client, d.TokenEndpoint, form, &tok); err != nil {
		return Profile{}, err
	}
	if tok.IDToken == "" {
		return Profile{}, providerErr("%s: no id_token in the token response", p.cfg.ID)
	}
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(tok.IDToken, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return p.key(ctx, d, kid)
	},
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384"}),
		jwt.WithIssuer(d.Issuer),
		jwt.WithAudience(p.cfg.ClientID),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(time.Minute),
	)
	if err != nil {
		return Profile{}, providerErr("%s: id_token: %v", p.cfg.ID, err)
	}
	if nonce, _ := claims["nonce"].(string); nonce == "" || nonce != x.Nonce {
		return Profile{}, providerErr("%s: id_token nonce mismatch", p.cfg.ID)
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return Profile{}, providerErr("%s: id_token without sub", p.cfg.ID)
	}
	prof := Profile{Subject: sub}
	prof.Email, _ = claims["email"].(string)
	prof.Name, _ = claims["name"].(string)
	switch v := claims["email_verified"].(type) { // бывает и bool, и строкой
	case bool:
		prof.EmailVerified = v
	case string:
		prof.EmailVerified = v == "true"
	}
	return prof, nil
}

// key — открытый ключ подписи по kid; незнакомый kid — один раз перечитать JWKS (ротация ключей).
func (p *oidcProvider) key(ctx context.Context, d *discovery, kid string) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k, ok := p.keys[kid]; ok {
		return k, nil
	}
	if time.Since(p.fetched) < 10*time.Second && p.keys != nil {
		return nil, fmt.Errorf("unknown key id %q", kid)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := getJSON(ctx, p.client, d.JWKSURI, &set); err != nil {
		return nil, err
	}
	p.keys, p.fetched = map[string]any{}, time.Now()
	for _, k := range set.Keys {
		if pub, err := k.public(); err == nil {
			p.keys[k.Kid] = pub
		}
	}
	if k, ok := p.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func b64int(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

func (k jwk) public() (any, error) {
	switch k.Kty {
	case "RSA":
		n, err := b64int(k.N)
		if err != nil {
			return nil, err
		}
		e, err := b64int(k.E)
		if err != nil {
			return nil, err
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		curve := map[string]elliptic.Curve{"P-256": elliptic.P256(), "P-384": elliptic.P384()}[k.Crv]
		if curve == nil {
			return nil, fmt.Errorf("unsupported curve %q", k.Crv)
		}
		x, err := b64int(k.X)
		if err != nil {
			return nil, err
		}
		y, err := b64int(k.Y)
		if err != nil {
			return nil, err
		}
		return &ecdsa.PublicKey{Curve: curve, X: x, Y: y}, nil
	}
	return nil, fmt.Errorf("unsupported key type %q", k.Kty)
}
