package httpapi

// Вход через внешние аккаунты (ADR-58). Браузер уходит на start, провайдер возвращает на callback,
// оттуда — на страницу приложения с одноразовым кодом в #oauth=…; фронт меняет код на токены
// через POST exchange. Токены в адресную строку (историю, логи прокси) не попадают.

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/google/uuid"

	"offgrid/core/internal/core/auth"
)

// WithPublicURL — адрес сайта снаружи (CORE_PUBLIC_URL); из него адрес возврата от провайдера.
// Пусто — адрес берётся из запроса (Host и X-Forwarded-Proto от фронтового прокси).
func (s *Server) WithPublicURL(u string) *Server {
	s.publicURL = u
	return s
}

func (s *Server) callbackURL(r *http.Request, provider string) string {
	base := s.publicURL
	if base == "" {
		proto := r.Header.Get("X-Forwarded-Proto")
		if proto != "https" {
			proto = "http"
		}
		base = proto + "://" + r.Host
	}
	return base + "/api/v1/auth/oauth/" + url.PathEscape(provider) + "/callback"
}

func (s *Server) authProviders(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]string{}
	for _, p := range s.auth.OAuthProviders() {
		out = append(out, map[string]string{"id": p.ID(), "name": p.Name()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.auth.OAuthProvider(r.PathValue("provider"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown_provider", "login provider is not configured")
		return
	}
	to, err := s.auth.OAuthStart(r.Context(), p, s.callbackURL(r, p.ID()), r.URL.Query().Get("return_to"))
	if err != nil {
		s.log.Warn("oauth start failed", "provider", p.ID(), "err", err)
		writeError(w, http.StatusBadGateway, "provider_unavailable", "login provider is unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, http.StatusFound)
}

// oauthCallback всегда уводит обратно в приложение: с #oauth=<код> или #oauth_error=<причина>.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	back := func(to, fragment string) { http.Redirect(w, r, auth.SafeReturn(to)+"#"+fragment, http.StatusFound) }
	p, ok := s.auth.OAuthProvider(r.PathValue("provider"))
	if !ok {
		back("/", "oauth_error=unknown_provider")
		return
	}
	res, err := s.auth.OAuthCallback(r.Context(), p, s.callbackURL(r, p.ID()), r.URL.Query())
	// Привязка из кабинета: #oauth_linked=<провайдер> или #oauth_link_error=<причина>.
	errKey := "oauth_error="
	if res.Linked {
		errKey = "oauth_link_error="
	}
	switch {
	case err == nil && res.Linked:
		back(res.ReturnTo, "oauth_linked="+url.QueryEscape(p.ID()))
	case err == nil:
		back(res.ReturnTo, "oauth="+url.QueryEscape(res.Code))
	case errors.Is(err, auth.ErrOAuthState):
		back(res.ReturnTo, errKey+"expired")
	case errors.Is(err, auth.ErrOAuthDenied):
		back(res.ReturnTo, errKey+"denied")
	case errors.Is(err, auth.ErrIdentityTaken):
		back(res.ReturnTo, errKey+"taken")
	default:
		s.log.Warn("oauth callback failed", "provider", p.ID(), "err", err)
		back(res.ReturnTo, errKey+"provider")
	}
}

// oauthLink — начать привязку ещё одного входа (из кабинета). Браузер не может передать
// токен при переходе, поэтому адрес провайдера выдаётся JSON-ом, а фронт уходит по нему сам.
func (s *Server) oauthLink(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	p, ok := s.auth.OAuthProvider(r.PathValue("provider"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown_provider", "login provider is not configured")
		return
	}
	var req struct {
		ReturnTo string `json:"return_to"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	to, err := s.auth.OAuthLinkStart(r.Context(), p, s.callbackURL(r, p.ID()), req.ReturnTo, u)
	if err != nil {
		s.log.Warn("oauth link start failed", "provider", p.ID(), "err", err)
		writeError(w, http.StatusBadGateway, "provider_unavailable", "login provider is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": to})
}

// --- кабинет ---------------------------------------------------------------

func (s *Server) features(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Features())
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if !decode(w, r, &req) {
		return
	}
	p, err := s.auth.UpdateProfile(r.Context(), u, req.DisplayName)
	switch {
	case errors.Is(err, auth.ErrWeakInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_input", "name: up to 100 characters, one line")
	case err != nil:
		s.fail(w, err)
	default:
		writeJSON(w, http.StatusOK, p)
	}
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	var req struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	err := s.auth.ChangePassword(r.Context(), u, req.Current, req.New)
	switch {
	case errors.Is(err, auth.ErrWeakInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_input", "password must be at least 8 characters")
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusForbidden, "wrong_password", "current password is wrong")
	case errors.Is(err, auth.ErrNoEmail):
		writeError(w, http.StatusConflict, "no_email", err.Error())
	case err != nil:
		s.internal(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) identities(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	items, err := s.auth.Identities(r.Context(), u)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) unlink(w http.ResponseWriter, r *http.Request, u uuid.UUID) {
	err := s.auth.Unlink(r.Context(), u, r.PathValue("provider"), r.PathValue("subject"))
	switch {
	case errors.Is(err, auth.ErrLastLogin):
		writeError(w, http.StatusConflict, "last_login", err.Error())
	case errors.Is(err, auth.ErrNotLinked):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case err != nil:
		s.internal(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) oauthExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	id, tokens, err := s.auth.ExchangeLogin(r.Context(), req.Code)
	switch {
	case errors.Is(err, auth.ErrOAuthState):
		writeError(w, http.StatusUnauthorized, "invalid_code", "login code is unknown, used or expired")
	case err != nil:
		s.internal(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"user_id": id, "tokens": tokens})
	}
}
