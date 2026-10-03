// Package config — все числа ядра в одном месте, из переменных окружения
// («все цифры в конфиге», ADR-23/30/37).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config — параметры процесса ядра.
type Config struct {
	HTTPAddr    string
	DatabaseURL string
	ModulesDir  string
	DBMaxConns  int32

	JWTSecret  []byte
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	// AuthHashConcurrency — одновременных argon2id на инстанс (0 — по числу CPU), ADR-55.
	AuthHashConcurrency int
	// CatalogTTL — сколько инстанс держит каталог в памяти (0 — не кэшировать), ADR-55.
	CatalogTTL time.Duration
	// PprofAddr — адрес профилировщика Go (net/http/pprof); пусто — выключен. Только во внутренней сети.
	PprofAddr string

	DefaultCurrency string

	// Очередь и попытки (ADR-30).
	AcceptTimeout  time.Duration // на ответ «принято/не принято» после claim
	LockMultiplier int           // ожидание попытки = множитель × ETA
	LockCeiling    time.Duration // потолок ожидания попытки
	DefaultETA     time.Duration // если нет ни оценки, ни истории сервиса
	MaxAttempts    int

	// Фоновые задания.
	SweepInterval  time.Duration
	OutboxInterval time.Duration
	OutboxBatch    int
	TraceTTL       time.Duration // ADR-24
	TaskRetention  time.Duration // ADR-37

	// Realtime (ADR-23).
	WSPingInterval time.Duration
	WSOrigins      []string // разрешённые Origin браузеров для WebSocket

	// Прорабы из конфига: service:name:token (ADR-52).
	ProrabTokens []string

	// Демо-провайдер пополнения (ADR-26); выключен по умолчанию.
	DemoDepositEnabled bool
	DemoDepositMax     string

	// Промокоды (ADR-57): эмитенты «имя:токен» (CORE_PROMO_ISSUERS), предел суммы кода,
	// сколько неверных кодов пользователь может ввести за окно.
	PromoIssuers       []string
	PromoMaxAmount     string
	PromoMaxFailures   int
	PromoFailureWindow time.Duration

	// Вход через внешние аккаунты (ADR-58). PublicURL — адрес сайта снаружи (из него адрес возврата
	// от провайдера: PublicURL + /api/v1/auth/oauth/<id>/callback). Провайдер без client id выключен.
	PublicURL                          string
	GoogleClientID, GoogleClientSecret string
	GoogleIssuer                       string
	VKClientID, VKClientSecret         string
	VKBaseURL                          string
	OAuthProviders                     string // JSON: дополнительные провайдеры (oidc | vkid)

	// Файлы пользователей (ADR-60): каталог хранилища (пусто — загрузка выключена), предел файла,
	// большая сторона картинки после пересохранения, срок подписанной ссылки, файлов на пользователя.
	StorageDir     string
	FileMaxBytes   int64
	ImageMaxSide   int
	FileLinkTTL    time.Duration
	FileMaxPerUser int
}

