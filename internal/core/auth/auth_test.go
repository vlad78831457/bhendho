package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"offgrid/core/internal/core/db/dbtest"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := hashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := verifyPassword("correct horse", h); !ok {
		t.Fatal("right password rejected")
	}
	if ok, _ := verifyPassword("wrong horse", h); ok {
		t.Fatal("wrong password accepted")
	}
	h2, _ := hashPassword("correct horse")
	if h == h2 {
		t.Fatal("salt must differ between hashes")
	}
}

func TestSessionLifecycle(t *testing.T) {
	pool, _ := dbtest.New(t)
	ctx := context.Background()
	s := New(pool, []byte("test-secret-test-secret-test-secret!"), time.Minute, time.Hour)

	if _, _, err := s.Register(ctx, "not-an-email", "longpassword", ""); !errors.Is(err, ErrWeakInput) {
		t.Fatalf("bad email: %v", err)
	}
	if _, _, err := s.Register(ctx, "a@b.io", "short", ""); !errors.Is(err, ErrWeakInput) {
		t.Fatalf("short password: %v", err)
	}
	id, tok, err := s.Register(ctx, "Tutor@Example.org", "longpassword", "Тьютор")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Register(ctx, "tutor@example.org", "longpassword", ""); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate email (case-insensitive): %v", err)
	}
	if got, err := s.ParseAccess(tok.AccessToken); err != nil || got != id {
		t.Fatalf("access: %v %v", got, err)
	}
	if _, err := s.ParseAccess(tok.AccessToken + "x"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("tampered access token accepted")
	}

	if _, _, err := s.Login(ctx, "tutor@example.org", "wrongpassword"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, _, err := s.Login(ctx, "ghost@example.org", "longpassword"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: %v", err)
	}
	_, tok2, err := s.Login(ctx, "tutor@example.org", "longpassword")
	if err != nil {
		t.Fatal(err)
	}

	rotated, err := s.Refresh(ctx, tok2.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	// Повтор старого refresh — признак кражи: отзываются все сессии.
	if _, err := s.Refresh(ctx, tok2.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("reused refresh: %v", err)
	}
	if _, err := s.Refresh(ctx, rotated.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("reuse must revoke every session of the user")
	}
	if _, err := s.Refresh(ctx, tok.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("reuse must revoke the registration session too")
	}

	_, tok3, err := s.Login(ctx, "tutor@example.org", "longpassword")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Logout(ctx, tok3.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Refresh(ctx, tok3.RefreshToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("refresh after logout accepted")
	}
}

func TestExpiredAccessRejected(t *testing.T) {
	s := &Service{secret: []byte("test-secret-test-secret-test-secret!"), accessTTL: time.Minute, now: time.Now}
	pool, _ := dbtest.New(t)
	s.pool = pool
	old := New(pool, s.secret, time.Minute, time.Hour)
	old.now = func() time.Time { return time.Now().Add(-time.Hour) }
	_, tok, err := old.Register(context.Background(), "old@example.org", "longpassword", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParseAccess(tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("expired access token accepted")
	}
}

func TestHashingIsBoundedAndCancellable(t *testing.T) {
	s := (&Service{}).WithHashConcurrency(2)
	var (
		mu           sync.Mutex
		inside, peak int
	)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.hashing(context.Background(), func() {
				mu.Lock()
				inside++
				peak = max(peak, inside)
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
			})
		}()
	}
	wg.Wait()
	if peak != 2 {
		t.Fatalf("peak concurrent hashes = %d, want 2", peak)
	}

	// Слоты заняты — запрос, у которого кончилось время, не ждёт вечно.
	s.hashSlots <- struct{}{}
	s.hashSlots <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ran := false
	if err := s.hashing(ctx, func() { ran = true }); !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("err = %v, ran = %v", err, ran)
	}
}
