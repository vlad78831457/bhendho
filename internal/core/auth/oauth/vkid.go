package oauth

// VK ID (id.vk.ru): OAuth 2.1 + PKCE со своими особенностями — в колбэк приходит device_id,
// который нужно передать при обмене кода; секрет приложения при обмене не нужен (его заменяет
// PKCE); личность — из POST oauth2/user_info по access_token. Подтверждённость email VK не
// сообщает, поэтому EmailVerified = false (без автопривязки к аккаунту с тем же email).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

type vkidProvider struct {
	cfg    Config
	client *http.Client
}

func newVKID(c Config, client *http.Client) *vkidProvider {
	c.Issuer = strings.TrimRight(c.Issuer, "/")
	if len(c.Scopes) == 0 {
		c.Scopes = []string{"email"}
	}
	return &vkidProvider{cfg: c, client: client}
}

func (p *vkidProvider) ID() string       { return p.cfg.ID }
func (p *vkidProvider) Name() string     { return p.cfg.Name }
func (p *vkidProvider) TrustEmail() bool { return p.cfg.TrustEmail }

func (p *vkidProvider) AuthURL(_ context.Context, a AuthParams) (string, error) {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {a.RedirectURI},
		"scope":                 {strings.Join(p.cfg.Scopes, " ")},
		"state":                 {a.State},
		"code_challenge":        {a.Challenge},
		"code_challenge_method": {"S256"},
	}
	return p.cfg.Issuer + "/authorize?" + q.Encode(), nil
}

func (p *vkidProvider) Exchange(ctx context.Context, x ExchangeParams) (Profile, error) {
	deviceID := x.Query.Get("device_id")
	if deviceID == "" {
		return Profile{}, providerErr("%s: callback without device_id", p.cfg.ID)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := postForm(ctx, p.client, p.cfg.Issuer+"/oauth2/auth", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {x.Code},
		"code_verifier": {x.Verifier},
		"client_id":     {p.cfg.ClientID},
		"device_id":     {deviceID},
		"redirect_uri":  {x.RedirectURI},
		"state":         {x.State},
	}, &tok); err != nil {
		return Profile{}, err
	}
	if tok.AccessToken == "" {
		return Profile{}, providerErr("%s: no access_token", p.cfg.ID)
	}
	var info struct {
		User struct {
			UserID    json.Number `json:"user_id"`
			Email     string      `json:"email"`
			FirstName string      `json:"first_name"`
			LastName  string      `json:"last_name"`
		} `json:"user"`
	}
	if err := postForm(ctx, p.client, p.cfg.Issuer+"/oauth2/user_info", url.Values{
		"client_id":    {p.cfg.ClientID},
		"access_token": {tok.AccessToken},
	}, &info); err != nil {
		return Profile{}, err
	}
	if info.User.UserID == "" {
		return Profile{}, providerErr("%s: user_info without user_id", p.cfg.ID)
	}
	return Profile{
		Subject: info.User.UserID.String(),
		Email:   info.User.Email,
		Name:    strings.TrimSpace(info.User.FirstName + " " + info.User.LastName),
	}, nil
}
