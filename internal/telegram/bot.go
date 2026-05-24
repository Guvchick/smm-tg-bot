package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"smm-tg-bot/internal/app"
	"smm-tg-bot/internal/domain"
	"smm-tg-bot/internal/i18n"
	"smm-tg-bot/internal/storage"
)

type Bot struct {
	service *app.Service
	api     *tgbotapi.BotAPI
	log     *slog.Logger
}

func New(service *app.Service, api *tgbotapi.BotAPI, logger *slog.Logger) *Bot {
	return &Bot{service: service, api: api, log: logger}
}

func (b *Bot) Run(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := b.api.GetUpdatesChan(u)
	b.log.Info("telegram polling started")
	for {
		select {
		case <-ctx.Done():
			b.api.StopReceivingUpdates()
			return
		case update, ok := <-updates:
			if !ok {
				b.log.Warn("telegram updates channel closed")
				return
			}
			if update.CallbackQuery != nil {
				b.handleCallback(ctx, update.CallbackQuery)
			}
			if update.Message != nil {
				b.handleMessage(ctx, update.Message)
			}
		}
	}
}

func (b *Bot) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	if msg.From == nil {
		return
	}
	u, err := b.ensureUser(ctx, msg)
	if err != nil {
		b.reply(msg.Chat.ID, "Ошибка профиля: "+err.Error(), nil)
		return
	}
	if msg.IsCommand() {
		b.handleCommand(ctx, msg, u)
		return
	}
	if b.handleAdminAsset(ctx, msg) {
		return
	}
	if b.handleDraft(ctx, msg, u) {
		return
	}
	switch msg.Text {
	case i18n.T(u.Language, "profile"):
		b.showProfile(ctx, msg.Chat.ID, u)
	case i18n.T(u.Language, "order"):
		b.startSingleOrder(ctx, msg.Chat.ID, msg.From.ID)
	case i18n.T(u.Language, "mass_order"):
		b.startMassOrder(ctx, msg.Chat.ID, msg.From.ID)
	case i18n.T(u.Language, "topup"):
		b.showTopup(msg.Chat.ID)
	case i18n.T(u.Language, "info"):
		b.showInfoMenu(msg.Chat.ID)
	case i18n.T(u.Language, "ref"):
		b.reply(msg.Chat.ID, fmt.Sprintf("🤝 Ваша реферальная ссылка:\nhttps://t.me/%s?start=ref_%d", b.api.Self.UserName, u.TelegramID), nil)
	case i18n.T(u.Language, "lang"):
		b.reply(msg.Chat.ID, "Выберите язык:", langKeyboard())
	case i18n.T(u.Language, "admin"):
		if b.service.IsAdmin(msg.From.ID) {
			b.showAdmin(msg.Chat.ID)
		}
	default:
		b.showMain(ctx, msg.Chat.ID, u)
	}
}

