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
		b.removeReplyKeyboard(msg.Chat.ID)
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
	case data == "menu:main":
		b.editMain(ctx, msg.Chat.ID, msg.MessageID, u)
	case data == "menu:profile":
		b.editProfile(ctx, msg.Chat.ID, msg.MessageID, u)
	case data == "menu:order":
		b.editOrderCategories(ctx, msg.Chat.ID, msg.MessageID, 0)
	case data == "menu:mass":
		b.startMassOrder(ctx, msg.Chat.ID, cb.From.ID)
	case data == "menu:topup":
		b.editTopup(msg.Chat.ID, msg.MessageID)
	case data == "menu:info":
		b.editInfoMenu(msg.Chat.ID, msg.MessageID)
	case data == "menu:ref":
		text := fmt.Sprintf("🤝 Ваша реферальная ссылка:\nhttps://t.me/%s?start=ref_%d", b.api.Self.UserName, u.TelegramID)
		kb := backKeyboard()
		b.edit(msg.Chat.ID, msg.MessageID, text, &kb)
	case data == "menu:lang":
		kb := langKeyboard()
		b.edit(msg.Chat.ID, msg.MessageID, "Выберите язык:", &kb)
	case data == "menu:admin":
		if b.service.IsAdmin(cb.From.ID) {
			b.editAdmin(msg.Chat.ID, msg.MessageID)
		}
	case strings.HasPrefix(data, "order:cat:"):
		page, _ := strconv.Atoi(strings.TrimPrefix(data, "order:cat:"))
		b.editOrderCategories(ctx, msg.Chat.ID, msg.MessageID, page)
	case strings.HasPrefix(data, "order:pick:"):
		parts := strings.Split(data, ":")
		if len(parts) == 4 {
			catPage, _ := strconv.Atoi(parts[2])
			catIndex, _ := strconv.Atoi(parts[3])
			b.editCategoryServices(ctx, msg.Chat.ID, msg.MessageID, catPage, catIndex, 0)
		}
	case strings.HasPrefix(data, "order:svcpage:"):
		parts := strings.Split(data, ":")
		if len(parts) == 5 {
			catPage, _ := strconv.Atoi(parts[2])
			catIndex, _ := strconv.Atoi(parts[3])
			svcPage, _ := strconv.Atoi(parts[4])
			b.editCategoryServices(ctx, msg.Chat.ID, msg.MessageID, catPage, catIndex, svcPage)
		}
	case strings.HasPrefix(data, "order:svc:"):
		serviceID, _ := strconv.ParseInt(strings.TrimPrefix(data, "order:svc:"), 10, 64)
		b.selectService(ctx, msg.Chat.ID, msg.MessageID, cb.From.ID, serviceID)
	case strings.HasPrefix(data, "lang:"):
		lang := domain.Language(strings.TrimPrefix(data, "lang:"))
		_ = b.service.Store.SetLanguage(ctx, cb.From.ID, lang)
		u.Language = lang
		b.editMain(ctx, msg.Chat.ID, msg.MessageID, u)
	case strings.HasPrefix(data, "info:"):
		slug := strings.TrimPrefix(data, "info:")
		title, body, err := b.service.Store.InfoPage(ctx, u.Language, slug)
		if err != nil {
			kb := infoKeyboard()
			b.edit(msg.Chat.ID, msg.MessageID, "Раздел пока пуст.", &kb)
			return
		}
		kb := infoKeyboard()
		b.edit(msg.Chat.ID, msg.MessageID, "<b>"+esc(title)+"</b>\n\n"+esc(body), &kb)
	case strings.HasPrefix(data, "pay:"):
		provider := strings.TrimPrefix(data, "pay:")
		if !b.service.Cfg.PaymentEnabled(provider) {
			kb := topupKeyboard(b.service.Cfg.PaymentEnabled)
			b.edit(msg.Chat.ID, msg.MessageID, "Эта платежная система сейчас отключена.", &kb)
			return
		}
		kb := backKeyboard()
		b.edit(msg.Chat.ID, msg.MessageID, "Введите сумму пополнения в RUB, например 500", &kb)
		_ = b.service.SaveDraft(ctx, cb.From.ID, app.OrderDraft{Mode: "topup", Step: provider})
	case strings.HasPrefix(data, "paycheck:"):
		txID := strings.TrimPrefix(data, "paycheck:")
		tx, err := b.service.CheckPayment(ctx, txID, cb.From.ID)
		if err != nil {
			kb := backKeyboard()
			b.edit(msg.Chat.ID, msg.MessageID, "Не удалось проверить оплату: "+esc(err.Error()), &kb)
			return
		}
		text := fmt.Sprintf("💳 Оплата %s\nСтатус: %s\nСумма: %s", tx.Provider, humanPaymentStatus(tx.Status), storage.FormatMoney(tx.AmountCents))
		kb := backKeyboard()
		b.edit(msg.Chat.ID, msg.MessageID, text, &kb)
	case data == "admin:stats":
		if b.service.IsAdmin(cb.From.ID) {
			b.editStats(ctx, msg.Chat.ID, msg.MessageID)
		}
	case data == "admin:users":
		if b.service.IsAdmin(cb.From.ID) {
			b.editUsers(ctx, msg.Chat.ID, msg.MessageID)
		}
	case data == "admin:payments":
		if b.service.IsAdmin(cb.From.ID) {
			b.editPayments(ctx, msg.Chat.ID, msg.MessageID)
		}
	case data == "admin:sync":
		if b.service.IsAdmin(cb.From.ID) {
			n, err := b.service.SyncServices(ctx)
			text := fmt.Sprintf("✅ Услуги обновлены: %d", n)
			if err != nil {
				text = "Ошибка синхронизации: " + esc(err.Error())
			}
			kb := adminKeyboard()
			b.edit(msg.Chat.ID, msg.MessageID, text, &kb)
		}
	case data == "admin:backup":
		if b.service.IsAdmin(cb.From.ID) {
			err := b.service.SendBackup(ctx)
			text := "✅ Бэкап отправлен"
			if err != nil {
				text = "Ошибка бэкапа: " + esc(err.Error())
			}
			kb := adminKeyboard()
			b.edit(msg.Chat.ID, msg.MessageID, text, &kb)
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
		rows := [][]tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonURL("💳 Оплатить", tx.PayURL)),
		}
		if tx.Provider == "cryptobot" {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔎 Проверить оплату", "paycheck:"+tx.ID)))
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Меню", "menu:main")))
		kb := tgbotapi.NewInlineKeyboardMarkup(rows...)
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
	kb := mainKeyboard(u.Language, b.service.IsAdmin(u.TelegramID))
	b.reply(chatID, i18n.T(u.Language, "main"), kb)
}

