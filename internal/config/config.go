package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv          string
	LogLevel        string
	HTTPAddr       string
	PublicBaseURL  string
	TelegramToken  string
	AdminIDs       map[int64]bool
	AdminGroupID   int64
	BackupGroupID  int64
	DatabaseURL    string
	PostgresMaxConns int32
	PostgresMinConns int32
	RedisAddr      string
	RedisPassword  string
	RedisDB        int
	SocRocketAPIURL string
	SocRocketAPIKey string
	DefaultMarkup   float64
	PlategaEnabled  bool
	PlategaMerchant string
	PlategaSecret   string
	PlategaAPIURL   string
	PallyEnabled    bool
	PallyToken      string
	PallyShopID     string
	PallyWebhookSecret string
	PallyAPIURL     string
	HeleketEnabled  bool
	HeleketPayKey   string
	HeleketMerchant string
	HeleketAPIURL   string
	CryptoBotEnabled bool
	CryptoBotToken  string
	CryptoBotBase   string
	ReferralPercent float64
	PaymentPollEnabled bool
	PaymentPollEvery   time.Duration
	OrderSyncEnabled bool
	OrderSyncEvery  time.Duration
	BackupEnabled   bool
	BackupEvery     time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:          env("APP_ENV", "dev"),
		LogLevel:        env("LOG_LEVEL", "info"),
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
		PublicBaseURL:  strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		TelegramToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		AdminIDs:       parseIDs(os.Getenv("ADMIN_IDS")),
		AdminGroupID:   parseInt64(os.Getenv("ADMIN_GROUP_ID")),
		BackupGroupID:  parseInt64(os.Getenv("BACKUP_GROUP_ID")),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		PostgresMaxConns: int32(parseInt64(env("POSTGRES_MAX_CONNS", "5"))),
		PostgresMinConns: int32(parseInt64(env("POSTGRES_MIN_CONNS", "0"))),
		RedisAddr:      env("REDIS_ADDR", "localhost:6379"),
		RedisPassword:  os.Getenv("REDIS_PASSWORD"),
		RedisDB:        int(parseInt64(env("REDIS_DB", "0"))),
		SocRocketAPIURL: env("SOC_ROCKET_API_URL", "https://soc-rocket.ru/api/v2/"),
		SocRocketAPIKey: os.Getenv("SOC_ROCKET_API_KEY"),
		DefaultMarkup:   parseFloat(env("DEFAULT_MARKUP_PERCENT", "25")),
		PlategaEnabled:  parseBool(env("PLATEGA_ENABLED", "true")),
		PlategaMerchant: os.Getenv("PLATEGA_MERCHANT_ID"),
		PlategaSecret:   os.Getenv("PLATEGA_SECRET"),
		PlategaAPIURL:   env("PLATEGA_API_URL", "https://app.platega.io/api/v1/transaction/process"),
		PallyEnabled:    parseBool(env("PALLY_ENABLED", "true")),
		PallyToken:      os.Getenv("PALLY_TOKEN"),
		PallyShopID:     os.Getenv("PALLY_SHOP_ID"),
		PallyWebhookSecret: env("PALLY_WEBHOOK_SECRET", os.Getenv("PALLY_TOKEN")),
		PallyAPIURL:     env("PALLY_API_URL", "https://pal24.pro/api/v1/bill/create"),
		HeleketEnabled:  parseBool(env("HELEKET_ENABLED", "true")),
		HeleketPayKey:   os.Getenv("HELEKET_PAYMENT_KEY"),
		HeleketMerchant: os.Getenv("HELEKET_MERCHANT_ID"),
		HeleketAPIURL:   env("HELEKET_API_URL", "https://api.heleket.com/v1/payment"),
		CryptoBotEnabled: parseBool(env("CRYPTOBOT_ENABLED", "true")),
		CryptoBotToken:  os.Getenv("CRYPTOBOT_TOKEN"),
		CryptoBotBase:   strings.TrimRight(env("CRYPTOBOT_BASE_URL", "https://pay.crypt.bot"), "/"),
		ReferralPercent: parseFloat(env("REFERRAL_PERCENT", "5")),
		PaymentPollEnabled: parseBool(env("PAYMENT_POLL_ENABLED", "true")),
		PaymentPollEvery:   parseDuration(env("PAYMENT_POLL_INTERVAL", "30s")),
		OrderSyncEnabled: parseBool(env("ORDER_SYNC_ENABLED", "true")),
		OrderSyncEvery:  parseDuration(env("ORDER_SYNC_INTERVAL", "5m")),
		BackupEnabled:   parseBool(env("BACKUP_ENABLED", "true")),
		BackupEvery:     parseDuration(env("BACKUP_INTERVAL", "24h")),
	}
	if cfg.TelegramToken == "" {
		return cfg, errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	if cfg.DatabaseURL == "" {
		return cfg, errors.New("DATABASE_URL is required")
	}
	if cfg.SocRocketAPIKey == "" {
		return cfg, errors.New("SOC_ROCKET_API_KEY is required")
	}
	if cfg.AppEnv == "prod" && !strings.HasPrefix(cfg.PublicBaseURL, "https://") {
		return cfg, errors.New("PUBLIC_BASE_URL must start with https:// in prod")
	}
	if cfg.PostgresMaxConns < 1 {
		cfg.PostgresMaxConns = 1
	}
	if cfg.PostgresMinConns < 0 {
		cfg.PostgresMinConns = 0
	}
	if cfg.PostgresMinConns > cfg.PostgresMaxConns {
		cfg.PostgresMinConns = cfg.PostgresMaxConns
	}
	cfg.PaymentPollEvery = minDuration(cfg.PaymentPollEvery, 15*time.Second)
	cfg.OrderSyncEvery = minDuration(cfg.OrderSyncEvery, time.Minute)
	cfg.BackupEvery = minDuration(cfg.BackupEvery, time.Hour)
	return cfg, nil
}

func (c Config) PaymentEnabled(provider string) bool {
	switch provider {
	case "platega":
		return c.PlategaEnabled
	case "pally":
		return c.PallyEnabled
	case "heleket":
		return c.HeleketEnabled
	case "cryptobot":
		return c.CryptoBotEnabled
	default:
		return false
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseIDs(raw string) map[int64]bool {
	out := map[int64]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			out[id] = true
		}
	}
	return out
}

func parseInt64(raw string) int64 {
	v, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return v
}

func parseFloat(raw string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return v
}

func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on", "enabled":
		return true
	default:
		return false
	}
}

func parseDuration(raw string) time.Duration {
	v, err := time.ParseDuration(raw)
	if err != nil {
		return time.Minute
	}
	return v
}

func minDuration(v, min time.Duration) time.Duration {
	if v < min {
		return min
	}
	return v
}
