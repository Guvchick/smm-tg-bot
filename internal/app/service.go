package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"smm-tg-bot/internal/config"
	"smm-tg-bot/internal/domain"
	"smm-tg-bot/internal/payments"
	"smm-tg-bot/internal/sheetslog"
	"smm-tg-bot/internal/smm"
	"smm-tg-bot/internal/storage"
)

type Service struct {
	Cfg      config.Config
	Store    *storage.Store
	Redis    *redis.Client
	SMM      *smm.Client
	Payments *payments.Hub
	Sheets   *sheetslog.Client
	Bot      *tgbotapi.BotAPI
	Log      *slog.Logger
}

type OrderDraft struct {
	Mode      string            `json:"mode"`
	Step      string            `json:"step"`
	ServiceID int64             `json:"service_id"`
	Link      string            `json:"link"`
	Quantity  int64             `json:"quantity"`
	Lines     []MassOrderLine   `json:"lines"`
	Extras    map[string]string `json:"extras"`
}

type MassOrderLine struct {
	ServiceID int64  `json:"service_id"`
	Link      string `json:"link"`
	Quantity  int64  `json:"quantity"`
}

func NewService(cfg config.Config, store *storage.Store, rdb *redis.Client, smmClient *smm.Client, paymentHub *payments.Hub, sheetsClient *sheetslog.Client, bot *tgbotapi.BotAPI, logger *slog.Logger) *Service {
	return &Service{Cfg: cfg, Store: store, Redis: rdb, SMM: smmClient, Payments: paymentHub, Sheets: sheetsClient, Bot: bot, Log: logger}
}

func (s *Service) IsAdmin(tgID int64) bool {
	return s.Cfg.AdminIDs[tgID]
}

func (s *Service) EnsureUser(ctx context.Context, tgUser *tgbotapi.User, lang domain.Language, refTelegramID *int64) (domain.User, error) {
	if lang == "" {
		lang = domain.LangRU
	}
	u, err := s.Store.UpsertUser(ctx, tgUser.ID, tgUser.UserName, tgUser.FirstName, lang, refTelegramID)
	if err != nil {
		return u, err
	}
	granted, err := s.Store.GrantWelcomeBonus(ctx, u.ID, s.Cfg.NewUserBonusCents)
	if err != nil {
		s.Log.Warn("welcome bonus failed", "telegram_id", tgUser.ID, "amount_cents", s.Cfg.NewUserBonusCents, "error", err)
		return u, nil
	}
	if granted {
		u.BalanceCents += s.Cfg.NewUserBonusCents
		s.Send(u.TelegramID, fmt.Sprintf("🎁 Стартовый бонус начислен: %s", storage.FormatMoney(s.Cfg.NewUserBonusCents)))
		s.NotifyAdminsTopic("payments", fmt.Sprintf("🎁 <b>Стартовый бонус</b>\nПользователь: %s\nСумма: %s", userLine(u.TelegramID, u.Username, u.FirstName), storage.FormatMoney(s.Cfg.NewUserBonusCents)))
	}
	return u, nil
}

func (s *Service) SyncServices(ctx context.Context) (int, error) {
	start := time.Now()
	services, err := s.SMM.Services(ctx)
	if err != nil {
		s.Log.Error("services sync failed", "error", err)
		return 0, err
	}
	err = s.Store.UpsertServices(ctx, services)
	s.Log.Info("services synced", "count", len(services), "duration_ms", time.Since(start).Milliseconds(), "error", err)
	return len(services), err
}

func (s *Service) PriceCents(ctx context.Context, serviceID, quantity int64) (int64, error) {
	svc, err := s.Store.GetService(ctx, serviceID)
	if err != nil {
		return 0, err
	}
	if quantity < svc.Min || quantity > svc.Max {
		return 0, fmt.Errorf("quantity must be from %d to %d", svc.Min, svc.Max)
	}
	markup := svc.Markup
	if markup == 0 {
		markup = s.Cfg.DefaultMarkup
	}
	base := svc.Rate * float64(quantity) / 1000.0
	return int64((base*(1+markup/100))*100 + 0.5), nil
}

