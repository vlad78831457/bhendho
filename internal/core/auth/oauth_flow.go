package auth

// Вход через внешние аккаунты (ADR-58):
//  1. Start — одноразовый state (в БД хэш), PKCE verifier и nonce на 10 минут → адрес провайдера;
//  2. Callback — state гасится, код меняется на профиль у провайдера, профиль → пользователь,
//     выдаётся одноразовый код входа (2 минуты);
//  3. ExchangeLogin — фронт меняет этот код на наши токены (токены не ходят в адресной строке).

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"offgrid/core/internal/core/auth/oauth"
)

const (
	oauthStateTTL = 10 * time.Minute
	oauthLoginTTL = 2 * time.Minute
)

// ErrOAuthState — вход не начинался, уже завершён или устарел.
var ErrOAuthState = errors.New("oauth state is unknown or expired")

// ErrOAuthDenied — человек отказался на стороне провайдера.
var ErrOAuthDenied = errors.New("oauth login denied by the user")

// WithOAuth подключает провайдеров входа.
func (s *Service) WithOAuth(r *oauth.Registry) *Service {
	s.oauth = r
	return s
}

// OAuthProviders — включённые провайдеры (для кнопок входа).
func (s *Service) OAuthProviders() []oauth.Provider { return s.oauth.List() }

// OAuthProvider — провайдер по id.
func (s *Service) OAuthProvider(id string) (oauth.Provider, bool) { return s.oauth.Get(id) }

// SafeReturn — куда вернуть после входа: только путь этого же сайта (не открытый редирект).
func SafeReturn(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	return p
}

// OAuthStart — адрес провайдера для начала входа.
func (s *Service) OAuthStart(ctx context.Context, p oauth.Provider, redirectURI, returnTo string) (string, error) {
	return s.start(ctx, p, redirectURI, returnTo, nil)
}

// OAuthLinkStart — адрес провайдера, чтобы привязать ещё один вход к вошедшему пользователю (кабинет).
func (s *Service) OAuthLinkStart(ctx context.Context, p oauth.Provider, redirectURI, returnTo string, userID uuid.UUID) (string, error) {
	return s.start(ctx, p, redirectURI, returnTo, &userID)
}

func (s *Service) start(ctx context.Context, p oauth.Provider, redirectURI, returnTo string, linkUser *uuid.UUID) (string, error) {
	state, verifier, nonce := oauth.RandomToken(), oauth.RandomToken(), oauth.RandomToken()
	_, _ = s.pool.Exec(ctx, `DELETE FROM oauth_states WHERE expires_at < now()`)
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO oauth_states (state_hash, provider, verifier, nonce, return_to, expires_at, link_user)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		hashToken(state), p.ID(), verifier, nonce, SafeReturn(returnTo), s.now().Add(oauthStateTTL), linkUser); err != nil {
		return "", err
	}
	return p.AuthURL(ctx, oauth.AuthParams{State: state, Challenge: oauth.Challenge(verifier), Nonce: nonce, RedirectURI: redirectURI})
}

// Callback — итог возврата от провайдера: одноразовый код входа или «привязано» (Linked), и куда
// вернуть пользователя (есть и при ошибке после проверки state).
type Callback struct {
	Code     string
	Linked   bool
	ReturnTo string
}

