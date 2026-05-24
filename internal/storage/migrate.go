package storage

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, schemaSQL)
	return err
}

const schemaSQL = `
create table if not exists users (
	id bigserial primary key,
	telegram_id bigint unique not null,
	username text not null default '',
	first_name text not null default '',
	language text not null default 'ru',
	balance_cents bigint not null default 0,
	bonus_cents bigint not null default 0,
	referral_code text unique not null,
	referred_by bigint references users(id),
	is_blocked boolean not null default false,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create table if not exists services (
	id bigint primary key,
	name text not null,
	category text not null,
	rate numeric(14,6) not null,
	min_qty bigint not null,
	max_qty bigint not null,
	social text not null,
	type text not null,
	refill boolean not null default false,
	cancel boolean not null default false,
	markup_percent numeric(8,2),
	enabled boolean not null default true,
	raw jsonb not null default '{}'::jsonb,
	updated_at timestamptz not null default now()
);

create table if not exists orders (
	id bigserial primary key,
	user_id bigint not null references users(id),
	soc_order_id text not null default '',
	service_id bigint not null,
	link text not null,
	quantity bigint not null,
	charge_cents bigint not null,
	status text not null default 'created',
	provider_status text not null default '',
	remains text not null default '',
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create table if not exists transactions (
	id uuid primary key,
	user_id bigint not null references users(id),
	provider text not null,
	provider_id text not null default '',
	amount_cents bigint not null,
	currency text not null default 'RUB',
	status text not null default 'created',
	pay_url text not null default '',
	payload jsonb not null default '{}'::jsonb,
	created_at timestamptz not null default now(),
	updated_at timestamptz not null default now()
);

create unique index if not exists transactions_provider_id_unique
on transactions(provider, provider_id)
where provider_id <> '';

create index if not exists orders_active_status_idx
on orders(updated_at)
where soc_order_id <> '' and status not in ('completed','canceled','partial');

create index if not exists orders_user_id_id_idx
on orders(user_id, id desc);

create index if not exists transactions_user_created_idx
on transactions(user_id, created_at desc);

create index if not exists transactions_created_idx
on transactions(created_at desc);

create index if not exists users_created_idx
on users(created_at desc);

create table if not exists promos (
	code text primary key,
	bonus_percent numeric(8,2) not null default 0,
	bonus_cents bigint not null default 0,
	uses_left bigint not null default 0,
	min_deposit_cents bigint not null default 0,
	expires_at timestamptz,
	created_at timestamptz not null default now()
);

create table if not exists promo_uses (
	code text not null references promos(code),
	user_id bigint not null references users(id),
	created_at timestamptz not null default now(),
	primary key(code, user_id)
);

create table if not exists info_pages (
	lang text not null,
	slug text not null,
	title text not null,
	body text not null,
	updated_at timestamptz not null default now(),
	primary key(lang, slug)
);

create table if not exists menu_assets (
	menu_key text primary key,
	kind text not null check(kind in ('photo','sticker')),
	file_id text not null,
	updated_at timestamptz not null default now()
);

create table if not exists broadcasts (
	id bigserial primary key,
	admin_id bigint not null,
	text text not null,
	sent_count bigint not null default 0,
	created_at timestamptz not null default now()
);

insert into info_pages(lang, slug, title, body) values
('ru','rules','Правила сервиса','Здесь будут правила сервиса. Админ может изменить текст командой /setinfo ru rules Новый текст.'),
('ru','privacy','Политика конфиденциальности','Здесь будет политика конфиденциальности.'),
('ru','offer','Публичная оферта','Здесь будет публичная оферта.'),
('en','rules','Service rules','Service rules will be here.'),
('en','privacy','Privacy policy','Privacy policy will be here.'),
('en','offer','Public offer','Public offer will be here.'),
('uk','rules','Правила сервісу','Тут будуть правила сервісу.'),
('uk','privacy','Політика конфіденційності','Тут буде політика конфіденційності.'),
('uk','offer','Публічна оферта','Тут буде публічна оферта.')
on conflict do nothing;
`