func (s *Service) SubmitOrder(ctx context.Context, u domain.User, serviceID int64, link string, quantity int64, extras map[string]string) (domain.Order, error) {
	start := time.Now()
	price, err := s.PriceCents(ctx, serviceID, quantity)
	if err != nil {
		s.Log.Warn("order price failed", "telegram_id", u.TelegramID, "service_id", serviceID, "quantity", quantity, "error", err)
		return domain.Order{}, err
	}
	if err := s.Store.ChargeUser(ctx, u.ID, price); err != nil {
		s.Log.Warn("order charge failed", "telegram_id", u.TelegramID, "service_id", serviceID, "quantity", quantity, "charge_cents", price, "error", err)
		return domain.Order{}, err
	}
	chargedUser, _ := s.Store.GetUserByID(ctx, u.ID)
	balanceAfter := u.BalanceCents - price
	if chargedUser.ID != 0 {
		balanceAfter = chargedUser.BalanceCents
	}
	socID, err := s.SMM.AddOrder(ctx, serviceID, link, quantity, extras)
	if err != nil {
		_ = s.Store.AddBalance(context.Background(), u.ID, price, false)
		s.Log.Error("provider order failed", "telegram_id", u.TelegramID, "service_id", serviceID, "quantity", quantity, "charge_cents", price, "error", err)
		return domain.Order{}, err
	}
	order, err := s.Store.CreateOrder(ctx, domain.Order{
		UserID: u.ID, SocOrderID: socID, ServiceID: serviceID, Link: link, Quantity: quantity,
		ChargeCents: price, Status: "created", ProviderStatus: "Pending",
	})
	if err != nil {
		s.Log.Error("order save failed", "telegram_id", u.TelegramID, "soc_order_id", socID, "error", err)
		return order, err
	}
	s.Log.Info("order created", "order_id", order.ID, "telegram_id", u.TelegramID, "soc_order_id", socID, "service_id", serviceID, "quantity", quantity, "charge_cents", price, "duration_ms", time.Since(start).Milliseconds())
	svc, _ := s.Store.GetService(ctx, serviceID)
	s.NotifyAdminsTopic("orders", fmt.Sprintf(
		"🆕 <b>Новый заказ #%d</b>\n\n👤 Пользователь: %s\n🧩 Услуга: <code>%d</code> %s\n🔢 Количество: <b>%d</b>\n💸 Списано: <b>%s</b>\n💰 Баланс после: <b>%s</b>\n🌐 Ссылка: %s\n🚀 SocRocket: <code>%s</code>",
		order.ID, userLine(u.TelegramID, u.Username, u.FirstName), serviceID, escText(svc.Name), quantity, storage.FormatMoney(price), storage.FormatMoney(balanceAfter), escText(link), socID,
	))
	s.recordOrderInSheets(ctx, u, order, svc)
	return order, nil
}

func (s *Service) SubmitMassOrder(ctx context.Context, u domain.User, lines []MassOrderLine) ([]domain.Order, []string) {
	var orders []domain.Order
	var errs []string
	for i, line := range lines {
		order, err := s.SubmitOrder(ctx, u, line.ServiceID, line.Link, line.Quantity, nil)
		if err != nil {
			errs = append(errs, fmt.Sprintf("строка %d: %v", i+1, err))
			continue
		}
		orders = append(orders, order)
	}
	return orders, errs
}

