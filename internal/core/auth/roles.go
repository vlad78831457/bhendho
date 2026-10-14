package auth

// Роли пользователя (ADR-61). Роль ставит только оператор командой на сервере; API её показывает
// в профиле, но не меняет.

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	RoleUser   = "user"
	RoleTester = "tester"
	RoleAdmin  = "admin"
)

var (
	// ErrBadRole — такой роли нет.
	ErrBadRole = errors.New("role must be user, tester or admin")
	// ErrUserNotFound — нет пользователя с таким id или email.
	ErrUserNotFound = errors.New("user not found")
)

// ValidRole — роль из справочника.
func ValidRole(role string) bool {
	return role == RoleUser || role == RoleTester || role == RoleAdmin
}

// FindUser находит пользователя по id или email (без учёта регистра).
func (s *Service) FindUser(ctx context.Context, ref string) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {
		var ok bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&ok); err != nil {
			return uuid.Nil, err
		}
		if !ok {
			return uuid.Nil, ErrUserNotFound
		}
		return id, nil
	}
	var id uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, strings.ToLower(ref)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrUserNotFound
	}
	return id, err
}

// SetRole ставит роль пользователю. Вызывается только из команды оператора.
func (s *Service) SetRole(ctx context.Context, userID uuid.UUID, role string) (Profile, error) {
	if !ValidRole(role) {
		return Profile{}, ErrBadRole
	}
	tag, err := s.pool.Exec(ctx, `UPDATE users SET role = $2 WHERE id = $1`, userID, role)
	if err != nil {
		return Profile{}, err
	}
	if tag.RowsAffected() == 0 {
		return Profile{}, ErrUserNotFound
	}
	return s.Profile(ctx, userID)
}
