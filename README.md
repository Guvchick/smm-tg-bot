# SMM Telegram Bot

Telegram-бот на Go для продажи SMM-услуг через SocRocket API с PostgreSQL, Redis, платежными webhook, админ-панелью, промокодами, рефералкой, автобэкапами и уведомлениями.

## Что уже заложено

- Обычный заказ и массовый заказ через Telegram-диалоги.
- Синхронизация услуг SocRocket: `/sync_services`.
- `docker compose` поднимает PostgreSQL, Redis и бота; Caddy включается отдельным профилем, чтобы не конфликтовать с уже занятыми 80/443.
- PostgreSQL-схема создается автоматически при старте бота.
- Redis используется для состояний диалогов и ожидающих промокодов.
- Платежные провайдеры через webhook: Platega, Pally, Heleket, CryptoBot.
- Защита от повторного начисления при повторных webhook.
- Профиль пользователя: баланс, бонусы, последние заказы и транзакции.
- Админ-команды: пользователи, проверки оплат, статистика, наценки, рассылки.
- Редактируемые страницы `rules`, `privacy`, `offer` на `ru/en/uk`.
- Фото и стикеры для меню через `menu_assets`.
- Автобэкапы PostgreSQL в Telegram-группу через `pg_dump`.
- Фоновая проверка статусов заказов и уведомления покупателю.

## Быстрый старт

1. Установить Go 1.22+, Docker, `pg_dump`.
2. Скопировать `.env.example` в `.env` и заполнить токены.
3. Запустить БД, Redis и бота:

```bash
docker compose up -d --build
```

4. Смотреть логи:

```bash
docker compose logs -f bot
```

Если запустить без `-d`, Docker Compose не зависает: он просто показывает живые логи контейнеров в текущем терминале.

Если на сервере уже заняты 80/443, как у Nextcloud AIO, не запускайте Caddy из этого compose. Бот публикуется на `127.0.0.1:18080`, а существующий SSL reverse proxy должен прокинуть домен `https://bot.example.com` на `http://127.0.0.1:18080`.

Если 80/443 свободны и хотите использовать Caddy из проекта:

```bash
docker compose --profile caddy up -d --build
docker compose logs -f caddy
```

Для локального запуска бота без Docker поменяйте в `.env` `DATABASE_URL`, `REDIS_ADDR` и `CADDY_UPSTREAM` на значения из комментариев, затем:

```bash
go mod tidy
go run ./cmd/bot
```

## Caddy

Пример reverse proxy лежит в `Caddyfile.example`.
Docker-версия Caddy лежит в `deploy/caddy`.
Для SSL webhook публичный `PUBLIC_BASE_URL` в `.env` должен начинаться с `https://`.

Webhook URL для платежек:

- `https://bot.example.com/webhooks/platega`
- `https://bot.example.com/webhooks/pally`
- `https://bot.example.com/webhooks/heleket`
- `https://bot.example.com/webhooks/cryptobot`

## Команды администратора

- `/sync_services` — загрузить услуги SocRocket.
- `/users` — последние пользователи.
- `/payments` — последние платежи.
- `/setmarkup SERVICE_ID PERCENT` — наценка на конкретную услугу.
- `/createpromo CODE BONUS_PERCENT USES [MIN_RUB]` — создать промокод.
- `/setinfo ru rules TEXT` — изменить инфо-страницу.
- `/setasset main photo FILE_ID` — привязать фото/стикер к меню.
- `/broadcast TEXT` — массовое оповещение.
- `/backup` — ручной бэкап в Telegram-группу.

Фото или стикер можно отправить с подписью `/asset main`, тогда бот сам сохранит `file_id`.

## Важные настройки

- `ADMIN_IDS` — Telegram ID админов через запятую.
- `ADMIN_GROUP_ID` — группа уведомлений о заказах и оплатах.
- `BACKUP_GROUP_ID` — группа автобэкапов.
- `DEFAULT_MARKUP_PERCENT` — базовая наценка, если у услуги нет своей.
- `REFERRAL_PERCENT` — бонус рефереру от успешного пополнения.
- `PLATEGA_ENABLED`, `PALLY_ENABLED`, `HELEKET_ENABLED`, `CRYPTOBOT_ENABLED` — включение платежек в меню и webhook.
- `CADDY_DOMAIN`, `CADDY_UPSTREAM` — домен Caddy и адрес бота внутри/снаружи Docker.
- `BOT_HOST_PORT` — локальный порт бота для существующего reverse proxy, по умолчанию `18080`.

## Примечания по платежкам

Подписи webhook вынесены в `internal/payments`. Перед продакшеном нужно сверить точные поля создания счета в личном кабинете каждого провайдера, потому что у Pally и Platega названия полей могут отличаться по типу подключения.

## Частые логи

Сообщение Redis `Memory overcommit must be enabled` не блокирует старт. На Linux-сервере его можно убрать командой:

```bash
sudo sysctl vm.overcommit_memory=1
```

Для постоянной настройки добавьте `vm.overcommit_memory = 1` в `/etc/sysctl.conf`.
