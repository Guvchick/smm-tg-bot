package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"smm-tg-bot/internal/domain"
)

type Store struct {
	db  *pgxpool.Pool
	orm *ORM
}

func New(db *pgxpool.Pool, orm *ORM) *Store { return &Store{db: db, orm: orm} }

func (s *Store) UpsertUser(ctx context.Context, tgID int64, username, firstName string, lang domain.Language, referredBy *int64) (domain.User, error) {
	code := fmt.Sprintf("u%d", tgID)
	row := s.db.QueryRow(ctx, `
insert into users(telegram_id, username, first_name, language, referral_code, referred_by)
values($1,$2,$3,$4,$5,(select id from users where telegram_id=$6))
on conflict(telegram_id) do update set username=excluded.username, first_name=excluded.first_name, updated_at=now()
returning id, telegram_id, username, first_name, language, balance_cents, bonus_cents, referral_code, referred_by, is_blocked, created_at
`, tgID, username, firstName, lang, code, referredBy)
	return scanUser(row)
}

func (s *Store) GetUserByTelegram(ctx context.Context, tgID int64) (domain.User, error) {
	row := s.db.QueryRow(ctx, `select id, telegram_id, username, first_name, language, balance_cents, bonus_cents, referral_code, referred_by, is_blocked, created_at from users where telegram_id=$1`, tgID)
	return scanUser(row)
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (domain.User, error) {
	row := s.db.QueryRow(ctx, `select id, telegram_id, username, first_name, language, balance_cents, bonus_cents, referral_code, referred_by, is_blocked, created_at from users where id=$1`, id)
	return scanUser(row)
}

func (s *Store) SetLanguage(ctx context.Context, tgID int64, lang domain.Language) error {
	_, err := s.db.Exec(ctx, `update users set language=$2, updated_at=now() where telegram_id=$1`, tgID, lang)
	return err
}

func (s *Store) AddBalance(ctx context.Context, userID int64, cents int64, bonus bool) error {
	field := "balance_cents"
	if bonus {
		field = "bonus_cents"
	}
	_, err := s.db.Exec(ctx, fmt.Sprintf(`update users set %s=%s+$2, updated_at=now() where id=$1`, field, field), userID, cents)
	return err
}

func (s *Store) ChargeUser(ctx context.Context, userID, cents int64) error {
	ct, err := s.db.Exec(ctx, `update users set balance_cents=balance_cents-$2, updated_at=now() where id=$1 and balance_cents >= $2`, userID, cents)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("insufficient balance")
	}
	return nil
}

func (s *Store) UpsertServices(ctx context.Context, services []domain.Service) error {
	batch := &pgx.Batch{}
	for _, svc := range services {
		raw, _ := json.Marshal(svc)
		batch.Queue(`
insert into services(id,name,category,rate,min_qty,max_qty,social,type,refill,cancel,raw,updated_at)
values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
on conflict(id) do update set name=excluded.name, category=excluded.category, rate=excluded.rate, min_qty=excluded.min_qty, max_qty=excluded.max_qty, social=excluded.social, type=excluded.type, refill=excluded.refill, cancel=excluded.cancel, raw=excluded.raw, updated_at=now()
`, svc.ID, svc.Name, svc.Category, svc.Rate, svc.Min, svc.Max, svc.Social, svc.Type, svc.Refill, svc.Cancel, raw)
	}
	br := s.db.SendBatch(ctx, batch)
	return br.Close()
}

func (s *Store) ListServices(ctx context.Context, limit int) ([]domain.Service, error) {
	rows, err := s.db.Query(ctx, `select id,name,category,rate,min_qty,max_qty,social,type,refill,cancel,coalesce(markup_percent,0),enabled from services where enabled=true order by category,name limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Service
	for rows.Next() {
		var svc domain.Service
		if err := rows.Scan(&svc.ID, &svc.Name, &svc.Category, &svc.Rate, &svc.Min, &svc.Max, &svc.Social, &svc.Type, &svc.Refill, &svc.Cancel, &svc.Markup, &svc.Enabled); err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, rows.Err()
}

func (s *Store) ListCategories(ctx context.Context, limit, offset int) ([]string, error) {
	rows, err := s.db.Query(ctx, `select category from services where enabled=true group by category order by category limit $1 offset $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			return nil, err
		}
		out = append(out, category)
	}
	return out, rows.Err()
}

func (s *Store) CountCategories(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `select count(*) from (select category from services where enabled=true group by category) c`).Scan(&count)
	return count, err
}