// OAuthCallback — возврат от провайдера: вход (код входа) или привязка к пользователю из кабинета.
func (s *Service) OAuthCallback(ctx context.Context, p oauth.Provider, redirectURI string, q url.Values) (Callback, error) {
	var verifier, nonce string
	var expires time.Time
	var linkUser *uuid.UUID
	res := Callback{ReturnTo: "/"}
	err := s.pool.QueryRow(ctx, `
		DELETE FROM oauth_states WHERE state_hash = $1 AND provider = $2 RETURNING verifier, nonce, return_to, expires_at, link_user`,
		hashToken(q.Get("state")), p.ID()).Scan(&verifier, &nonce, &res.ReturnTo, &expires, &linkUser)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && s.now().After(expires)) {
		return Callback{ReturnTo: "/"}, ErrOAuthState
	}
	if err != nil {
		return Callback{ReturnTo: "/"}, err
	}
	res.Linked = linkUser != nil
	if q.Get("error") != "" {
		return res, ErrOAuthDenied
	}
	prof, err := p.Exchange(ctx, oauth.ExchangeParams{
		Code: q.Get("code"), Verifier: verifier, Nonce: nonce, State: q.Get("state"), RedirectURI: redirectURI, Query: q,
	})
	if err != nil {
		return res, err
	}
	if linkUser != nil {
		return res, s.linkTo(ctx, p, prof, *linkUser)
	}
	userID, err := s.linkIdentity(ctx, p, prof)
	if err != nil {
		return res, err
	}
	code := oauth.RandomToken()
	_, _ = s.pool.Exec(ctx, `DELETE FROM oauth_logins WHERE expires_at < now()`)
	if _, err := s.pool.Exec(ctx, `INSERT INTO oauth_logins (code_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hashToken(code), userID, s.now().Add(oauthLoginTTL)); err != nil {
		return res, err
	}
	res.Code = code
	return res, nil
}

// linkTo — привязать внешний аккаунт к пользователю из кабинета; чужой аккаунт не перепривязывается.
// Пользователь без email получает подтверждённый email доверенного провайдера, если адрес свободен.
func (s *Service) linkTo(ctx context.Context, p oauth.Provider, prof oauth.Profile, userID uuid.UUID) error {
	email := strings.ToLower(strings.TrimSpace(prof.Email))
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "oauth:"+p.ID()+":"+prof.Subject); err != nil {
			return err
		}
		var owner uuid.UUID
		err := tx.QueryRow(ctx, `SELECT user_id FROM user_identities WHERE provider = $1 AND subject = $2`, p.ID(), prof.Subject).Scan(&owner)
		switch {
		case err == nil && owner != userID:
			return ErrIdentityTaken
		case err == nil:
			return nil // уже привязан к этому пользователю
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO user_identities (provider, subject, user_id, email, email_verified, display_name)
			VALUES ($1, $2, $3, $4, $5, $6)`, p.ID(), prof.Subject, userID, nullable(email), prof.EmailVerified, prof.Name); err != nil {
			return err
		}
		if p.TrustEmail() && prof.EmailVerified && email != "" {
			_, err = tx.Exec(ctx, `UPDATE users SET email = $2 WHERE id = $1 AND email IS NULL
				AND NOT EXISTS (SELECT 1 FROM users WHERE email = $2)`, userID, email)
		}
		return err
	})
}

// ExchangeLogin — одноразовый код входа → сессия.
func (s *Service) ExchangeLogin(ctx context.Context, code string) (uuid.UUID, Tokens, error) {
	var userID uuid.UUID
	var tokens Tokens
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var expires time.Time
		err := tx.QueryRow(ctx, `DELETE FROM oauth_logins WHERE code_hash = $1 RETURNING user_id, expires_at`, hashToken(code)).
			Scan(&userID, &expires)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && s.now().After(expires)) {
			return ErrOAuthState
		}
		if err != nil {
			return err
		}
		tokens, err = s.issue(ctx, tx, userID)
		return err
	})
	return userID, tokens, err
}

// linkIdentity — профиль провайдера → наш пользователь:
//   - эта привязка уже есть — её пользователь (данные профиля обновляются);
//   - провайдер подтвердил email и ему доверяем (TrustEmail) — пользователь с этим email, если есть;
//   - иначе новый пользователь. Email ему записывается, только если подтверждён доверенным
//     провайдером и свободен: иначе чужой человек занял бы адрес, под которым потом не зарегистрироваться.
func (s *Service) linkIdentity(ctx context.Context, p oauth.Provider, prof oauth.Profile) (uuid.UUID, error) {
	email := strings.ToLower(strings.TrimSpace(prof.Email))
	trusted := p.TrustEmail() && prof.EmailVerified && email != ""
	var userID uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Два одновременных первых входа одним аккаунтом не создают двух пользователей.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "oauth:"+p.ID()+":"+prof.Subject); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT user_id FROM user_identities WHERE provider = $1 AND subject = $2`, p.ID(), prof.Subject).Scan(&userID)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE user_identities SET email = $3, email_verified = $4, display_name = $5, last_login_at = now()
				WHERE provider = $1 AND subject = $2`, p.ID(), prof.Subject, nullable(email), prof.EmailVerified, prof.Name)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		found := false
		if trusted {
			err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&userID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			found = err == nil
		}
		if !found {
			userID = uuid.New()
			var userEmail any
			if trusted {
				userEmail = email // свободен: занятый нашёлся бы выше
			}
			if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, display_name) VALUES ($1, $2, $3)`,
				userID, userEmail, prof.Name); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO user_identities (provider, subject, user_id, email, email_verified, display_name)
			VALUES ($1, $2, $3, $4, $5, $6)`, p.ID(), prof.Subject, userID, nullable(email), prof.EmailVerified, prof.Name)
		return err
	})
	return userID, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