func (b *Bot) editMain(ctx context.Context, chatID int64, messageID int, u domain.User) {
	kb := mainKeyboard(u.Language, b.service.IsAdmin(u.TelegramID))
	b.edit(chatID, messageID, i18n.T(u.Language, "main"), &kb)
}

func (b *Bot) showProfile(ctx context.Context, chatID int64, u domain.User) {
	text, kb := b.profileView(ctx, u)
	b.reply(chatID, text, kb)
}

func (b *Bot) editProfile(ctx context.Context, chatID int64, messageID int, u domain.User) {
	text, kb := b.profileView(ctx, u)
	b.edit(chatID, messageID, text, &kb)
}

func (b *Bot) profileView(ctx context.Context, u domain.User) (string, tgbotapi.InlineKeyboardMarkup) {
	orders, _ := b.service.Store.ListUserOrders(ctx, u.ID, 5)
	txs, _ := b.service.Store.UserTransactions(ctx, u.ID, 5)
	waiting, _ := b.service.Store.UserWaitingTransactions(ctx, u.ID, 5)
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
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, tx := range waiting {
		if tx.Provider == "cryptobot" && tx.ProviderID != "" {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔎 Проверить CryptoBot "+storage.FormatMoney(tx.AmountCents), "paycheck:"+tx.ID)))
		}
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "menu:main")))
	return sb.String(), tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func (b *Bot) startSingleOrder(ctx context.Context, chatID, tgID int64) {
	b.showOrderCategories(ctx, chatID, 0)
}

func (b *Bot) startMassOrder(ctx context.Context, chatID, tgID int64) {
	_ = b.service.SaveDraft(ctx, tgID, app.OrderDraft{Mode: "mass", Step: "lines"})
	b.reply(chatID, "📦 Отправьте строки массового заказа:\nSERVICE_ID LINK QUANTITY\n\nПример:\n14 https://example.com/post 100\n18 https://example.com/post2 250", nil)
}

