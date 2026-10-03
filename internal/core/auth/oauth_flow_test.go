package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"offgrid/core/internal/core/auth/oauth"
	"offgrid/core/internal/core/db/dbtest"
)

type stubProvider struct{ trust bool }

func (stubProvider) ID() string         { return "stub" }
func (stubProvider) Name() string       { return "Stub" }
func (p stubProvider) TrustEmail() bool { return p.trust }
func (stubProvider) AuthURL(context.Context, oauth.AuthParams) (string, error) {
	return "", nil
}
func (stubProvider) Exchange(context.Context, oauth.ExchangeParams) (oauth.Profile, error) {
	return oauth.Profile{}, nil
}

// Два одновременных первых входа одним внешним аккаунтом — один пользователь.
func TestLinkIdentityConcurrentFirstLogin(t *testing.T) {
	pool, _ := dbtest.New(t)
	s := New(pool, []byte("test-secret-test-secret-test-secret!"), time.Minute, time.Hour)
	prof := oauth.Profile{Subject: "same", Email: "twin@example.org", EmailVerified: true, Name: "Twin"}

	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.linkIdentity(context.Background(), stubProvider{trust: true}, prof)
			if err != nil {
				t.Error(err)
			}
			ids[i] = id.String()
		}()
	}
	wg.Wait()
	for _, id := range ids[1:] {
		if id != ids[0] {
			t.Fatalf("concurrent first logins created different users: %v", ids)
		}
	}
	var users int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM users`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("users: %d %v", users, err)
	}
}
