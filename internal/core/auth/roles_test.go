package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"offgrid/core/internal/core/db/dbtest"
)

func TestRoles(t *testing.T) {
	pool, _ := dbtest.New(t)
	ctx := context.Background()
	s := New(pool, []byte("test-secret-test-secret-test-secret!"), time.Minute, time.Hour)

	id, _, err := s.Register(ctx, "Admin@Example.org", "longpassword", "")
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Profile(ctx, id); p.Role != RoleUser {
		t.Fatalf("new user role: %q", p.Role)
	}

	// Поиск по email без учёта регистра и по id.
	if got, err := s.FindUser(ctx, " ADMIN@example.org "); err != nil || got != id {
		t.Fatalf("find by email: %v %v", got, err)
	}
	if got, err := s.FindUser(ctx, id.String()); err != nil || got != id {
		t.Fatalf("find by id: %v %v", got, err)
	}
	if _, err := s.FindUser(ctx, "ghost@example.org"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown email: %v", err)
	}
	if _, err := s.FindUser(ctx, uuid.NewString()); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown id: %v", err)
	}

	for _, role := range []string{RoleAdmin, RoleTester, RoleUser} {
		p, err := s.SetRole(ctx, id, role)
		if err != nil || p.Role != role {
			t.Fatalf("set %s: %v %v", role, p.Role, err)
		}
	}
	if _, err := s.SetRole(ctx, id, "root"); !errors.Is(err, ErrBadRole) {
		t.Fatalf("bad role: %v", err)
	}
	if _, err := s.SetRole(ctx, uuid.New(), RoleAdmin); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	// Справочник ролей держит и база: мимо SetRole чужую роль не записать.
	if _, err := pool.Exec(ctx, `UPDATE users SET role = 'root' WHERE id = $1`, id); err == nil {
		t.Fatal("database accepted an unknown role")
	}
}
