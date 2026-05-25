package sheetslog

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"

	"smm-tg-bot/internal/config"
	"smm-tg-bot/internal/domain"
)

var orderHeaders = []any{
	"order_id", "created_at", "user_id", "telegram_id", "username", "first_name",
	"service_id", "service_name", "soc_order_id", "link", "quantity", "charge_rub",
	"status", "provider_status",
}

var depositHeaders = []any{
	"transaction_id", "created_at", "user_id", "telegram_id", "username", "first_name",
	"provider", "provider_id", "amount_rub", "currency", "status", "pay_url", "balance_after_rub",
}

type Client struct {
	service       *sheets.Service
	spreadsheetID string
	ordersSheet   string
	depositsSheet string
	log           *slog.Logger
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Client, error) {
	if !cfg.GoogleSheetsEnabled {
		return nil, nil
	}
	opts := []option.ClientOption{
		option.WithScopes(sheets.SpreadsheetsScope),
	}
	if cfg.GoogleSheetsCredentialsJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(cfg.GoogleSheetsCredentialsJSON)))
	} else {
		opts = append(opts, option.WithCredentialsFile(cfg.GoogleSheetsCredentialsFile))
	}
	service, err := sheets.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	client := &Client{
		service:       service,
		spreadsheetID: cfg.GoogleSheetsSpreadsheetID,
		ordersSheet:   cfg.GoogleSheetsOrdersSheet,
		depositsSheet: cfg.GoogleSheetsDepositsSheet,
		log:           logger,
	}
	if err := client.Ensure(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

func (c *Client) Ensure(ctx context.Context) error {
	meta, err := c.service.Spreadsheets.Get(c.spreadsheetID).Context(ctx).Fields("sheets(properties(title))").Do()
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, sheet := range meta.Sheets {
		if sheet != nil && sheet.Properties != nil {
			existing[sheet.Properties.Title] = true
		}
	}
	var requests []*sheets.Request
	for _, title := range []string{c.ordersSheet, c.depositsSheet} {
		if !existing[title] {
			requests = append(requests, &sheets.Request{
				AddSheet: &sheets.AddSheetRequest{
					Properties: &sheets.SheetProperties{Title: title},
				},
			})
		}
	}
	if len(requests) > 0 {
		_, err = c.service.Spreadsheets.BatchUpdate(c.spreadsheetID, &sheets.BatchUpdateSpreadsheetRequest{Requests: requests}).Context(ctx).Do()
		if err != nil {
			return err
		}
		c.log.Info("google sheets tabs created", "count", len(requests))
	}
	if err := c.setHeaders(ctx, c.ordersSheet, orderHeaders); err != nil {
		return err
	}
	return c.setHeaders(ctx, c.depositsSheet, depositHeaders)
}

func (c *Client) AppendOrder(ctx context.Context, u domain.User, order domain.Order, svc domain.Service) error {
	row := []any{
		order.ID,
		formatTime(order.CreatedAt),
		u.ID,
		u.TelegramID,
		u.Username,
		u.FirstName,
		order.ServiceID,
		svc.Name,
		order.SocOrderID,
		order.Link,
		order.Quantity,
		money(order.ChargeCents),
		order.Status,
		order.ProviderStatus,
	}
	_, err := c.service.Spreadsheets.Values.Append(c.spreadsheetID, c.appendRange(c.ordersSheet, len(orderHeaders)), valueRange(row)).
		Context(ctx).
		ValueInputOption("RAW").
		InsertDataOption("INSERT_ROWS").
		Do()
	return err
}

func (c *Client) UpsertDeposit(ctx context.Context, u domain.User, tx domain.Transaction, balanceAfterCents int64) error {
	row := []any{
		tx.ID,
		formatTime(tx.CreatedAt),
		u.ID,
		u.TelegramID,
		u.Username,
		u.FirstName,
		tx.Provider,
		tx.ProviderID,
		money(tx.AmountCents),
		tx.Currency,
		tx.Status,
		tx.PayURL,
		money(balanceAfterCents),
	}
	rowNumber, err := c.findRowByFirstColumn(ctx, c.depositsSheet, tx.ID)
	if err != nil {
		return err
	}
	if rowNumber == 0 {
		_, err = c.service.Spreadsheets.Values.Append(c.spreadsheetID, c.appendRange(c.depositsSheet, len(depositHeaders)), valueRange(row)).
			Context(ctx).
			ValueInputOption("RAW").
			InsertDataOption("INSERT_ROWS").
			Do()
		return err
	}
	updateRange := fmt.Sprintf("%s!A%d:%s%d", quoteSheet(c.depositsSheet), rowNumber, columnName(len(depositHeaders)), rowNumber)
	_, err = c.service.Spreadsheets.Values.Update(c.spreadsheetID, updateRange, valueRange(row)).
		Context(ctx).
		ValueInputOption("RAW").
		Do()
	return err
}

func (c *Client) setHeaders(ctx context.Context, sheetName string, headers []any) error {
	rng := fmt.Sprintf("%s!A1:%s1", quoteSheet(sheetName), columnName(len(headers)))
	_, err := c.service.Spreadsheets.Values.Update(c.spreadsheetID, rng, valueRange(headers)).
		Context(ctx).
		ValueInputOption("RAW").
		Do()
	return err
}

func (c *Client) findRowByFirstColumn(ctx context.Context, sheetName, value string) (int, error) {
	rng := fmt.Sprintf("%s!A:A", quoteSheet(sheetName))
	resp, err := c.service.Spreadsheets.Values.Get(c.spreadsheetID, rng).Context(ctx).Do()
	if err != nil {
		return 0, err
	}
	for i, row := range resp.Values {
		if len(row) == 0 {
			continue
		}
		if fmt.Sprint(row[0]) == value {
			return i + 1, nil
		}
	}
	return 0, nil
}

func (c *Client) appendRange(sheetName string, columns int) string {
	return fmt.Sprintf("%s!A:%s", quoteSheet(sheetName), columnName(columns))
}

func valueRange(row []any) *sheets.ValueRange {
	return &sheets.ValueRange{Values: [][]any{row}}
}

func quoteSheet(name string) string {
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}

func columnName(n int) string {
	var out []byte
	for n > 0 {
		n--
		out = append([]byte{byte('A' + n%26)}, out...)
		n /= 26
	}
	return string(out)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.Format(time.RFC3339)
}

func money(cents int64) string {
	return fmt.Sprintf("%.2f", float64(cents)/100)
}
