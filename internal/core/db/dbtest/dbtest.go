// Package dbtest даёт интеграционным тестам отдельную свежую базу с миграциями.
// Адрес сервера — TEST_DATABASE_URL (в compose задан для сервиса test).
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"offgrid/core/internal/core/db"
)

// New создаёт базу test_<random>, применяет миграции и удаляет базу после теста.
func New(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL not set — integration tests run via docker compose run --rm test")
	}
	ctx := context.Background()

	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "test_" + hex.EncodeToString(b[:])

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create db: %v", err)
	}
	_ = conn.Close(ctx)

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	dbURL := u.String()

	pool, err := db.Open(ctx, dbURL, 10)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return pool, dbURL
}