func (s *Service) CreateDeposit(ctx context.Context, u domain.User, provider string, amountCents int64) (domain.Transaction, error) {
	start := time.Now()
	id := uuid.NewString()
	tx := domain.Transaction{ID: id, UserID: u.ID, Provider: provider, AmountCents: amountCents, Currency: "RUB", Status: "created"}
	if err := s.Store.CreateTransaction(ctx, tx); err != nil {
		s.Log.Error("deposit create transaction failed", "telegram_id", u.TelegramID, "provider", provider, "amount_cents", amountCents, "error", err)
		return tx, err
	}
	invoice, err := s.Payments.CreateInvoice(ctx, provider, payments.InvoiceRequest{
		ID: id, AmountCents: amountCents, Currency: "RUB", Description: "SMM bot balance",
		CallbackURL: s.Cfg.PublicBaseURL + "/webhooks/" + provider,
	})
	if err != nil {
		_, _ = s.Store.UpdateTransaction(ctx, id, "", "failed", "", []byte(`{}`))
		s.Log.Error("deposit invoice failed", "telegram_id", u.TelegramID, "provider", provider, "transaction_id", id, "amount_cents", amountCents, "error", err)
		return tx, err
	}
	tx.ProviderID = invoice.ProviderID
	tx.PayURL = invoice.PayURL
	tx.Status = "waiting"
	raw, _ := json.Marshal(invoice)
	updated, err := s.Store.UpdateTransaction(ctx, id, invoice.ProviderID, "waiting", invoice.PayURL, raw)
	if err == nil {
		tx = updated
	}
	s.Log.Info("deposit invoice created", "telegram_id", u.TelegramID, "provider", provider, "transaction_id", id, "provider_id", invoice.ProviderID, "amount_cents", amountCents, "duration_ms", time.Since(start).Milliseconds(), "error", err)
	if err == nil {
		s.recordDepositInSheets(ctx, u, tx, u.BalanceCents)
	}
	return tx, err
}

func (s *Service) recordOrderInSheets(ctx context.Context, u domain.User, order domain.Order, svc domain.Service) {
	if s.Sheets == nil {
		return
	}
	sheetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	if err := s.Sheets.AppendOrder(sheetCtx, u, order, svc); err != nil {
		s.Log.Warn("google sheets order append failed", "order_id", order.ID, "error", err)
	}
}

func (s *Service) recordDepositInSheets(ctx context.Context, u domain.User, tx domain.Transaction, balanceAfterCents int64) {
	if s.Sheets == nil {
		return
	}
	sheetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	if err := s.Sheets.UpsertDeposit(sheetCtx, u, tx, balanceAfterCents); err != nil {
		s.Log.Warn("google sheets deposit upsert failed", "transaction_id", tx.ID, "status", tx.Status, "error", err)
	}
}

func (s *Service) HandlePaymentEvent(ctx context.Context, event payments.WebhookEvent) error {
	start := time.Now()
	raw := event.Raw
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	prev, err := s.Store.GetTransaction(ctx, event.LocalID)
	if err != nil {
		s.Log.Warn("payment event transaction not found", "provider", event.Provider, "provider_id", event.ProviderID, "local_id", event.LocalID, "status", event.Status, "error", err)
		return err
	}
	tx, err := s.Store.UpdateTransaction(ctx, event.LocalID, event.ProviderID, event.Status, "", raw)
	if err != nil {
		s.Log.Error("payment event update failed", "provider", event.Provider, "provider_id", event.ProviderID, "local_id", event.LocalID, "status", event.Status, "error", err)
		return err
	}
	credited := event.Status == "paid" && prev.Status != "paid"
	s.Log.Info("payment event handled", "provider", event.Provider, "provider_id", event.ProviderID, "local_id", event.LocalID, "status", event.Status, "previous_status", prev.Status, "credited", credited, "amount_cents", tx.AmountCents, "duration_ms", time.Since(start).Milliseconds())
	if !credited && event.Status != prev.Status {
		u, _ := s.Store.GetUserByID(ctx, tx.UserID)
		s.recordDepositInSheets(ctx, u, tx, u.BalanceCents)
	}
	if credited {
		if err := s.Store.AddBalance(ctx, tx.UserID, tx.AmountCents, false); err != nil {
			return err
		}
		if code := s.pendingPromo(ctx, tx.UserID); code != "" {
			if bonus, err := s.Store.ApplyPromo(ctx, tx.UserID, code, tx.AmountCents); err == nil && bonus > 0 {
				s.NotifyAdminsTopic("payments", fmt.Sprintf("🎁 <b>Промокод</b>\nКод: <code>%s</code>\nБонус: <b>%s</b>", escText(code), storage.FormatMoney(bonus)))
			}
			_ = s.Redis.Del(ctx, promoKey(tx.UserID)).Err()
		}
		if s.Cfg.ReferralPercent > 0 {
			if parent, err := s.Store.ReferralParent(ctx, tx.UserID); err == nil {
				bonus := int64(float64(tx.AmountCents)*s.Cfg.ReferralPercent/100 + 0.5)
				_ = s.Store.AddBalance(ctx, parent.ID, bonus, true)
				s.Send(parent.TelegramID, fmt.Sprintf("🤝 Реферальный бонус: %s", storage.FormatMoney(bonus)))
			}
		}
		u, _ := s.Store.GetUserByID(ctx, tx.UserID)
		if u.TelegramID != 0 {
			s.Send(u.TelegramID, fmt.Sprintf("✅ Оплата зачислена: %s", storage.FormatMoney(tx.AmountCents)))
		}
		s.NotifyAdminsTopic("payments", fmt.Sprintf(
			"💳 <b>Оплата зачислена</b>\n\n👤 Пользователь: %s\n🏦 Провайдер: <b>%s</b>\n💵 Сумма: <b>%s</b>\n💰 Баланс после: <b>%s</b>\n🧾 Транзакция: <code>%s</code>\n🔗 Provider ID: <code>%s</code>",
			userLine(u.TelegramID, u.Username, u.FirstName), escText(event.Provider), storage.FormatMoney(tx.AmountCents), storage.FormatMoney(u.BalanceCents), event.LocalID, event.ProviderID,
		))
		s.recordDepositInSheets(ctx, u, tx, u.BalanceCents)
	}
	return nil
}