func (b *Bot) handleCommand(ctx context.Context, msg *tgbotapi.Message, u domain.User) {
	args := msg.CommandArguments()
	switch msg.Command() {
	case "start":
		b.showMain(ctx, msg.Chat.ID, u)
	case "sync_services":
		if !b.requireAdmin(msg) {
			return
		}
		n, err := b.service.SyncServices(ctx)
		if err != nil {
			b.reply(msg.Chat.ID, "Ошибка синхронизации: "+err.Error(), nil)
			return
		}
		b.reply(msg.Chat.ID, fmt.Sprintf("✅ Услуги обновлены: %d", n), nil)
	case "setmarkup":
		if !b.requireAdmin(msg) {
			return
		}
		parts := strings.Fields(args)
		if len(parts) != 2 {
			b.reply(msg.Chat.ID, "Формат: /setmarkup SERVICE_ID PERCENT", nil)
			return
		}
		serviceID, _ := strconv.ParseInt(parts[0], 10, 64)
		percent, _ := strconv.ParseFloat(parts[1], 64)
		if err := b.service.Store.SetServiceMarkup(ctx, serviceID, percent); err != nil {
			b.reply(msg.Chat.ID, "Ошибка: "+err.Error(), nil)
			return
		}
		b.reply(msg.Chat.ID, "✅ Наценка обновлена", nil)
	case "setinfo":
		if !b.requireAdmin(msg) {
			return
		}
		parts := strings.SplitN(args, " ", 3)
		if len(parts) != 3 {
			b.reply(msg.Chat.ID, "Формат: /setinfo ru rules Текст", nil)
			return
		}
		if err := b.service.Store.SetInfoPage(ctx, parts[0], parts[1], parts[2]); err != nil {
			b.reply(msg.Chat.ID, "Ошибка: "+err.Error(), nil)
			return
		}
		b.reply(msg.Chat.ID, "✅ Инфо обновлено", nil)
	case "setasset":
		if !b.requireAdmin(msg) {
			return
		}
		parts := strings.Fields(args)
		if len(parts) != 3 {
			b.reply(msg.Chat.ID, "Формат: /setasset menu_key photo|sticker file_id", nil)
			return
		}
		if err := b.service.Store.SetMenuAsset(ctx, parts[0], parts[1], parts[2]); err != nil {
			b.reply(msg.Chat.ID, "Ошибка: "+err.Error(), nil)
			return
		}
		b.reply(msg.Chat.ID, "✅ Ассет меню сохранен", nil)
	case "createpromo":
		if !b.requireAdmin(msg) {
			return
		}
		parts := strings.Fields(args)
		if len(parts) < 3 {
			b.reply(msg.Chat.ID, "Формат: /createpromo CODE BONUS_PERCENT USES [MIN_RUB]", nil)
			return
		}
		percent, _ := strconv.ParseFloat(parts[1], 64)
		uses, _ := strconv.ParseInt(parts[2], 10, 64)
		var minCents int64
		if len(parts) > 3 {
			minRub, _ := strconv.ParseFloat(parts[3], 64)
			minCents = int64(minRub*100 + 0.5)
		}
		if err := b.service.Store.CreatePromo(ctx, parts[0], percent, 0, uses, minCents); err != nil {
			b.reply(msg.Chat.ID, "Ошибка: "+err.Error(), nil)
			return
		}
		b.reply(msg.Chat.ID, "✅ Промокод сохранен", nil)
	case "users":
		if !b.requireAdmin(msg) {
			return
		}
		b.showUsers(ctx, msg.Chat.ID)
	case "payments":
		if !b.requireAdmin(msg) {
			return
		}
		b.showPayments(ctx, msg.Chat.ID)
	case "broadcast":
		if !b.requireAdmin(msg) {
			return
		}
		if args == "" {
			b.reply(msg.Chat.ID, "Формат: /broadcast текст оповещения", nil)
			return
		}
		n, err := b.service.Broadcast(ctx, msg.From.ID, args)
		if err != nil {
			b.reply(msg.Chat.ID, "Ошибка: "+err.Error(), nil)
			return
		}
		b.reply(msg.Chat.ID, fmt.Sprintf("📣 Отправлено: %d", n), nil)
	case "backup":
		if !b.requireAdmin(msg) {
			return
		}
		if err := b.service.SendBackup(ctx); err != nil {
			b.reply(msg.Chat.ID, "Ошибка бэкапа: "+err.Error(), nil)
			return
		}
		b.reply(msg.Chat.ID, "✅ Бэкап отправлен", nil)
	case "promo":
		parts := strings.Fields(args)
		if len(parts) != 1 {
			b.reply(msg.Chat.ID, "Формат: /promo CODE", nil)
			return
		}
		if err := b.service.SetPendingPromo(ctx, u.ID, parts[0]); err != nil {
			b.reply(msg.Chat.ID, "Ошибка: "+err.Error(), nil)
			return
		}
		b.reply(msg.Chat.ID, "🎁 Промокод применится к следующему успешному пополнению.", nil)
	default:
		b.showMain(ctx, msg.Chat.ID, u)
	}
}

func (b *Bot) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	if cb.From == nil {
		return
	}
	msg := cb.Message
	if msg == nil {
		return
	}
	u, err := b.service.Store.GetUserByTelegram(ctx, cb.From.ID)
	if err != nil {
		return
	}
	data := cb.Data
	_, _ = b.api.Request(tgbotapi.NewCallback(cb.ID, ""))
	switch {
	case strings.HasPrefix(data, "lang:"):
		lang := domain.Language(strings.TrimPrefix(data, "lang:"))
		_ = b.service.Store.SetLanguage(ctx, cb.From.ID, lang)
		u.Language = lang
		b.showMain(ctx, msg.Chat.ID, u)
	case strings.HasPrefix(data, "info:"):
		slug := strings.TrimPrefix(data, "info:")
		title, body, err := b.service.Store.InfoPage(ctx, u.Language, slug)
		if err != nil {
			b.reply(msg.Chat.ID, "Раздел пока пуст.", nil)
			return
		}
		b.reply(msg.Chat.ID, "<b>"+esc(title)+"</b>\n\n"+esc(body), nil)
	case strings.HasPrefix(data, "pay:"):
		provider := strings.TrimPrefix(data, "pay:")
		if !b.service.Cfg.PaymentEnabled(provider) {
			b.reply(msg.Chat.ID, "Эта платежная система сейчас отключена.", nil)
			return
		}
		b.reply(msg.Chat.ID, "Введите сумму пополнения в RUB, например 500", nil)
		_ = b.service.SaveDraft(ctx, cb.From.ID, app.OrderDraft{Mode: "topup", Step: provider})
	case data == "admin:stats":
		if b.service.IsAdmin(cb.From.ID) {
			b.showStats(ctx, msg.Chat.ID)
		}
	}
}

