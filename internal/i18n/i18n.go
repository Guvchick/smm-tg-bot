package i18n

import "smm-tg-bot/internal/domain"

var text = map[domain.Language]map[string]string{
	domain.LangRU: {
		"main":       "Главное меню",
		"profile":    "👤 Профиль",
		"order":      "🛒 Обычный заказ",
		"mass_order": "📦 Массовый заказ",
		"topup":      "💳 Пополнить",
		"info":       "ℹ️ Инфо",
		"ref":        "🤝 Рефералы",
		"lang":       "🌐 Язык",
		"admin":      "🛠 Админ",
		"back":       "⬅️ Назад",
	},
	domain.LangEN: {
		"main":       "Main menu",
		"profile":    "👤 Profile",
		"order":      "🛒 Single order",
		"mass_order": "📦 Mass order",
		"topup":      "💳 Top up",
		"info":       "ℹ️ Info",
		"ref":        "🤝 Referrals",
		"lang":       "🌐 Language",
		"admin":      "🛠 Admin",
		"back":       "⬅️ Back",
	},
	domain.LangUK: {
		"main":       "Головне меню",
		"profile":    "👤 Профіль",
		"order":      "🛒 Звичайне замовлення",
		"mass_order": "📦 Масове замовлення",
		"topup":      "💳 Поповнити",
		"info":       "ℹ️ Інфо",
		"ref":        "🤝 Реферали",
		"lang":       "🌐 Мова",
		"admin":      "🛠 Адмін",
		"back":       "⬅️ Назад",
	},
}

func T(lang domain.Language, key string) string {
	if lang == "" {
		lang = domain.LangRU
	}
	if v := text[lang][key]; v != "" {
		return v
	}
	if v := text[domain.LangRU][key]; v != "" {
		return v
	}
	return key
}