func (b *Bot) showTopup(chatID int64) {
	kb := topupKeyboard(b.service.Cfg.PaymentEnabled)
	if len(kb.InlineKeyboard) == 1 {
		b.reply(chatID, "Пополнение временно отключено.", nil)
		return
	}
	b.reply(chatID, "Выберите платежную систему:", kb)
}

func (b *Bot) editTopup(chatID int64, messageID int) {
	kb := topupKeyboard(b.service.Cfg.PaymentEnabled)
	if len(kb.InlineKeyboard) == 1 {
		b.edit(chatID, messageID, "Пополнение временно отключено.", &kb)
		return
	}
	b.edit(chatID, messageID, "Выберите платежную систему:", &kb)
}

func (b *Bot) showInfoMenu(chatID int64) {
	kb := infoKeyboard()
	b.reply(chatID, "ℹ️ Информация:", kb)
}

func (b *Bot) editInfoMenu(chatID int64, messageID int) {
	kb := infoKeyboard()
	b.edit(chatID, messageID, "ℹ️ Информация:", &kb)
}

func (b *Bot) showAdmin(chatID int64) {
	kb := adminKeyboard()
	b.reply(chatID, "🛠 Админ-панель", kb)
}

func (b *Bot) editAdmin(chatID int64, messageID int) {
	kb := adminKeyboard()
	b.edit(chatID, messageID, "🛠 Админ-панель", &kb)
}

func (b *Bot) showStats(ctx context.Context, chatID int64) {
	text := b.statsText(ctx)
	b.reply(chatID, text, nil)
}

func (b *Bot) editStats(ctx context.Context, chatID int64, messageID int) {
	kb := adminKeyboard()
	b.edit(chatID, messageID, b.statsText(ctx), &kb)
}

func (b *Bot) statsText(ctx context.Context) string {
	stats, err := b.service.Store.Stats(ctx)
	if err != nil {
		return "Ошибка статистики: " + esc(err.Error())
	}
	return fmt.Sprintf("📊 Статистика\nПользователи: %d\nЗаказы: %d\nОплат: %d\nОборот: %s", stats["users"], stats["orders"], stats["paid_transactions"], storage.FormatMoney(stats["revenue_cents"]))
}

func (b *Bot) showUsers(ctx context.Context, chatID int64) {
	text := b.usersText(ctx)
	b.reply(chatID, text, nil)
}

func (b *Bot) editUsers(ctx context.Context, chatID int64, messageID int) {
	kb := adminKeyboard()
	b.edit(chatID, messageID, b.usersText(ctx), &kb)
}

func (b *Bot) usersText(ctx context.Context) string {
	users, err := b.service.Store.LatestUsers(ctx, 15)
	if err != nil {
		return "Ошибка: " + esc(err.Error())
	}
	var sb strings.Builder
	sb.WriteString("👥 Последние пользователи:\n")
	for _, u := range users {
		sb.WriteString(fmt.Sprintf("%d @%s %s баланс %s\n", u.TelegramID, u.Username, u.FirstName, storage.FormatMoney(u.BalanceCents)))
	}
	return sb.String()
}

func (b *Bot) showPayments(ctx context.Context, chatID int64) {
	text := b.paymentsText(ctx)
	kb := paymentsKeyboard(ctx, b.service.Store)
	b.reply(chatID, text, kb)
}

func (b *Bot) editPayments(ctx context.Context, chatID int64, messageID int) {
	kb := paymentsKeyboard(ctx, b.service.Store)
	b.edit(chatID, messageID, b.paymentsText(ctx), &kb)
}