func (b *Bot) handleDraft(ctx context.Context, msg *tgbotapi.Message, u domain.User) bool {
	draft, err := b.service.Draft(ctx, msg.From.ID)
	if err != nil {
		return false
	}
	switch draft.Mode {
	case "single":
		return b.handleSingleDraft(ctx, msg, u, draft)
	case "mass":
		lines, err := app.ParseMassLines(msg.Text)
		if err != nil {
			b.reply(msg.Chat.ID, "Формат строки: SERVICE_ID LINK QUANTITY\nМожно несколько строк.", nil)
			return true
		}
		orders, errs := b.service.SubmitMassOrder(ctx, u, lines)
		b.service.ClearDraft(ctx, msg.From.ID)
		b.reply(msg.Chat.ID, fmt.Sprintf("📦 Массовый заказ: создано %d\nОшибки: %s", len(orders), strings.Join(errs, "; ")), nil)
		return true
	case "topup":
		amount, err := strconv.ParseFloat(strings.ReplaceAll(msg.Text, ",", "."), 64)
		if err != nil || amount <= 0 {
			b.reply(msg.Chat.ID, "Введите сумму числом.", nil)
			return true
		}
		tx, err := b.service.CreateDeposit(ctx, u, draft.Step, int64(amount*100+0.5))
		b.service.ClearDraft(ctx, msg.From.ID)
		if err != nil {
			b.reply(msg.Chat.ID, "Не удалось создать оплату: "+err.Error(), nil)
			return true
		}
		kb := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonURL("💳 Оплатить", tx.PayURL)))
		b.reply(msg.Chat.ID, "Счет создан. После оплаты баланс пополнится автоматически через webhook.", kb)
		return true
	default:
		b.service.ClearDraft(ctx, msg.From.ID)
		return false
	}
}

func (b *Bot) handleSingleDraft(ctx context.Context, msg *tgbotapi.Message, u domain.User, draft app.OrderDraft) bool {
	switch draft.Step {
	case "service":
		id, err := strconv.ParseInt(strings.TrimSpace(msg.Text), 10, 64)
		if err != nil {
			b.reply(msg.Chat.ID, "Введите ID услуги числом.", nil)
			return true
		}
		draft.ServiceID = id
		draft.Step = "link"
		_ = b.service.SaveDraft(ctx, msg.From.ID, draft)
		b.reply(msg.Chat.ID, "🔗 Теперь отправьте ссылку.", nil)
	case "link":
		draft.Link = strings.TrimSpace(msg.Text)
		draft.Step = "quantity"
		_ = b.service.SaveDraft(ctx, msg.From.ID, draft)
		b.reply(msg.Chat.ID, "🔢 Укажите количество.", nil)
	case "quantity":
		qty, err := strconv.ParseInt(strings.TrimSpace(msg.Text), 10, 64)
		if err != nil {
			b.reply(msg.Chat.ID, "Количество должно быть числом.", nil)
			return true
		}
		order, err := b.service.SubmitOrder(ctx, u, draft.ServiceID, draft.Link, qty, draft.Extras)
		b.service.ClearDraft(ctx, msg.From.ID)
		if err != nil {
			b.reply(msg.Chat.ID, "Не удалось создать заказ: "+err.Error(), nil)
			return true
		}
		b.reply(msg.Chat.ID, fmt.Sprintf("✅ Заказ #%d создан\nSocRocket: %s", order.ID, order.SocOrderID), nil)
	}
	return true
}

func (b *Bot) handleAdminAsset(ctx context.Context, msg *tgbotapi.Message) bool {
	if !b.service.IsAdmin(msg.From.ID) {
		return false
	}
	if len(msg.Photo) == 0 && msg.Sticker == nil {
		return false
	}
	caption := strings.Fields(msg.Caption)
	if len(caption) != 2 || caption[0] != "/asset" {
		return false
	}
	menuKey := caption[1]
	if msg.Sticker != nil {
		_ = b.service.Store.SetMenuAsset(ctx, menuKey, "sticker", msg.Sticker.FileID)
		b.reply(msg.Chat.ID, "✅ Стикер сохранен для "+menuKey, nil)
		return true
	}
	photo := msg.Photo[len(msg.Photo)-1]
	_ = b.service.Store.SetMenuAsset(ctx, menuKey, "photo", photo.FileID)
	b.reply(msg.Chat.ID, "✅ Фото сохранено для "+menuKey, nil)
	return true
}