func (s *Store) ListServicesByCategory(ctx context.Context, category string, limit, offset int) ([]domain.Service, error) {
	rows, err := s.db.Query(ctx, `select id,name,category,rate,min_qty,max_qty,social,type,refill,cancel,coalesce(markup_percent,0),enabled from services where enabled=true and category=$1 order by name limit $2 offset $3`, category, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Service
	for rows.Next() {
		var svc domain.Service
		if err := rows.Scan(&svc.ID, &svc.Name, &svc.Category, &svc.Rate, &svc.Min, &svc.Max, &svc.Social, &svc.Type, &svc.Refill, &svc.Cancel, &svc.Markup, &svc.Enabled); err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, rows.Err()
}

func (s *Store) CountServicesByCategory(ctx context.Context, category string) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `select count(*) from services where enabled=true and category=$1`, category).Scan(&count)
	return count, err
}

func (s *Store) GetService(ctx context.Context, id int64) (domain.Service, error) {
	row := s.db.QueryRow(ctx, `select id,name,category,rate,min_qty,max_qty,social,type,refill,cancel,coalesce(markup_percent,0),enabled from services where id=$1`, id)
	var svc domain.Service
	err := row.Scan(&svc.ID, &svc.Name, &svc.Category, &svc.Rate, &svc.Min, &svc.Max, &svc.Social, &svc.Type, &svc.Refill, &svc.Cancel, &svc.Markup, &svc.Enabled)
	return svc, err
}

func (s *Store) SetServiceMarkup(ctx context.Context, serviceID int64, percent float64) error {
	_, err := s.db.Exec(ctx, `update services set markup_percent=$2 where id=$1`, serviceID, percent)
	return err
}

