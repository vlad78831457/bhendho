// Command core — процесс ядра OFFGRID.
//
//	core serve                                   HTTP API, шлюз прорабов, WebSocket, фоновые задания
//	core migrate                                 применить миграции и выйти
//	core issue-token --service tale --name p1    выпустить токен прораба (печатается один раз)
//	core promo-issuer --name shop                 токен службы оплаты (эмитента промокодов), один раз
//	core promo-codes --amount 500 --count 10      подарочные промокоды оператора (печатаются один раз)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"offgrid/core/internal/core/auth"
	"offgrid/core/internal/core/auth/oauth"
	"offgrid/core/internal/core/config"
	"offgrid/core/internal/core/contracts"
	"offgrid/core/internal/core/db"
	"offgrid/core/internal/core/engine"
	"offgrid/core/internal/core/files"
	"offgrid/core/internal/core/httpapi"
	"offgrid/core/internal/core/modules"
	"offgrid/core/internal/core/outbox"
	"offgrid/core/internal/core/realtime"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, log)
	case "migrate":
		err = withDB(ctx, func(cfg config.Config, pool *pgxpool.Pool) error { return nil })
	case "issue-token":
		err = issueToken(ctx, args)
	case "promo-issuer":
		err = promoIssuer(ctx, args)
	case "promo-codes":
		err = promoCodes(ctx, args)
	default:
		err = fmt.Errorf("unknown command %q (serve | migrate | issue-token | promo-issuer | promo-codes)", cmd)
	}
	if err != nil {
		log.Error("core stopped", "cmd", cmd, "err", err)
		os.Exit(1)
	}
}

// withDB — конфиг, пул и миграции.
func withDB(ctx context.Context, f func(config.Config, *pgxpool.Pool) error) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	return f(cfg, pool)
}

func issueToken(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("issue-token", flag.ContinueOnError)
	service := fs.String("service", "", "target_service")
	name := fs.String("name", "", "имя прораба")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *service == "" || *name == "" {
		return errors.New("--service and --name are required")
	}
	return withDB(ctx, func(cfg config.Config, pool *pgxpool.Pool) error {
		token, err := engine.New(pool, cfg, nil).IssueServiceToken(ctx, *service, *name)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	})
}

func serve(ctx context.Context, log *slog.Logger) error {
	return withDB(ctx, func(cfg config.Config, pool *pgxpool.Pool) error {
		validator, err := contracts.Load()
		if err != nil {
			return err
		}
		hub := realtime.NewHub(pool, cfg.WSPingInterval, cfg.WSOrigins, log)
		eng := engine.New(pool, cfg, hub)
		if cfg.StorageDir != "" {
			eng.WithFiles(files.Local{Dir: cfg.StorageDir}) // ADR-60: каталог — общий том реплик ядра
			log.Info("file storage enabled", "dir", cfg.StorageDir)
		}

		if cfg.ModulesDir != "" {
			mods, err := modules.LoadDir(cfg.ModulesDir, validator)
			if err != nil {
				return err
			}
			for _, m := range mods {
				if err := eng.RegisterModule(ctx, m); err != nil {
					return err
				}
				log.Info("module registered", "module", m.Manifest.ModuleID, "service", m.TargetService(), "forms", len(m.Forms))
			}
		}
		for _, spec := range cfg.ProrabTokens {
			parts := strings.SplitN(spec, ":", 3)
			if len(parts) != 3 {
				return errors.New("CORE_PRORAB_TOKENS: expected service:name:token")
			}
			if err := eng.EnsureServiceToken(ctx, engine.ProrabToken{Service: parts[0], Name: parts[1], Token: parts[2]}); err != nil {
				return err
			}
			log.Info("prorab provisioned", "service", parts[0], "name", parts[1])
		}
		for _, spec := range cfg.PromoIssuers {
			name, token, ok := strings.Cut(spec, ":")
			if !ok {
				return errors.New("CORE_PROMO_ISSUERS: expected name:token")
			}
			if token == "" {
				log.Warn("promo issuer without token skipped", "name", name) // служба оплаты не настроена
				continue
			}
			if err := eng.EnsurePromoIssuer(ctx, name, token); err != nil {
				return err
			}
			log.Info("promo issuer provisioned", "name", name)
		}

		bg, cancel := context.WithCancel(ctx)
		defer cancel()
		var wg sync.WaitGroup
		for _, run := range []func(context.Context){
			hub.Listen,
			outbox.New(pool, cfg.OutboxBatch, cfg.OutboxInterval, log).Run,
			func(c context.Context) { eng.RunBackground(c, log) },
		} {
			wg.Add(1)
			go func() { defer wg.Done(); run(bg) }()
		}

		if cfg.PprofAddr != "" {
			go servePprof(cfg.PprofAddr, log)
		}

		providers, err := oauthRegistry(cfg)
		if err != nil {
			return err
		}
		for _, p := range providers.List() {
			log.Info("login provider enabled", "id", p.ID())
		}
		authSvc := auth.New(pool, cfg.JWTSecret, cfg.AccessTTL, cfg.RefreshTTL).
			WithHashConcurrency(cfg.AuthHashConcurrency).WithOAuth(providers)
		srv := &http.Server{
			Addr: cfg.HTTPAddr,
			Handler: httpapi.New(eng, authSvc, hub, log).WithPublicURL(cfg.PublicURL).
				WithFileLinks(files.NewSigner(cfg.JWTSecret), cfg.FileLinkTTL).Handler(),
			ReadHeaderTimeout: 5 * time.Second,
		}
		errc := make(chan error, 1)
		go func() {
			log.Info("core listening", "addr", cfg.HTTPAddr)
			errc <- srv.ListenAndServe()
		}()

		select {
		case err := <-errc:
			cancel()
			wg.Wait()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
		}
		shutdownCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		err = srv.Shutdown(shutdownCtx)
		cancel()
		wg.Wait()
		return err
	})
}