func (s *Service) CheckPayment(ctx context.Context, txID string, requesterTelegramID int64) (domain.Transaction, error) {
	tx, err := s.Store.GetTransaction(ctx, txID)
	if err != nil {
		return tx, err
	}
	if !s.IsAdmin(requesterTelegramID) {
		u, err := s.Store.GetUserByTelegram(ctx, requesterTelegramID)
		if err != nil {
			return tx, err
		}
		if u.ID != tx.UserID {
			return tx, fmt.Errorf("transaction belongs to another user")
		}
	}
	if tx.Provider != "cryptobot" {
		return tx, fmt.Errorf("manual check is available only for CryptoBot")
	}
	event, err := s.checkPaymentTransaction(ctx, tx)
	if err != nil {
		s.Log.Warn("manual payment check failed", "requester_telegram_id", requesterTelegramID, "provider", tx.Provider, "transaction_id", tx.ID, "provider_id", tx.ProviderID, "error", err)
		return tx, err
	}
	if err := s.HandlePaymentEvent(ctx, event); err != nil {
		return tx, err
	}
	updated, err := s.Store.GetTransaction(ctx, txID)
	s.Log.Info("manual payment check handled", "requester_telegram_id", requesterTelegramID, "provider", tx.Provider, "transaction_id", tx.ID, "provider_id", tx.ProviderID, "previous_status", tx.Status, "status", event.Status, "credited", tx.Status != "paid" && event.Status == "paid", "error", err)
	return updated, err
}

func (s *Service) checkPaymentTransaction(ctx context.Context, tx domain.Transaction) (payments.WebhookEvent, error) {
	if tx.ProviderID == "" {
		return payments.WebhookEvent{}, fmt.Errorf("transaction has no provider id")
	}
	status, err := s.Payments.CheckInvoice(ctx, tx.Provider, tx.ProviderID)
	if err != nil {
		return payments.WebhookEvent{}, err
	}
	event := payments.EventFromInvoiceStatus(tx.Provider, status)
	event.LocalID = tx.ID
	if event.ProviderID == "" {
		event.ProviderID = tx.ProviderID
	}
	if event.AmountCents == 0 {
		event.AmountCents = tx.AmountCents
	}
	if event.Currency == "" {
		event.Currency = tx.Currency
	}
	if event.Status == "" {
		event.Status = "pending"
	}
	return event, nil
}

func (s *Service) RunPaymentPoll(ctx context.Context) {
	if s.Cfg.PaymentPollEvery < 15*time.Second {
		s.Cfg.PaymentPollEvery = 15 * time.Second
	}
	s.Log.Info("payment poll started", "interval", s.Cfg.PaymentPollEvery.String(), "providers", []string{"cryptobot"})
	s.pollPaymentProvider(ctx, "cryptobot")
	ticker := time.NewTicker(s.Cfg.PaymentPollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pollPaymentProvider(ctx, "cryptobot")
		}
	}
}

