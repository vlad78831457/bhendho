package auth

// Кабинет пользователя: имя, пароль, способы входа (ADR-58). Привязка ещё одного внешнего
// аккаунта начинается в oauth_flow.go (OAuthLinkStart) и завершается в его колбэке.

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxDisplayName = 100

var (
	// ErrIdentityTaken — внешний аккаунт уже привязан к другому пользователю.
	ErrIdentityTaken = errors.New("this external account belongs to another user")
	// ErrLastLogin — нельзя отвязать последний способ входа: в аккаунт будет не войти.
	ErrLastLogin = errors.New("the last way to sign in cannot be removed")
	// ErrNoEmail — пароль без email бесполезен: входить по паролю не с чем.
	ErrNoEmail = errors.New("the account has no email to sign in with a password")
	// ErrNotLinked — такого входа у пользователя нет.
	ErrNotLinked = errors.New("sign-in method not found")
)

// Identity — внешний аккаунт, через который пользователь входит.
type Identity struct {
	Provider    string     `json:"provider"`
	Subject     string     `json:"subject"`
	Email       string     `json:"email,omitempty"`
	DisplayName string     `json:"display_name"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// Identities — внешние аккаунты пользователя, старые сверху.
func (s *Service) Identities(ctx context.Context, userID uuid.UUID) ([]Identity, error) {
	rows, err := s.pool.Query(ctx, `SELECT provider, subject, COALESCE(email, ''), display_name, created_at, last_login_at
		FROM user_identities WHERE user_id = $1 ORDER BY created_at, provider`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Identity, error) {
		var i Identity
		err := r.Scan(&i.Provider, &i.Subject, &i.Email, &i.DisplayName, &i.CreatedAt, &i.LastLoginAt)
		return i, err
	})
}

// UpdateProfile — имя, которое видно в приложении.
func (s *Service) UpdateProfile(ctx context.Context, userID uuid.UUID, displayName string) (Profile, error) {
	displayName = strings.TrimSpace(displayName)
	if utf8.RuneCountInString(displayName) > maxDisplayName || strings.ContainsAny(displayName, "\r\n\t") {
		return Profile{}, ErrWeakInput
	}
	if _, err := s.pool.Exec(ctx, `UPDATE users SET display_name = $2 WHERE id = $1`, userID, displayName); err != nil {
		return Profile{}, err
	}
	return s.Profile(ctx, userID)
}

// ChangePassword — сменить пароль (нужен текущий) или задать его, если входили только через
// провайдера (нужен email, по которому входить).
func (s *Service) ChangePassword(ctx context.Context, userID uuid.UUID, current, next string) error {
	if len(next) < 8 || len(next) > 256 {
		return ErrWeakInput
	}
	var email, hash string
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(email, ''), COALESCE(password_hash, '') FROM users WHERE id = $1`, userID).
		Scan(&email, &hash); err != nil {
		return err
	}
	if email == "" {
		return ErrNoEmail
	}
	if hash != "" {
		var ok bool
		var err error
		if herr := s.hashing(ctx, func() { ok, err = verifyPassword(current, hash) }); herr != nil {
			return herr
		}
		if err != nil || !ok {
			return ErrInvalidCredentials
		}
	}
	var newHash string
	var err error
	if herr := s.hashing(ctx, func() { newHash, err = hashPassword(next) }); herr != nil {
		return herr
	}
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, newHash)
	return err
}

// Unlink — отвязать внешний аккаунт; последний способ входа не отвязывается.
func (s *Service) Unlink(ctx context.Context, userID uuid.UUID, provider, subject string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var hasPassword bool
		if err := tx.QueryRow(ctx, `SELECT password_hash IS NOT NULL AND email IS NOT NULL FROM users WHERE id = $1 FOR UPDATE`, userID).
			Scan(&hasPassword); err != nil {
			return err
		}
		var others int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_identities WHERE user_id = $1 AND NOT (provider = $2 AND subject = $3)`,
			userID, provider, subject).Scan(&others); err != nil {
			return err
		}
		if !hasPassword && others == 0 {
			return ErrLastLogin
		}
		tag, err := tx.Exec(ctx, `DELETE FROM user_identities WHERE user_id = $1 AND provider = $2 AND subject = $3`, userID, provider, subject)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotLinked
		}
		return err
	})
}