// FromEnv читает конфиг; обязательны только DATABASE_URL и JWT_SECRET.
func FromEnv() (Config, error) {
	c := Config{
		HTTPAddr:            str("CORE_HTTP_ADDR", ":8080"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		ModulesDir:          str("CORE_MODULES_DIR", ""),
		DBMaxConns:          int32(num("CORE_DB_MAX_CONNS", 20)),
		JWTSecret:           []byte(os.Getenv("JWT_SECRET")),
		AccessTTL:           dur("CORE_ACCESS_TTL", 15*time.Minute),
		RefreshTTL:          dur("CORE_REFRESH_TTL", 30*24*time.Hour),
		AuthHashConcurrency: num("CORE_AUTH_HASH_CONCURRENCY", 0),
		CatalogTTL:          dur("CORE_CATALOG_TTL", 5*time.Second),
		PprofAddr:           str("CORE_PPROF_ADDR", ""),
		DefaultCurrency:     str("CORE_DEFAULT_CURRENCY", "GC"),
		AcceptTimeout:       dur("CORE_ACCEPT_TIMEOUT", 30*time.Second),
		LockMultiplier:      num("CORE_LOCK_MULTIPLIER", 3),
		LockCeiling:         dur("CORE_LOCK_CEILING", 2*time.Hour),
		DefaultETA:          dur("CORE_DEFAULT_ETA", 60*time.Second),
		MaxAttempts:         num("CORE_MAX_ATTEMPTS", 5),
		SweepInterval:       dur("CORE_SWEEP_INTERVAL", 2*time.Second),
		OutboxInterval:      dur("CORE_OUTBOX_INTERVAL", 200*time.Millisecond),
		OutboxBatch:         num("CORE_OUTBOX_BATCH", 500),
		TraceTTL:            dur("CORE_TRACE_TTL", 14*24*time.Hour),
		TaskRetention:       dur("CORE_TASK_RETENTION", 14*24*time.Hour),
		WSPingInterval:      dur("CORE_WS_PING_INTERVAL", 15*time.Second),
		WSOrigins:           list("CORE_WS_ORIGINS"),
		ProrabTokens:        list("CORE_PRORAB_TOKENS"),
		DemoDepositEnabled:  str("CORE_DEMO_DEPOSIT", "false") == "true",
		DemoDepositMax:      str("CORE_DEMO_DEPOSIT_MAX", "10000"),
		PromoIssuers:        list("CORE_PROMO_ISSUERS"),
		PromoMaxAmount:      str("CORE_PROMO_MAX_AMOUNT", "100000"),
		PromoMaxFailures:    num("CORE_PROMO_MAX_FAILURES", 10),
		PromoFailureWindow:  dur("CORE_PROMO_FAILURE_WINDOW", 15*time.Minute),
		PublicURL:           strings.TrimRight(str("CORE_PUBLIC_URL", ""), "/"),
		GoogleClientID:      str("CORE_OAUTH_GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret:  str("CORE_OAUTH_GOOGLE_CLIENT_SECRET", ""),
		GoogleIssuer:        str("CORE_OAUTH_GOOGLE_ISSUER", ""),
		VKClientID:          str("CORE_OAUTH_VK_CLIENT_ID", ""),
		VKClientSecret:      str("CORE_OAUTH_VK_CLIENT_SECRET", ""),
		VKBaseURL:           str("CORE_OAUTH_VK_BASE_URL", ""),
		OAuthProviders:      str("CORE_OAUTH_PROVIDERS", ""),
		StorageDir:          str("CORE_STORAGE_DIR", ""),
		FileMaxBytes:        int64(num("CORE_FILE_MAX_BYTES", 15<<20)),
		ImageMaxSide:        num("CORE_IMAGE_MAX_SIDE", 1024),
		FileLinkTTL:         dur("CORE_FILE_LINK_TTL", 15*time.Minute),
		FileMaxPerUser:      num("CORE_FILE_MAX_PER_USER", 300),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		return c, fmt.Errorf("JWT_SECRET must be at least 32 bytes")
	}
	return c, nil
}

// ForTests — конфиг с короткими таймингами для интеграционных тестов.
func ForTests(dbURL string) Config {
	return Config{
		DatabaseURL:        dbURL,
		DBMaxConns:         10,
		JWTSecret:          []byte("test-secret-test-secret-test-secret!"),
		AccessTTL:          time.Minute,
		RefreshTTL:         time.Hour,
		DefaultCurrency:    "GC",
		AcceptTimeout:      30 * time.Second,
		LockMultiplier:     3,
		LockCeiling:        time.Hour,
		DefaultETA:         time.Minute,
		MaxAttempts:        5,
		SweepInterval:      time.Second,
		OutboxInterval:     50 * time.Millisecond,
		OutboxBatch:        100,
		TraceTTL:           14 * 24 * time.Hour,
		TaskRetention:      14 * 24 * time.Hour,
		WSPingInterval:     15 * time.Second,
		DemoDepositEnabled: true,
		DemoDepositMax:     "10000",
		PromoMaxAmount:     "100000",
		PromoMaxFailures:   10,
		PromoFailureWindow: 15 * time.Minute,
		FileMaxBytes:       15 << 20,
		ImageMaxSide:       1024,
		FileLinkTTL:        15 * time.Minute,
		FileMaxPerUser:     300,
	}
}

func str(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func num(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func dur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func list(key string) []string {
	var out []string
	for _, s := range strings.Split(os.Getenv(key), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