func (s *Service) pollPaymentProvider(ctx context.Context, provider string) {
	start := time.Now()
	if !s.Cfg.PaymentEnabled(provider) {
		s.Log.Debug("payment poll provider disabled", "provider", provider)
		return
	}
	pending, err := s.Store.WaitingTransactionsByProvider(ctx, provider, 100)
	if err != nil {
		s.Log.Warn("payment poll load failed", "provider", provider, "error", err)
		return
	}
	if len(pending) == 0 {
		s.Log.Debug("payment poll no pending transactions", "provider", provider)
		return
	}
	var checked, paid, failed, stillPending int
	for _, tx := range pending {
		event, err := s.checkPaymentTransaction(ctx, tx)
		if err != nil {
			s.Log.Warn("payment poll invoice check failed", "provider", provider, "transaction_id", tx.ID, "provider_id", tx.ProviderID, "status", tx.Status, "error", err)
			continue
		}
		checked++
		switch event.Status {
		case "paid":
			paid++
		case "failed":
			failed++
		default:
			stillPending++
		}
		if event.Status == tx.Status {
			continue
		}
		s.Log.Info("payment poll status changed", "provider", provider, "transaction_id", tx.ID, "provider_id", tx.ProviderID, "previous_status", tx.Status, "status", event.Status)
		if err := s.HandlePaymentEvent(ctx, event); err != nil {
			s.Log.Warn("payment poll event failed", "provider", provider, "transaction_id", tx.ID, "provider_id", tx.ProviderID, "status", event.Status, "error", err)
		}
	}
	s.Log.Info("payment poll batch finished", "provider", provider, "pending", len(pending), "checked", checked, "paid", paid, "failed", failed, "still_pending", stillPending, "duration_ms", time.Since(start).Milliseconds())
}

func (s *Service) RunOrderSync(ctx context.Context) {
	if s.Cfg.OrderSyncEvery < time.Minute {
		s.Cfg.OrderSyncEvery = time.Minute
	}
	s.Log.Info("order sync started", "interval", s.Cfg.OrderSyncEvery.String())
	ticker := time.NewTicker(s.Cfg.OrderSyncEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncOrderStatuses(ctx)
		}
	}
}

func (s *Service) syncOrderStatuses(ctx context.Context) {
	orders, err := s.Store.PendingOrders(ctx, 100)
	if err != nil {
		s.Log.Warn("pending orders", "error", err)
		return
	}
	for _, order := range orders {
		st, err := s.SMM.Status(ctx, order.SocOrderID)
		if err != nil {
			s.Log.Warn("order status", "id", order.ID, "error", err)
			continue
		}
		normalized := smm.NormalizeStatus(st.Status)
		if normalized != order.Status || st.Remains != order.Remains {
			if err := s.Store.UpdateOrderStatus(ctx, order.ID, normalized, st.Status, st.Remains); err != nil {
				s.Log.Warn("update order", "error", err)
				continue
			}
			s.Send(order.TelegramID, fmt.Sprintf("📊 Заказ #%d: %s\nОсталось: %s", order.ID, humanOrderStatus(normalized), st.Remains))
		}
	}
}

func (s *Service) RunBackups(ctx context.Context) {
	if s.backupChatID() == 0 {
		return
	}
	if s.Cfg.BackupEvery < time.Hour {
		s.Cfg.BackupEvery = time.Hour
	}
	s.Log.Info("backup job started", "interval", s.Cfg.BackupEvery.String())
	ticker := time.NewTicker(s.Cfg.BackupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SendBackup(ctx); err != nil {
				s.Log.Warn("backup", "error", err)
			}
		}
	}
}