func (b *Bot) ensureUser(ctx context.Context, msg *tgbotapi.Message) (domain.User, error) {
	var ref *int64
	if msg.IsCommand() && msg.Command() == "start" && strings.HasPrefix(msg.CommandArguments(), "ref_") {
		id, err := strconv.ParseInt(strings.TrimPrefix(msg.CommandArguments(), "ref_"), 10, 64)
		if err == nil && id != msg.From.ID {
			ref = &id
		}
	}
	return b.service.EnsureUser(ctx, msg.From, domain.LangRU, ref)
}

func (b *Bot) showMain(ctx context.Context, chatID int64, u domain.User) {
	b.sendAsset(ctx, chatID, "main")
	b.reply(chatID, i18n.T(u.Language, "main"), mainKeyboard(u.Language, b.service.IsAdmin(u.TelegramID)))
}

func (b *Bot) showProfile(ctx context.Context, chatID int64, u domain.User) {
	orders, _ := b.service.Store.ListUserOrders(ctx, u.ID, 5)
	txs, _ := b.service.Store.UserTransactions(ctx, u.ID, 5)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("👤 <b>Профиль</b>\nID: %d\nБаланс: %s\nБонусы: %s\n\n", u.TelegramID, storage.FormatMoney(u.BalanceCents), storage.FormatMoney(u.BonusCents)))
	sb.WriteString("🧾 Последние заказы:\n")
	if len(orders) == 0 {
		sb.WriteString("пока нет\n")
	}
	for _, o := range orders {
		sb.WriteString(fmt.Sprintf("#%d %s %s\n", o.ID, o.Status, storage.FormatMoney(o.ChargeCents)))
	}
	sb.WriteString("\n💳 Транзакции:\n")
	if len(txs) == 0 {
		sb.WriteString("пока нет\n")
	}
	for _, tx := range txs {
		sb.WriteString(fmt.Sprintf("%s %s %s\n", tx.Provider, tx.Status, storage.FormatMoney(tx.AmountCents)))
	}
	b.reply(chatID, sb.String(), nil)
}

func (b *Bot) startSingleOrder(ctx context.Context, chatID, tgID int64) {
	services, err := b.service.Store.ListServices(ctx, 10)
	var hint strings.Builder
	hint.WriteString("🛒 Введите ID услуги.\n\nПервые услуги:\n")
	if err == nil {
		for _, svc := range services {
			hint.WriteString(fmt.Sprintf("%d — %s (%d-%d)\n", svc.ID, svc.Name, svc.Min, svc.Max))
		}
	}
	_ = b.service.SaveDraft(ctx, tgID, app.OrderDraft{Mode: "single", Step: "service", Extras: map[string]string{}})
	b.reply(chatID, hint.String(), nil)
}

func (b *Bot) startMassOrder(ctx context.Context, chatID, tgID int64) {
	_ = b.service.SaveDraft(ctx, tgID, app.OrderDraft{Mode: "mass", Step: "lines"})
	b.reply(chatID, "📦 Отправьте строки массового заказа:\nSERVICE_ID LINK QUANTITY\n\nПример:\n14 https://example.com/post 100\n18 https://example.com/post2 250", nil)
}

func (b *Bot) showTopup(chatID int64) {
	var buttons []tgbotapi.InlineKeyboardButton
	if b.service.Cfg.PaymentEnabled("platega") {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData("💳 Platega", "pay:platega"))
	}
	if b.service.Cfg.PaymentEnabled("pally") {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData("💎 Pally", "pay:pally"))
	}
	if b.service.Cfg.PaymentEnabled("heleket") {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData("🪙 Heleket", "pay:heleket"))
	}
	if b.service.Cfg.PaymentEnabled("cryptobot") {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData("🤖 CryptoBot", "pay:cryptobot"))
	}
	if len(buttons) == 0 {
		b.reply(chatID, "Пополнение временно отключено.", nil)
		return
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for i := 0; i < len(buttons); i += 2 {
		end := i + 2
		if end > len(buttons) {
			end = len(buttons)
		}
		rows = append(rows, buttons[i:end])
	}
	kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
	b.reply(chatID, "Выберите платежную систему:", kb)
}

func (b *Bot) showInfoMenu(chatID int64) {
	kb := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("📜 Правила", "info:rules")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔐 Политика", "info:privacy")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🧾 Оферта", "info:offer")),
	)
	b.reply(chatID, "ℹ️ Информация:", kb)
}

