package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

	"smm-tg-bot/internal/app"
	"smm-tg-bot/internal/config"
	"smm-tg-bot/internal/httpapi"
	"smm-tg-bot/internal/payments"
	"smm-tg-bot/internal/sheetslog"
	"smm-tg-bot/internal/smm"
	"smm-tg-bot/internal/storage"
	"smm-tg-bot/internal/telegram"
)

func main() {
	_ = godotenv.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("config", "error", err)
		os.Exit(1)
	}
	logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}))
	logger.Info("postgres config", "database_url", safeDatabaseURL(cfg.DatabaseURL), "max_conns", cfg.PostgresMaxConns)

	pgxCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		logger.Error("postgres config", "error", err)
		os.Exit(1)
	}
	pgxCfg.MaxConns = cfg.PostgresMaxConns
	pgxCfg.MinConns = cfg.PostgresMinConns
	pgxCfg.MaxConnLifetime = time.Hour
	pgxCfg.MaxConnIdleTime = 10 * time.Minute
	db, err := pgxpool.NewWithConfig(ctx, pgxCfg)
	if err != nil {
		logger.Error("postgres", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := storage.Migrate(ctx, db); err != nil {
		logger.Error("migrate", "error", err)
		os.Exit(1)
	}
	orm, err := storage.OpenORM(cfg.DatabaseURL)
	if err != nil {
		logger.Error("orm", "error", err)
		os.Exit(1)
	}
	if err := orm.SetPoolLimits(int(cfg.PostgresMaxConns), int(cfg.PostgresMinConns)); err != nil {
		logger.Error("orm pool", "error", err)
		os.Exit(1)
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	if err := rdb.Ping(ctx).Err(); err != nil {
		logger.Error("redis", "error", err)
		os.Exit(1)
	}
	defer rdb.Close()

	bot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		logger.Error("telegram", "error", err)
		os.Exit(1)
	}

	store := storage.New(db, orm)
	smmClient := smm.NewClient(cfg.SocRocketAPIURL, cfg.SocRocketAPIKey)
	paymentHub := payments.NewHub(cfg)
	sheetsClient, err := sheetslog.New(ctx, cfg, logger)
	if err != nil {
		logger.Error("google sheets", "error", err)
		os.Exit(1)
	}
	service := app.NewService(cfg, store, rdb, smmClient, paymentHub, sheetsClient, bot, logger)
	tg := telegram.New(service, bot, logger)

	router := chi.NewRouter()
	httpapi.Mount(router, service, paymentHub, logger)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("http listening", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http", "error", err)
			stop()
		}
	}()
	if cfg.OrderSyncEnabled {
		go service.RunOrderSync(ctx)
	}
	if cfg.PaymentPollEnabled {
		go service.RunPaymentPoll(ctx)
	}
	if cfg.BackupEnabled {
		go service.RunBackups(ctx)
	}
	go tg.Run(ctx)

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	logger.Info("stopped")
}

func safeDatabaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid-url"
	}
	if u.User != nil {
		username := u.User.Username()
		u.User = url.UserPassword(username, "xxxxx")
	}
	return u.String()
}

func logLevel(raw string) slog.Level {
	switch raw {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