// servePprof — профилировщик Go на отдельном адресе (CORE_PPROF_ADDR, ADR-55): для разбора
// нагрузки. Порт не публикуется наружу; в проде включается только на время разбора.
func servePprof(addr string, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	log.Info("pprof listening", "addr", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Warn("pprof stopped", "err", err)
	}
}

// promoIssuer — токен службы оплаты (эмитента промокодов, ADR-57); прежний токен перестаёт работать.
func promoIssuer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("promo-issuer", flag.ContinueOnError)
	name := fs.String("name", "", "имя эмитента, например shop")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	return withDB(ctx, func(cfg config.Config, pool *pgxpool.Pool) error {
		token, err := engine.New(pool, cfg, nil).IssuePromoIssuerToken(ctx, *name)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	})
}

// promoCodes — подарочные коды оператора (без службы оплаты): тестерам, партнёрам, акции.
// Коды печатаются один раз — ядро хранит только их хэши.
func promoCodes(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("promo-codes", flag.ContinueOnError)
	issuer := fs.String("issuer", "operator", "эмитент (создаётся, если его нет)")
	amount := fs.String("amount", "", "сумма в GC")
	count := fs.Int("count", 1, "сколько кодов")
	days := fs.Int("days", 0, "срок действия в днях (0 — бессрочные)")
	email := fs.String("email", "", "персональный код для этого email")
	if err := fs.Parse(args); err != nil {
		return err
	}
	value, err := decimal.NewFromString(*amount)
	if err != nil || *count < 1 || *count > 1000 {
		return errors.New("--amount is required (GC) and --count must be 1..1000")
	}
	return withDB(ctx, func(cfg config.Config, pool *pgxpool.Pool) error {
		eng := engine.New(pool, cfg, nil)
		iss, err := eng.PromoIssuerByName(ctx, *issuer)
		if err != nil {
			return err
		}
		spec := engine.PromoSpec{Amount: value, ForEmail: *email}
		if *days > 0 {
			exp := time.Now().Add(time.Duration(*days) * 24 * time.Hour)
			spec.ExpiresAt = &exp
		}
		for range *count {
			p, _, err := eng.IssuePromo(ctx, iss, spec)
			if err != nil {
				return err
			}
			fmt.Printf("%s\t%s %s\n", p.Code, p.Amount, p.CurrencyID)
		}
		return nil
	})
}

// oauthRegistry — провайдеры входа (ADR-58): готовые Google и VK ID по ключам плюс любые из
// CORE_OAUTH_PROVIDERS. Провайдер без client id выключен.
func oauthRegistry(cfg config.Config) (*oauth.Registry, error) {
	extra, err := oauth.ParseExtra(cfg.OAuthProviders)
	if err != nil {
		return nil, err
	}
	presets := oauth.Presets(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleIssuer, cfg.VKClientID, cfg.VKClientSecret, cfg.VKBaseURL)
	return oauth.NewRegistry(append(presets, extra...), nil)
}
