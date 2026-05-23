package app

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	"smm-tg-bot/internal/smm"
	"smm-tg-bot/internal/storage"
)

type Service struct {
	Cfg      config.Config
	Store    *storage.Store
	Redis    *redis.Client
	SMM      *smm.Client
	Payments *payments.Hub
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

func NewService(cfg config.Config, store *storage.Store, rdb *redis.Client, smmClient *smm.Client, paymentHub *payments.Hub, bot *tgbotapi.BotAPI, logger *slog.Logger) *Service {
	return &Service{Cfg: cfg, Store: store, Redis: rdb, SMM: smmClient, Payments: paymentHub, Bot: bot, Log: logger}
}

func (s *Service) IsAdmin(tgID int64) bool {
	return s.Cfg.AdminIDs[tgID]
}

func (s *Service) EnsureUser(ctx context.Context, tgUser *tgbotapi.User, lang domain.Language, refTelegramID *int64) (domain.User, error) {
	if lang == "" {
		lang = domain.LangRU
	}
	return s.Store.UpsertUser(ctx, tgUser.ID, tgUser.UserName, tgUser.FirstName, lang, refTelegramID)
}

func (s *Service) SyncServices(ctx context.Context) (int, error) {
	services, err := s.SMM.Services(ctx)
	if err != nil {
		return 0, err
	}
	return len(services), s.Store.UpsertServices(ctx, services)
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
	price, err := s.PriceCents(ctx, serviceID, quantity)
	if err != nil {
		return domain.Order{}, err
	}
	if err := s.Store.ChargeUser(ctx, u.ID, price); err != nil {
		return domain.Order{}, err
	}
	socID, err := s.SMM.AddOrder(ctx, serviceID, link, quantity, extras)
	if err != nil {
		_ = s.Store.AddBalance(context.Background(), u.ID, price, false)
		return domain.Order{}, err
	}
	order, err := s.Store.CreateOrder(ctx, domain.Order{
		UserID: u.ID, SocOrderID: socID, ServiceID: serviceID, Link: link, Quantity: quantity,
		ChargeCents: price, Status: "created", ProviderStatus: "Pending",
	})
	if err != nil {
		return order, err
	}
	s.NotifyAdmins(fmt.Sprintf("🆕 Новый заказ #%d\nПользователь: %d\nSocRocket: %s\nУслуга: %d\nКол-во: %d\nСумма: %s", order.ID, u.TelegramID, socID, serviceID, quantity, storage.FormatMoney(price)))
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
	id := uuid.NewString()
	tx := domain.Transaction{ID: id, UserID: u.ID, Provider: provider, AmountCents: amountCents, Currency: "RUB", Status: "created"}
	if err := s.Store.CreateTransaction(ctx, tx); err != nil {
		return tx, err
	}
	invoice, err := s.Payments.CreateInvoice(ctx, provider, payments.InvoiceRequest{
		ID: id, AmountCents: amountCents, Currency: "RUB", Description: "SMM bot balance",
		CallbackURL: s.Cfg.PublicBaseURL + "/webhooks/" + provider,
	})
	if err != nil {
		_, _ = s.Store.UpdateTransaction(ctx, id, "", "failed", "", []byte(`{}`))
		return tx, err
	}
	tx.ProviderID = invoice.ProviderID
	tx.PayURL = invoice.PayURL
	tx.Status = "waiting"
	raw, _ := json.Marshal(invoice)
	_, err = s.Store.UpdateTransaction(ctx, id, invoice.ProviderID, "waiting", invoice.PayURL, raw)
	return tx, err
}

func (s *Service) HandlePaymentEvent(ctx context.Context, event payments.WebhookEvent) error {
	raw := event.Raw
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	prev, err := s.Store.GetTransaction(ctx, event.LocalID)
	if err != nil {
		return err
	}
	tx, err := s.Store.UpdateTransaction(ctx, event.LocalID, event.ProviderID, event.Status, "", raw)
	if err != nil {
		return err
	}
	if event.Status == "paid" && prev.Status != "paid" {
		if err := s.Store.AddBalance(ctx, tx.UserID, tx.AmountCents, false); err != nil {
			return err
		}
		if code := s.pendingPromo(ctx, tx.UserID); code != "" {
			if bonus, err := s.Store.ApplyPromo(ctx, tx.UserID, code, tx.AmountCents); err == nil && bonus > 0 {
				s.NotifyAdmins(fmt.Sprintf("🎁 Промокод %s начислил %s", code, storage.FormatMoney(bonus)))
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
		s.NotifyAdmins(fmt.Sprintf("💳 Оплата: %s\nПровайдер: %s\nТранзакция: %s", storage.FormatMoney(tx.AmountCents), event.Provider, event.LocalID))
	}
	return nil
}

func (s *Service) RunOrderSync(ctx context.Context) {
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
	if s.Cfg.BackupGroupID == 0 {
		return
	}
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
	doc := tgbotapi.NewDocument(s.Cfg.BackupGroupID, tgbotapi.FilePath(zipPath))
	doc.Caption = "🗄 Автобэкап PostgreSQL " + storage.NowString()
	_, err := s.Bot.Send(doc)
	return err
}

func (s *Service) Send(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	_, _ = s.Bot.Send(msg)
}

func (s *Service) NotifyAdmins(text string) {
	if s.Cfg.AdminGroupID != 0 {
		s.Send(s.Cfg.AdminGroupID, text)
		return
	}
	for id := range s.Cfg.AdminIDs {
		s.Send(id, text)
	}
}

func (s *Service) Broadcast(ctx context.Context, adminID int64, text string) (int, error) {
	ids, err := s.Store.AllUserTelegramIDs(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, id := range ids {
		msg := tgbotapi.NewMessage(id, text)
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