func (s *Service) SendBackup(ctx context.Context) error {
	chatID := s.backupChatID()
	if chatID == 0 {
		return fmt.Errorf("BACKUP_GROUP_ID or ADMIN_GROUP_ID is required")
	}
	tmp := filepath.Join(os.TempDir(), "smm-bot-backup-"+time.Now().Format("20060102-150405")+".sql")
	cmd := exec.CommandContext(ctx, "pg_dump", s.Cfg.DatabaseURL, "-f", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_dump: %v: %s", err, string(out))
	}
	zipPath := tmp + ".zip"
	if err := zipFile(zipPath, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	defer os.Remove(zipPath)
	if s.Cfg.AdminTopicBackupsID != 0 {
		return s.sendTelegramDocument(ctx, chatID, s.Cfg.AdminTopicBackupsID, zipPath, "🗄 Бэкап PostgreSQL "+storage.NowString())
	}
	doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(zipPath))
	doc.Caption = "🗄 Автобэкап PostgreSQL " + storage.NowString()
	_, err := s.Bot.Send(doc)
	return err
}

func (s *Service) backupChatID() int64 {
	if s.Cfg.BackupGroupID != 0 {
		return s.Cfg.BackupGroupID
	}
	return s.Cfg.AdminGroupID
}

func (s *Service) RestoreBackup(ctx context.Context, backupPath string) error {
	sqlPath := backupPath
	cleanup := ""
	if strings.HasSuffix(strings.ToLower(backupPath), ".zip") {
		extracted, err := unzipFirstSQL(backupPath)
		if err != nil {
			return err
		}
		sqlPath = extracted
		cleanup = extracted
	}
	if cleanup != "" {
		defer os.Remove(cleanup)
	}
	cmd := exec.CommandContext(ctx, "psql", s.Cfg.DatabaseURL, "-v", "ON_ERROR_STOP=1", "-f", sqlPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("psql restore: %v: %s", err, string(out))
	}
	s.NotifyAdminsTopic("backups", "♻️ <b>Бэкап восстановлен</b>\nВремя: "+storage.NowString())
	return nil
}

func (s *Service) DownloadTelegramFile(ctx context.Context, fileURL, fileName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("telegram file http %d", resp.StatusCode)
	}
	if fileName == "" {
		fileName = "backup.sql"
	}
	tmp := filepath.Join(os.TempDir(), "smm-restore-"+time.Now().Format("20060102-150405")+"-"+filepath.Base(fileName))
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

func (s *Service) Send(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	_, _ = s.Bot.Send(msg)
}

func (s *Service) NotifyAdmins(text string) {
	s.NotifyAdminsTopic("general", text)
}

func (s *Service) NotifyAdminsTopic(topic string, text string) {
	if s.Cfg.AdminGroupID != 0 {
		if err := s.sendTelegramMessage(context.Background(), s.Cfg.AdminGroupID, s.topicID(topic), text); err == nil {
			return
		}
		s.Send(s.Cfg.AdminGroupID, text)
		return
	}
	for id := range s.Cfg.AdminIDs {
		s.Send(id, text)
	}
}

func (s *Service) topicID(topic string) int {
	switch topic {
	case "orders":
		if s.Cfg.AdminTopicOrdersID != 0 {
			return s.Cfg.AdminTopicOrdersID
		}
	case "payments":
		if s.Cfg.AdminTopicPaymentsID != 0 {
			return s.Cfg.AdminTopicPaymentsID
		}
	case "backups":
		if s.Cfg.AdminTopicBackupsID != 0 {
			return s.Cfg.AdminTopicBackupsID
		}
	case "support":
		if s.Cfg.AdminTopicSupportID != 0 {
			return s.Cfg.AdminTopicSupportID
		}
	}
	return s.Cfg.AdminTopicGeneralID
}

func (s *Service) sendTelegramMessage(ctx context.Context, chatID int64, threadID int, text string) error {
	values := url.Values{
		"chat_id":    {strconv.FormatInt(chatID, 10)},
		"text":       {text},
		"parse_mode": {"HTML"},
	}
	if threadID != 0 {
		values.Set("message_thread_id", strconv.Itoa(threadID))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+s.Cfg.TelegramToken+"/sendMessage", strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendMessage http %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (s *Service) sendTelegramDocument(ctx context.Context, chatID int64, threadID int, path string, caption string) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	_ = writer.WriteField("caption", caption)
	if threadID != 0 {
		_ = writer.WriteField("message_thread_id", strconv.Itoa(threadID))
	}
	part, err := writer.CreateFormFile("document", filepath.Base(path))
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		_ = file.Close()
		return err
	}
	_ = file.Close()
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+s.Cfg.TelegramToken+"/sendDocument", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram sendDocument http %d: %s", resp.StatusCode, string(raw))
	}
	return nil
}