func (b *Bot) showAdmin(chatID int64) {
	kb := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("📊 Статистика", "admin:stats")))
	b.reply(chatID, "🛠 Админ-панель\n\nКоманды:\n/sync_services\n/users\n/payments\n/setmarkup SERVICE_ID PERCENT\n/createpromo CODE BONUS_PERCENT USES [MIN_RUB]\n/setinfo ru rules TEXT\n/setasset main photo FILE_ID\n/broadcast TEXT\n/backup", kb)
}

func (b *Bot) showStats(ctx context.Context, chatID int64) {
	stats, err := b.service.Store.Stats(ctx)
	if err != nil {
		b.reply(chatID, "Ошибка статистики: "+err.Error(), nil)
		return
	}
	b.reply(chatID, fmt.Sprintf("📊 Статистика\nПользователи: %d\nЗаказы: %d\nОплат: %d\nОборот: %s", stats["users"], stats["orders"], stats["paid_transactions"], storage.FormatMoney(stats["revenue_cents"])), nil)
}

func (b *Bot) showUsers(ctx context.Context, chatID int64) {
	users, err := b.service.Store.LatestUsers(ctx, 15)
	if err != nil {
		b.reply(chatID, "Ошибка: "+err.Error(), nil)
		return
	}
	var sb strings.Builder
	sb.WriteString("👥 Последние пользователи:\n")
	for _, u := range users {
		sb.WriteString(fmt.Sprintf("%d @%s %s баланс %s\n", u.TelegramID, u.Username, u.FirstName, storage.FormatMoney(u.BalanceCents)))
	}
	b.reply(chatID, sb.String(), nil)
}

func (b *Bot) showPayments(ctx context.Context, chatID int64) {
	txs, err := b.service.Store.LatestTransactions(ctx, 15)
	if err != nil {
		b.reply(chatID, "Ошибка: "+err.Error(), nil)
		return
	}
	var sb strings.Builder
	sb.WriteString("💳 Последние оплаты:\n")
	for _, tx := range txs {
		sb.WriteString(fmt.Sprintf("%s %s user:%d %s\n", tx.Provider, tx.Status, tx.UserID, storage.FormatMoney(tx.AmountCents)))
	}
	b.reply(chatID, sb.String(), nil)
}

func (b *Bot) sendAsset(ctx context.Context, chatID int64, menuKey string) {
	asset, err := b.service.Store.MenuAsset(ctx, menuKey)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, redis.Nil) {
			b.log.Debug("menu asset", "error", err)
		}
		return
	}
	if asset.Kind == "sticker" {
		_, _ = b.api.Send(tgbotapi.NewSticker(chatID, tgbotapi.FileID(asset.FileID)))
		return
	}
	_, _ = b.api.Send(tgbotapi.NewPhoto(chatID, tgbotapi.FileID(asset.FileID)))
}

func (b *Bot) reply(chatID int64, text string, markup any) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	_, _ = b.api.Send(msg)
}

func (b *Bot) requireAdmin(msg *tgbotapi.Message) bool {
	if b.service.IsAdmin(msg.From.ID) {
		return true
	}
	b.reply(msg.Chat.ID, "Недостаточно прав.", nil)
	return false
}

func mainKeyboard(lang domain.Language, admin bool) tgbotapi.ReplyKeyboardMarkup {
	rows := [][]tgbotapi.KeyboardButton{
		{tgbotapi.NewKeyboardButton(i18n.T(lang, "order")), tgbotapi.NewKeyboardButton(i18n.T(lang, "mass_order"))},
		{tgbotapi.NewKeyboardButton(i18n.T(lang, "profile")), tgbotapi.NewKeyboardButton(i18n.T(lang, "topup"))},
		{tgbotapi.NewKeyboardButton(i18n.T(lang, "info")), tgbotapi.NewKeyboardButton(i18n.T(lang, "ref")), tgbotapi.NewKeyboardButton(i18n.T(lang, "lang"))},
	}
	if admin {
		rows = append(rows, []tgbotapi.KeyboardButton{tgbotapi.NewKeyboardButton(i18n.T(lang, "admin"))})
	}
	kb := tgbotapi.NewReplyKeyboard(rows...)
	kb.ResizeKeyboard = true
	return kb
}

func langKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🇷🇺 Русский", "lang:ru"),
			tgbotapi.NewInlineKeyboardButtonData("🇬🇧 English", "lang:en"),
			tgbotapi.NewInlineKeyboardButtonData("🇺🇦 Українська", "lang:uk"),
		),
	)
}

func esc(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(s)
}
