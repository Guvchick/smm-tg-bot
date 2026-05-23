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
	HTTPAddr       string
	PublicBaseURL  string
	TelegramToken  string
	AdminIDs       map[int64]bool
	AdminGroupID   int64
	BackupGroupID  int64
	DatabaseURL    string
	RedisAddr      string
	RedisPassword  string
	RedisDB        int
	SocRocketAPIURL string
	SocRocketAPIKey string
	DefaultMarkup   float64
	PlategaMerchant string
	PlategaSecret   string
	PallyToken      string
	PallyShopID     string
	HeleketPayKey   string
	HeleketMerchant string
	CryptoBotToken  string
	CryptoBotBase   string
	ReferralPercent float64
	OrderSyncEvery  time.Duration
	BackupEvery     time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:          env("APP_ENV", "dev"),
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
		PublicBaseURL:  strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"),
		TelegramToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		AdminIDs:       parseIDs(os.Getenv("ADMIN_IDS")),
		AdminGroupID:   parseInt64(os.Getenv("ADMIN_GROUP_ID")),
		BackupGroupID:  parseInt64(os.Getenv("BACKUP_GROUP_ID")),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		RedisAddr:      env("REDIS_ADDR", "localhost:6379"),
		RedisPassword:  os.Getenv("REDIS_PASSWORD"),
		RedisDB:        int(parseInt64(env("REDIS_DB", "0"))),
		SocRocketAPIURL: env("SOC_ROCKET_API_URL", "https://soc-rocket.ru/api.php"),
		SocRocketAPIKey: os.Getenv("SOC_ROCKET_API_KEY"),
		DefaultMarkup:   parseFloat(env("DEFAULT_MARKUP_PERCENT", "25")),
		PlategaMerchant: os.Getenv("PLATEGA_MERCHANT_ID"),
		PlategaSecret:   os.Getenv("PLATEGA_SECRET"),
		PallyToken:      os.Getenv("PALLY_TOKEN"),
		PallyShopID:     os.Getenv("PALLY_SHOP_ID"),
		HeleketPayKey:   os.Getenv("HELEKET_PAYMENT_KEY"),
		HeleketMerchant: os.Getenv("HELEKET_MERCHANT_ID"),
		CryptoBotToken:  os.Getenv("CRYPTOBOT_TOKEN"),
		CryptoBotBase:   strings.TrimRight(env("CRYPTOBOT_BASE_URL", "https://pay.crypt.bot"), "/"),
		ReferralPercent: parseFloat(env("REFERRAL_PERCENT", "5")),
		OrderSyncEvery:  parseDuration(env("ORDER_SYNC_INTERVAL", "60s")),
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
	return cfg, nil
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

func parseDuration(raw string) time.Duration {
	v, err := time.ParseDuration(raw)
	if err != nil {
		return time.Minute
	}
	return v
}
