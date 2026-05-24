package storage

import (
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type UserModel struct {
	ID           int64 `gorm:"primaryKey"`
	TelegramID   int64 `gorm:"uniqueIndex;not null"`
	Username     string
	FirstName    string
	Language     string
	BalanceCents int64
	BonusCents   int64
	ReferralCode string
	ReferredBy   *int64
	IsBlocked    bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (UserModel) TableName() string { return "users" }

type OrderModel struct {
	ID             int64 `gorm:"primaryKey"`
	UserID         int64
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

func (OrderModel) TableName() string { return "orders" }

type TransactionModel struct {
	ID          string `gorm:"type:uuid;primaryKey"`
	UserID      int64
	Provider    string
	ProviderID  string
	AmountCents int64
	Currency    string
	Status      string
	PayURL      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (TransactionModel) TableName() string { return "transactions" }

type ServiceModel struct {
	ID            int64 `gorm:"primaryKey"`
	Name          string
	Category      string
	Rate          float64
	MinQty        int64
	MaxQty        int64
	Social        string
	Type          string
	Refill        bool
	Cancel        bool
	MarkupPercent *float64
	Enabled       bool
	UpdatedAt     time.Time
}

func (ServiceModel) TableName() string { return "services" }

type ORM struct {
	db *gorm.DB
}

func OpenORM(databaseURL string) (*ORM, error) {
	db, err := gorm.Open(postgres.Open(databaseURL), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return &ORM{db: db}, nil
}

func (o *ORM) SetPoolLimits(maxOpen, maxIdle int) error {
	sqlDB, err := o.db.DB()
	if err != nil {
		return err
	}
	if maxOpen < 1 {
		maxOpen = 1
	}
	if maxIdle < 0 {
		maxIdle = 0
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxIdleTime(10 * time.Minute)
	sqlDB.SetConnMaxLifetime(time.Hour)
	return nil
}

func (o *ORM) LatestUsers(limit int) ([]UserModel, error) {
	var users []UserModel
	err := o.db.Order("created_at desc").Limit(limit).Find(&users).Error
	return users, err
}

func (o *ORM) LatestTransactions(limit int) ([]TransactionModel, error) {
	var txs []TransactionModel
	err := o.db.Order("created_at desc").Limit(limit).Find(&txs).Error
	return txs, err
}