func (s *Service) Broadcast(ctx context.Context, adminID int64, text string) (int, error) {
	ids, err := s.Store.AllUserTelegramIDs(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, id := range ids {
		msg := tgbotapi.NewMessage(id, text)
		msg.ParseMode = "HTML"
		if _, err := s.Bot.Send(msg); err == nil {
			sent++
		}
		time.Sleep(35 * time.Millisecond)
	}
	return sent, nil
}

func (s *Service) SaveDraft(ctx context.Context, tgID int64, draft OrderDraft) error {
	raw, _ := json.Marshal(draft)
	return s.Redis.Set(ctx, draftKey(tgID), raw, 30*time.Minute).Err()
}

func (s *Service) Draft(ctx context.Context, tgID int64) (OrderDraft, error) {
	raw, err := s.Redis.Get(ctx, draftKey(tgID)).Bytes()
	if err != nil {
		return OrderDraft{}, err
	}
	var d OrderDraft
	return d, json.Unmarshal(raw, &d)
}

func (s *Service) ClearDraft(ctx context.Context, tgID int64) {
	_ = s.Redis.Del(ctx, draftKey(tgID)).Err()
}

func draftKey(tgID int64) string { return "order_draft:" + strconv.FormatInt(tgID, 10) }

func (s *Service) SetPendingPromo(ctx context.Context, userID int64, code string) error {
	return s.Redis.Set(ctx, promoKey(userID), strings.ToUpper(strings.TrimSpace(code)), 24*time.Hour).Err()
}

func (s *Service) pendingPromo(ctx context.Context, userID int64) string {
	code, _ := s.Redis.Get(ctx, promoKey(userID)).Result()
	return code
}

func promoKey(userID int64) string { return "promo:" + strconv.FormatInt(userID, 10) }

func ParseMassLines(text string) ([]MassOrderLine, error) {
	var out []MassOrderLine
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			return nil, fmt.Errorf("format: service_id link quantity")
		}
		serviceID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return nil, err
		}
		quantity, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, MassOrderLine{ServiceID: serviceID, Link: parts[1], Quantity: quantity})
	}
	return out, nil
}

func ParseMassLinesForService(serviceID int64, text string) ([]MassOrderLine, error) {
	var out []MassOrderLine
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return nil, fmt.Errorf("format: link quantity")
		}
		quantity, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, MassOrderLine{ServiceID: serviceID, Link: parts[0], Quantity: quantity})
	}
	return out, nil
}

func humanOrderStatus(status string) string {
	switch status {
	case "completed":
		return "✅ выполнен"
	case "canceled":
		return "❌ отменен"
	case "partial":
		return "⚠️ частично выполнен"
	case "in_progress":
		return "🔄 в работе"
	case "pending":
		return "⏳ ожидает запуска"
	default:
		return "📊 обновлен"
	}
}

func zipFile(dst, src string) error {
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	w, err := zw.Create(filepath.Base(src))
	if err != nil {
		return err
	}
	_, err = in.WriteTo(w)
	return err
}

func unzipFirstSQL(src string) (string, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(f.Name), ".sql") {
			continue
		}
		in, err := f.Open()
		if err != nil {
			return "", err
		}
		dst := filepath.Join(os.TempDir(), "smm-restore-"+time.Now().Format("20060102-150405")+".sql")
		out, err := os.Create(dst)
		if err != nil {
			_ = in.Close()
			return "", err
		}
		_, copyErr := io.Copy(out, in)
		_ = in.Close()
		_ = out.Close()
		if copyErr != nil {
			_ = os.Remove(dst)
			return "", copyErr
		}
		return dst, nil
	}
	return "", fmt.Errorf("zip does not contain sql backup")
}

func userLine(telegramID int64, username, firstName string) string {
	name := escText(firstName)
	if username != "" {
		name = "@" + escText(username)
	}
	if name == "" {
		name = "user"
	}
	return fmt.Sprintf("%s (<code>%d</code>)", name, telegramID)
}

func escText(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(s)
}
