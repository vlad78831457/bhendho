// Package auth — своя авторизация ядра (ADR-44): email/пароль (argon2id),
// короткий access-JWT и refresh-токен с ротацией и обнаружением повторного использования.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"runtime"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"

	"offgrid/core/internal/core/auth/oauth"
	"offgrid/core/internal/core/db"
)

// Ошибки авторизации (наружу — без подробностей, чтобы не раскрывать существование аккаунтов).
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidToken       = errors.New("invalid token")
	ErrWeakInput          = errors.New("email must be valid and password at least 8 characters")
)

// Tokens — пара токенов сессии.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// Service — авторизация поверх пула Postgres.
type Service struct {
	pool       *pgxpool.Pool
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
	// hashSlots ограничивает одновременные argon2id: каждый берёт argonMemory (64 МиБ),
	// и волна входов без лимита съела бы память инстанса (ADR-55).
	hashSlots chan struct{}
	oauth     *oauth.Registry // вход через внешние аккаунты (ADR-58); nil — выключен
}

// New создаёт сервис авторизации.
func New(pool *pgxpool.Pool, secret []byte, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{pool: pool, secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL, now: time.Now,
		hashSlots: make(chan struct{}, runtime.NumCPU())}
}

// WithHashConcurrency задаёт лимит одновременных хэшей пароля (n <= 0 — по числу CPU).
func (s *Service) WithHashConcurrency(n int) *Service {
	if n > 0 {
		s.hashSlots = make(chan struct{}, n)
	}
	return s
}

// hashing выполняет f в свободном слоте; ждёт слот, пока жив запрос.
func (s *Service) hashing(ctx context.Context, f func()) error {
	select {
	case s.hashSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.hashSlots }()
	f()
	return nil
}

// Register создаёт пользователя и открывает сессию.
func (s *Service) Register(ctx context.Context, email, password, displayName string) (uuid.UUID, Tokens, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if _, err := mail.ParseAddress(email); err != nil || len(password) < 8 || len(password) > 256 {
		return uuid.Nil, Tokens{}, ErrWeakInput
	}
	var hash string
	var err error
	if herr := s.hashing(ctx, func() { hash, err = hashPassword(password) }); herr != nil {
		return uuid.Nil, Tokens{}, herr
	}
	if err != nil {
		return uuid.Nil, Tokens{}, err
	}
	id := uuid.New()
	var tokens Tokens
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, password_hash, display_name) VALUES ($1, $2, $3, $4)`,
			id, email, hash, displayName); err != nil {
			return err
		}
		tokens, err = s.issue(ctx, tx, id)
		return err
	})
	if db.IsUniqueViolation(err) {
		return uuid.Nil, Tokens{}, ErrEmailTaken
	}
	return id, tokens, err
}

// Login проверяет пароль и открывает сессию.
func (s *Service) Login(ctx context.Context, email, password string) (uuid.UUID, Tokens, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var (
		id   uuid.UUID
		hash string
	)
	err := s.pool.QueryRow(ctx, `SELECT id, COALESCE(password_hash, '') FROM users WHERE email = $1`, email).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// Тратим то же время, что на проверку настоящего хэша.
		_ = s.hashing(ctx, func() { _, _ = verifyPassword(password, dummyHash) })
		return uuid.Nil, Tokens{}, ErrInvalidCredentials
	}
	if err != nil {
		return uuid.Nil, Tokens{}, err
	}
	passwordless := hash == ""
	if passwordless {
		// Аккаунт только через провайдера (ADR-58): тратим то же время, чтобы не выдать это по скорости ответа.
		hash = dummyHash
	}
	var ok bool
	if herr := s.hashing(ctx, func() { ok, err = verifyPassword(password, hash) }); herr != nil {
		return uuid.Nil, Tokens{}, herr
	}
	if err != nil || !ok || passwordless {
		return uuid.Nil, Tokens{}, ErrInvalidCredentials
	}
	var tokens Tokens
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tokens, err = s.issue(ctx, tx, id)
		return err
	})
	return id, tokens, err
}

// Refresh — ротация: старый refresh отзывается, выдаётся новая пара. Повторное
// предъявление отозванного токена отзывает все сессии пользователя (кража токена).
func (s *Service) Refresh(ctx context.Context, refresh string) (Tokens, error) {
	var tokens Tokens
	var reused bool
	var owner uuid.UUID
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var (
			id        uuid.UUID
			expiresAt time.Time
			revokedAt *time.Time
		)
		err := tx.QueryRow(ctx, `SELECT id, user_id, expires_at, revoked_at FROM refresh_tokens
			WHERE token_hash = $1 FOR UPDATE`, hashToken(refresh)).Scan(&id, &owner, &expiresAt, &revokedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidToken
		}
		if err != nil {
			return err
		}
		if revokedAt != nil {
			reused = true
			return nil
		}
		if !expiresAt.After(s.now()) {
			return ErrInvalidToken
		}
		if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		tokens, err = s.issue(ctx, tx, owner)
		return err
	})
	if err != nil {
		return Tokens{}, err
	}
	if reused {
		if _, err := s.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now()
			WHERE user_id = $1 AND revoked_at IS NULL`, owner); err != nil {
			return Tokens{}, err
		}
		return Tokens{}, ErrInvalidToken
	}
	return tokens, nil
}