func (b *Bot) paymentsText(ctx context.Context) string {
	txs, err := b.service.Store.LatestTransactions(ctx, 15)
	if err != nil {
		return "Ошибка: " + esc(err.Error())
	}
	var sb strings.Builder
	sb.WriteString("💳 Последние оплаты:\n")
	for _, tx := range txs {
		sb.WriteString(fmt.Sprintf("%s %s user:%d %s\n", tx.Provider, tx.Status, tx.UserID, storage.FormatMoney(tx.AmountCents)))
	}
	return sb.String()
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

func (b *Bot) showOrderCategories(ctx context.Context, chatID int64, page int) {
	text, kb := b.orderCategoriesView(ctx, page)
	b.reply(chatID, text, kb)
}

func (b *Bot) editOrderCategories(ctx context.Context, chatID int64, messageID int, page int) {
	text, kb := b.orderCategoriesView(ctx, page)
	b.edit(chatID, messageID, text, &kb)
}

func (b *Bot) orderCategoriesView(ctx context.Context, page int) (string, tgbotapi.InlineKeyboardMarkup) {
	const perPage = 8
	if page < 0 {
		page = 0
	}
	categories, err := b.service.Store.ListCategories(ctx, perPage, page*perPage)
	if err != nil {
		kb := backKeyboard()
		return "Не удалось загрузить категории: " + esc(err.Error()), kb
	}
	total, _ := b.service.Store.CountCategories(ctx)
	var rows [][]tgbotapi.InlineKeyboardButton
	for i, category := range categories {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("▫️ "+short(category, 48), fmt.Sprintf("order:pick:%d:%d", page, i))))
	}
	var nav []tgbotapi.InlineKeyboardButton
	if page > 0 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("⬅️", fmt.Sprintf("order:cat:%d", page-1)))
	}
	if (page+1)*perPage < total {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("➡️", fmt.Sprintf("order:cat:%d", page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Меню", "menu:main")))
	if len(categories) == 0 {
		return "🛒 Услуги пока не загружены. Админ может нажать «Синхронизировать услуги» в админ-панели.", tgbotapi.NewInlineKeyboardMarkup(rows...)
	}
	return fmt.Sprintf("🛒 Выберите категорию\nСтраница %d", page+1), tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func (b *Bot) editCategoryServices(ctx context.Context, chatID int64, messageID int, catPage, catIndex, svcPage int) {
	text, kb := b.categoryServicesView(ctx, catPage, catIndex, svcPage)
	b.edit(chatID, messageID, text, &kb)
}

func (b *Bot) categoryServicesView(ctx context.Context, catPage, catIndex, svcPage int) (string, tgbotapi.InlineKeyboardMarkup) {
	const catPerPage = 8
	const svcPerPage = 7
	categories, err := b.service.Store.ListCategories(ctx, catPerPage, catPage*catPerPage)
	if err != nil || catIndex < 0 || catIndex >= len(categories) {
		kb := backKeyboard()
		if err != nil {
			return "Не удалось загрузить категорию: " + esc(err.Error()), kb
		}
		return "Категория не найдена.", kb
	}
	category := categories[catIndex]
	services, err := b.service.Store.ListServicesByCategory(ctx, category, svcPerPage, svcPage*svcPerPage)
	if err != nil {
		kb := backKeyboard()
		return "Не удалось загрузить услуги: " + esc(err.Error()), kb
	}
	total, _ := b.service.Store.CountServicesByCategory(ctx, category)
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, svc := range services {
		label := fmt.Sprintf("▫️ %s | %d-%d", short(svc.Name, 38), svc.Min, svc.Max)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("order:svc:%d", svc.ID))))
	}
	var nav []tgbotapi.InlineKeyboardButton
	if svcPage > 0 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("⬅️", fmt.Sprintf("order:svcpage:%d:%d:%d", catPage, catIndex, svcPage-1)))
	}
	if (svcPage+1)*svcPerPage < total {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("➡️", fmt.Sprintf("order:svcpage:%d:%d:%d", catPage, catIndex, svcPage+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Категории", fmt.Sprintf("order:cat:%d", catPage))))
	return fmt.Sprintf("🛒 <b>%s</b>\nВыберите услугу", esc(category)), tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func (b *Bot) selectService(ctx context.Context, chatID int64, messageID int, tgID int64, serviceID int64) {
	svc, err := b.service.Store.GetService(ctx, serviceID)
	if err != nil {
		kb := backKeyboard()
		b.edit(chatID, messageID, "Услуга не найдена: "+esc(err.Error()), &kb)
		return
	}
	_ = b.service.SaveDraft(ctx, tgID, app.OrderDraft{Mode: "single", Step: "link", ServiceID: serviceID, Extras: map[string]string{}})
	kb := backKeyboard()
	text := fmt.Sprintf("🛒 <b>%s</b>\nID: %d\nМин: %d\nМакс: %d\n\nОтправьте ссылку для заказа.", esc(svc.Name), svc.ID, svc.Min, svc.Max)
	b.edit(chatID, messageID, text, &kb)
}

func (b *Bot) reply(chatID int64, text string, markup any) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	_, _ = b.api.Send(msg)
}

func (b *Bot) edit(chatID int64, messageID int, text string, markup *tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	msg.ParseMode = "HTML"
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	_, _ = b.api.Send(msg)
}

func (b *Bot) removeReplyKeyboard(chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "Меню обновлено.")
	msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
	_, _ = b.api.Send(msg)
}

func (b *Bot) requireAdmin(msg *tgbotapi.Message) bool {
	if b.service.IsAdmin(msg.From.ID) {
		return true
	}
	b.reply(msg.Chat.ID, "Недостаточно прав.", nil)
	return false
}

func mainKeyboard(lang domain.Language, admin bool) tgbotapi.InlineKeyboardMarkup {
	rows := [][]tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(i18n.T(lang, "order"), "menu:order"), tgbotapi.NewInlineKeyboardButtonData(i18n.T(lang, "mass_order"), "menu:mass")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(i18n.T(lang, "profile"), "menu:profile"), tgbotapi.NewInlineKeyboardButtonData(i18n.T(lang, "topup"), "menu:topup")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(i18n.T(lang, "info"), "menu:info"), tgbotapi.NewInlineKeyboardButtonData(i18n.T(lang, "ref"), "menu:ref"), tgbotapi.NewInlineKeyboardButtonData(i18n.T(lang, "lang"), "menu:lang")),
	}
	if admin {
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(i18n.T(lang, "admin"), "menu:admin")))
	}
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func langKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🇷🇺 Русский", "lang:ru"),
			tgbotapi.NewInlineKeyboardButtonData("🇬🇧 English", "lang:en"),
			tgbotapi.NewInlineKeyboardButtonData("🇺🇦 Українська", "lang:uk"),
		),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "menu:main")),
	)
}

