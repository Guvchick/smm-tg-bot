package domain

import "time"

type Language string

const (
	LangRU Language = "ru"
	LangEN Language = "en"
	LangUK Language = "uk"
)

type User struct {
	ID           int64
	TelegramID   int64
	Username     string
	FirstName    string
	Language     Language
	BalanceCents int64
	BonusCents   int64
	ReferralCode string
	ReferredBy   *int64
	IsBlocked    bool
	CreatedAt    time.Time
}

type Service struct {
	ID       int64
	Name     string
	Category string
	Rate     float64
	Min      int64
	Max      int64
	Social   string
	Type     string
	Refill   bool
	Cancel   bool
	Markup   float64
	Enabled  bool
}

type Order struct {
	ID             int64
	UserID         int64
	TelegramID     int64
	SocOrderID     string
	ServiceID      int64
	Link           string
	Quantity       int64
	ChargeCents    int64
	Status         string
	ProviderStatus string
	Remains        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Transaction struct {
	ID          string
	UserID      int64
	Provider    string
	ProviderID  string
	AmountCents int64
	Currency    string
	Status      string
	PayURL      string
	CreatedAt   time.Time
}

type Promo struct {
	Code             string
	BonusPercent     float64
	BonusCents       int64
	UsesLeft         int64
	MinDepositCents  int64
	ExpiresAt        *time.Time
}

type MenuAsset struct {
	MenuKey string
	Kind    string
	FileID  string
}