// Logout отзывает refresh-токен.
func (s *Service) Logout(ctx context.Context, refresh string) error {
	_, err := s.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL`, hashToken(refresh))
	return err
}

// ParseAccess проверяет access-JWT и возвращает пользователя. Без обращения к БД —
// любой инстанс проверяет сам (ADR-50).
func (s *Service) ParseAccess(token string) (uuid.UUID, error) {
	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return s.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithAudience("offgrid.access"),
		jwt.WithTimeFunc(s.now))
	if err != nil || !parsed.Valid {
		return uuid.Nil, ErrInvalidToken
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, ErrInvalidToken
	}
	return id, nil
}

func (s *Service) issue(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (Tokens, error) {
	now := s.now()
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   userID.String(),
		Audience:  jwt.ClaimStrings{"offgrid.access"},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
		ID:        uuid.NewString(),
	}).SignedString(s.secret)
	if err != nil {
		return Tokens{}, err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Tokens{}, err
	}
	refresh := "rt_" + base64.RawURLEncoding.EncodeToString(b[:])
	if _, err := tx.Exec(ctx, `INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		uuid.New(), userID, hashToken(refresh), now.Add(s.refreshTTL)); err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, RefreshToken: refresh, ExpiresIn: int(s.accessTTL.Seconds()), TokenType: "Bearer"}, nil
}

// Параметры argon2id: 64 МиБ, 1 проход, 4 потока. Цена по CPU и памяти измерена под нагрузкой
// (docs/reports/load-test.md); одновременных хэшей — не больше hashSlots (ADR-55).
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

var dummyHash, _ = hashPassword("dummy-password-for-timing")

func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unsupported hash")
	}
	var (
		version   int
		memory, t uint32
		threads   uint8
	)
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, err
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &t, &threads); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, t, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Profile — базовый профиль ядра (ADR-32: имя; аватар — со Storage, цикл M3).
type Profile struct {
	ID          uuid.UUID `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	HasPassword bool      `json:"has_password"`
	CreatedAt   time.Time `json:"created_at"`
}

// Profile возвращает профиль пользователя.
func (s *Service) Profile(ctx context.Context, id uuid.UUID) (Profile, error) {
	p := Profile{ID: id}
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(email, ''), display_name, password_hash IS NOT NULL, created_at FROM users WHERE id = $1`, id).
		Scan(&p.Email, &p.DisplayName, &p.HasPassword, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrInvalidToken
	}
	return p, err
}