func topupKeyboard(enabled func(string) bool) tgbotapi.InlineKeyboardMarkup {
	var buttons []tgbotapi.InlineKeyboardButton
	if enabled("platega") {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData("💳 Platega", "pay:platega"))
	}
	if enabled("pally") {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData("💎 Pally", "pay:pally"))
	}
	if enabled("heleket") {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData("🪙 Heleket", "pay:heleket"))
	}
	if enabled("cryptobot") {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData("🤖 CryptoBot", "pay:cryptobot"))
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for i := 0; i < len(buttons); i += 2 {
		end := i + 2
		if end > len(buttons) {
			end = len(buttons)
		}
		rows = append(rows, buttons[i:end])
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "menu:main")))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func infoKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("📜 Правила", "info:rules")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔐 Политика", "info:privacy")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🧾 Оферта", "info:offer")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "menu:main")),
	)
}

func adminKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("📊 Статистика", "admin:stats"), tgbotapi.NewInlineKeyboardButtonData("👥 Юзеры", "admin:users")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("💳 Оплаты", "admin:payments"), tgbotapi.NewInlineKeyboardButtonData("🔄 Синхронизировать услуги", "admin:sync")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🗄 Бэкап", "admin:backup")),
		tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Меню", "menu:main")),
	)
}

func backKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Назад", "menu:main")))
}

func paymentsKeyboard(ctx context.Context, store *storage.Store) tgbotapi.InlineKeyboardMarkup {
	txs, _ := store.LatestTransactions(ctx, 10)
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, tx := range txs {
		if tx.Provider == "cryptobot" && tx.ProviderID != "" && tx.Status != "paid" {
			rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("🔎 Проверить "+storage.FormatMoney(tx.AmountCents), "paycheck:"+tx.ID)))
		}
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("⬅️ Админка", "menu:admin")))
	return tgbotapi.NewInlineKeyboardMarkup(rows...)
}

func short(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

func humanPaymentStatus(status string) string {
	switch status {
	case "paid":
		return "✅ оплачено и зачислено"
	case "waiting", "created", "pending":
		return "⏳ ожидает оплаты"
	case "failed":
		return "❌ не оплачено"
	default:
		return status
	}
}

func esc(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(s)
}