func (s *Store) CreateOrder(ctx context.Context, o domain.Order) (domain.Order, error) {
	row := s.db.QueryRow(ctx, `
insert into orders(user_id,soc_order_id,service_id,link,quantity,charge_cents,status,provider_status)
values($1,$2,$3,$4,$5,$6,$7,$8)
returning id, created_at, updated_at
`, o.UserID, o.SocOrderID, o.ServiceID, o.Link, o.Quantity, o.ChargeCents, o.Status, o.ProviderStatus)
	err := row.Scan(&o.ID, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func (s *Store) ListUserOrders(ctx context.Context, userID int64, limit int) ([]domain.Order, error) {
	rows, err := s.db.Query(ctx, `select id,user_id,soc_order_id,service_id,link,quantity,charge_cents,status,provider_status,remains,created_at,updated_at from orders where user_id=$1 order by id desc limit $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOrders(rows)
}

func (s *Store) PendingOrders(ctx context.Context, limit int) ([]domain.Order, error) {
	rows, err := s.db.Query(ctx, `select o.id,o.user_id,o.soc_order_id,o.service_id,o.link,o.quantity,o.charge_cents,o.status,o.provider_status,o.remains,o.created_at,o.updated_at,u.telegram_id from orders o join users u on u.id=o.user_id where o.soc_order_id<>'' and o.status not in ('completed','canceled','partial') order by o.updated_at asc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Order
	for rows.Next() {
		var o domain.Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.SocOrderID, &o.ServiceID, &o.Link, &o.Quantity, &o.ChargeCents, &o.Status, &o.ProviderStatus, &o.Remains, &o.CreatedAt, &o.UpdatedAt, &o.TelegramID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) UpdateOrderStatus(ctx context.Context, id int64, status, providerStatus, remains string) error {
	_, err := s.db.Exec(ctx, `update orders set status=$2, provider_status=$3, remains=$4, updated_at=now() where id=$1`, id, status, providerStatus, remains)
	return err
}

func (s *Store) CreateTransaction(ctx context.Context, tx domain.Transaction) error {
	_, err := s.db.Exec(ctx, `insert into transactions(id,user_id,provider,provider_id,amount_cents,currency,status,pay_url) values($1,$2,$3,$4,$5,$6,$7,$8)`, tx.ID, tx.UserID, tx.Provider, tx.ProviderID, tx.AmountCents, tx.Currency, tx.Status, tx.PayURL)
	return err
}

func (s *Store) UpdateTransaction(ctx context.Context, id, providerID, status, payURL string, payload []byte) (domain.Transaction, error) {
	row := s.db.QueryRow(ctx, `
update transactions set provider_id=coalesce(nullif($2,''),provider_id), status=$3, pay_url=coalesce(nullif($4,''),pay_url), payload=jsonb_build_object('raw',$5::text), updated_at=now()
where id=$1
returning id,user_id,provider,provider_id,amount_cents,currency,status,pay_url,created_at
`, id, providerID, status, payURL, string(payload))
	var tx domain.Transaction
	return tx, row.Scan(&tx.ID, &tx.UserID, &tx.Provider, &tx.ProviderID, &tx.AmountCents, &tx.Currency, &tx.Status, &tx.PayURL, &tx.CreatedAt)
}

func (s *Store) GetTransaction(ctx context.Context, id string) (domain.Transaction, error) {
	row := s.db.QueryRow(ctx, `select id,user_id,provider,provider_id,amount_cents,currency,status,pay_url,created_at from transactions where id=$1`, id)
	var tx domain.Transaction
	return tx, row.Scan(&tx.ID, &tx.UserID, &tx.Provider, &tx.ProviderID, &tx.AmountCents, &tx.Currency, &tx.Status, &tx.PayURL, &tx.CreatedAt)
}

func (s *Store) UserTransactions(ctx context.Context, userID int64, limit int) ([]domain.Transaction, error) {
	rows, err := s.db.Query(ctx, `select id,user_id,provider,provider_id,amount_cents,currency,status,pay_url,created_at from transactions where user_id=$1 order by created_at desc limit $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Transaction
	for rows.Next() {
		var tx domain.Transaction
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.Provider, &tx.ProviderID, &tx.AmountCents, &tx.Currency, &tx.Status, &tx.PayURL, &tx.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (s *Store) UserWaitingTransactions(ctx context.Context, userID int64, limit int) ([]domain.Transaction, error) {
	rows, err := s.db.Query(ctx, `select id,user_id,provider,provider_id,amount_cents,currency,status,pay_url,created_at from transactions where user_id=$1 and status in ('created','waiting','pending') order by created_at desc limit $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Transaction
	for rows.Next() {
		var tx domain.Transaction
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.Provider, &tx.ProviderID, &tx.AmountCents, &tx.Currency, &tx.Status, &tx.PayURL, &tx.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (s *Store) WaitingTransactionsByProvider(ctx context.Context, provider string, limit int) ([]domain.Transaction, error) {
	if limit < 1 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `select id,user_id,provider,provider_id,amount_cents,currency,status,pay_url,created_at from transactions where provider=$1 and status in ('created','waiting','pending') and coalesce(provider_id,'')<>'' order by created_at asc limit $2`, provider, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Transaction
	for rows.Next() {
		var tx domain.Transaction
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.Provider, &tx.ProviderID, &tx.AmountCents, &tx.Currency, &tx.Status, &tx.PayURL, &tx.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (s *Store) LatestTransactions(ctx context.Context, limit int) ([]domain.Transaction, error) {
	if s.orm != nil {
		models, err := s.orm.LatestTransactions(limit)
		if err != nil {
			return nil, err
		}
		out := make([]domain.Transaction, 0, len(models))
		for _, tx := range models {
			out = append(out, domain.Transaction{
				ID: tx.ID, UserID: tx.UserID, Provider: tx.Provider, ProviderID: tx.ProviderID,
				AmountCents: tx.AmountCents, Currency: tx.Currency, Status: tx.Status, PayURL: tx.PayURL,
				CreatedAt: tx.CreatedAt,
			})
		}
		return out, nil
	}
	rows, err := s.db.Query(ctx, `select id,user_id,provider,provider_id,amount_cents,currency,status,pay_url,created_at from transactions order by created_at desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Transaction
	for rows.Next() {
		var tx domain.Transaction
		if err := rows.Scan(&tx.ID, &tx.UserID, &tx.Provider, &tx.ProviderID, &tx.AmountCents, &tx.Currency, &tx.Status, &tx.PayURL, &tx.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, tx)
	}
	return out, rows.Err()
}

func (s *Store) LatestUsers(ctx context.Context, limit int) ([]domain.User, error) {
	if s.orm != nil {
		models, err := s.orm.LatestUsers(limit)
		if err != nil {
			return nil, err
		}
		out := make([]domain.User, 0, len(models))
		for _, u := range models {
			out = append(out, domain.User{
				ID: u.ID, TelegramID: u.TelegramID, Username: u.Username, FirstName: u.FirstName,
				Language: domain.Language(u.Language), BalanceCents: u.BalanceCents, BonusCents: u.BonusCents,
				ReferralCode: u.ReferralCode, ReferredBy: u.ReferredBy, IsBlocked: u.IsBlocked, CreatedAt: u.CreatedAt,
			})
		}
		return out, nil
	}
	rows, err := s.db.Query(ctx, `select id, telegram_id, username, first_name, language, balance_cents, bonus_cents, referral_code, referred_by, is_blocked, created_at from users order by created_at desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.User
	for rows.Next() {
		var u domain.User
		if err := rows.Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.Language, &u.BalanceCents, &u.BonusCents, &u.ReferralCode, &u.ReferredBy, &u.IsBlocked, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) InfoPage(ctx context.Context, lang domain.Language, slug string) (string, string, error) {
	row := s.db.QueryRow(ctx, `select title, body from info_pages where lang=$1 and slug=$2`, lang, slug)
	var title, body string
	err := row.Scan(&title, &body)
	return title, body, err
}

func (s *Store) SetInfoPage(ctx context.Context, lang, slug, body string) error {
	_, err := s.db.Exec(ctx, `insert into info_pages(lang,slug,title,body) values($1,$2,$2,$3) on conflict(lang,slug) do update set body=excluded.body, updated_at=now()`, lang, slug, body)
	return err
}

func (s *Store) SetMenuAsset(ctx context.Context, menuKey, kind, fileID string) error {
	_, err := s.db.Exec(ctx, `insert into menu_assets(menu_key,kind,file_id) values($1,$2,$3) on conflict(menu_key) do update set kind=excluded.kind, file_id=excluded.file_id, updated_at=now()`, menuKey, kind, fileID)
	return err
}

func (s *Store) MenuAsset(ctx context.Context, menuKey string) (domain.MenuAsset, error) {
	row := s.db.QueryRow(ctx, `select menu_key,kind,file_id from menu_assets where menu_key=$1`, menuKey)
	var a domain.MenuAsset
	return a, row.Scan(&a.MenuKey, &a.Kind, &a.FileID)
}

func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	row := s.db.QueryRow(ctx, `
select
	(select count(*) from users),
	(select count(*) from orders),
	(select coalesce(sum(charge_cents),0) from orders),
	(select count(*) from transactions where status='paid')
`)
	var users, orders, revenue, paid int64
	if err := row.Scan(&users, &orders, &revenue, &paid); err != nil {
		return nil, err
	}
	return map[string]int64{"users": users, "orders": orders, "revenue_cents": revenue, "paid_transactions": paid}, nil
}

func (s *Store) AllUserTelegramIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Query(ctx, `select telegram_id from users where is_blocked=false`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) ApplyPromo(ctx context.Context, userID int64, code string, depositCents int64) (int64, error) {
	var bonusCents int64
	err := s.db.QueryRow(ctx, `
with p as (
	select * from promos where code=$1 and uses_left<>0 and (expires_at is null or expires_at>now()) and min_deposit_cents <= $3
), ins as (
	insert into promo_uses(code,user_id) select code,$2 from p on conflict do nothing returning code
), upd as (
	update promos set uses_left=case when uses_left>0 then uses_left-1 else uses_left end where code in (select code from ins)
	returning bonus_cents, bonus_percent
)
select coalesce(max(bonus_cents + round($3 * bonus_percent / 100.0)::bigint),0) from upd
`, code, userID, depositCents).Scan(&bonusCents)
	if err != nil {
		return 0, err
	}
	if bonusCents > 0 {
		return bonusCents, s.AddBalance(ctx, userID, bonusCents, true)
	}
	return 0, nil
}

func (s *Store) CreatePromo(ctx context.Context, code string, bonusPercent float64, bonusCents, usesLeft, minDepositCents int64) error {
	_, err := s.db.Exec(ctx, `
insert into promos(code,bonus_percent,bonus_cents,uses_left,min_deposit_cents)
values(upper($1),$2,$3,$4,$5)
on conflict(code) do update set bonus_percent=excluded.bonus_percent, bonus_cents=excluded.bonus_cents, uses_left=excluded.uses_left, min_deposit_cents=excluded.min_deposit_cents
`, code, bonusPercent, bonusCents, usesLeft, minDepositCents)
	return err
}

func (s *Store) ReferralParent(ctx context.Context, userID int64) (domain.User, error) {
	row := s.db.QueryRow(ctx, `
select p.id, p.telegram_id, p.username, p.first_name, p.language, p.balance_cents, p.bonus_cents, p.referral_code, p.referred_by, p.is_blocked, p.created_at
from users u join users p on p.id=u.referred_by where u.id=$1
`, userID)
	return scanUser(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (domain.User, error) {
	var u domain.User
	err := row.Scan(&u.ID, &u.TelegramID, &u.Username, &u.FirstName, &u.Language, &u.BalanceCents, &u.BonusCents, &u.ReferralCode, &u.ReferredBy, &u.IsBlocked, &u.CreatedAt)
	return u, err
}

func scanOrders(rows pgx.Rows) ([]domain.Order, error) {
	var out []domain.Order
	for rows.Next() {
		var o domain.Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.SocOrderID, &o.ServiceID, &o.Link, &o.Quantity, &o.ChargeCents, &o.Status, &o.ProviderStatus, &o.Remains, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func FormatMoney(cents int64) string {
	return fmt.Sprintf("%.2f RUB", float64(cents)/100)
}

func NowString() string {
	return time.Now().Format("2006-01-02 15:04:05")
}
