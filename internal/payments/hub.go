package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"smm-tg-bot/internal/config"
)

type InvoiceRequest struct {
	ID          string
	AmountCents int64
	Currency    string
	Description string
	CallbackURL string
}

type Invoice struct {
	ProviderID string
	PayURL     string
}

type WebhookEvent struct {
	Provider    string
	ProviderID  string
	LocalID     string
	AmountCents int64
	Currency    string
	Status      string
	Raw         []byte
}

type Hub struct {
	cfg  config.Config
	http *http.Client
}

func NewHub(cfg config.Config) *Hub {
	return &Hub{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second}}
}

func (h *Hub) CreateInvoice(ctx context.Context, provider string, req InvoiceRequest) (Invoice, error) {
	if !h.cfg.PaymentEnabled(provider) {
		return Invoice{}, fmt.Errorf("payment provider disabled")
	}
	switch provider {
	case "cryptobot":
		return h.createCryptoBot(ctx, req)
	case "pally":
		return h.createPally(ctx, req)
	case "platega":
		return h.createPlatega(ctx, req)
	case "heleket":
		return h.createHeleket(ctx, req)
	default:
		return Invoice{}, fmt.Errorf("unknown payment provider")
	}
}

func (h *Hub) createCryptoBot(ctx context.Context, req InvoiceRequest) (Invoice, error) {
	body := map[string]any{
		"currency_type": "fiat",
		"fiat":          req.Currency,
		"amount":        fmt.Sprintf("%.2f", float64(req.AmountCents)/100),
		"description":   req.Description,
		"payload":       req.ID,
		"expires_in":    3600,
	}
	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.CryptoBotBase+"/api/createInvoice", bytes.NewReader(payload))
	if err != nil {
		return Invoice{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Crypto-Pay-API-Token", h.cfg.CryptoBotToken)
	var res struct {
		OK     bool `json:"ok"`
		Result struct {
			InvoiceID     int64  `json:"invoice_id"`
			BotInvoiceURL string `json:"bot_invoice_url"`
			PayURL        string `json:"pay_url"`
		} `json:"result"`
		Error string `json:"error"`
	}
	if err := h.doJSON(httpReq, &res); err != nil {
		return Invoice{}, err
	}
	if !res.OK {
		return Invoice{}, fmt.Errorf("cryptobot: %s", res.Error)
	}
	payURL := res.Result.BotInvoiceURL
	if payURL == "" {
		payURL = res.Result.PayURL
	}
	return Invoice{ProviderID: fmt.Sprint(res.Result.InvoiceID), PayURL: payURL}, nil
}

func (h *Hub) createPally(ctx context.Context, req InvoiceRequest) (Invoice, error) {
	values := url.Values{
		"amount":                {fmt.Sprintf("%.2f", float64(req.AmountCents)/100)},
		"order_id":              {req.ID},
		"description":           {req.Description},
		"type":                  {"normal"},
		"shop_id":               {h.cfg.PallyShopID},
		"currency_in":           {req.Currency},
		"callback_url":          {req.CallbackURL},
		"payer_pays_commission": {"1"},
		"name":                  {"SMM balance"},
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.PallyAPIURL, strings.NewReader(values.Encode()))
	if err != nil {
		return Invoice{}, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Authorization", "Bearer "+h.cfg.PallyToken)
	var res struct {
		Success     any    `json:"success"`
		LinkPageURL string `json:"link_page_url"`
		BillID      string `json:"bill_id"`
	}
	if err := h.doJSON(httpReq, &res); err != nil {
		return Invoice{}, err
	}
	return Invoice{ProviderID: res.BillID, PayURL: res.LinkPageURL}, nil
}

func (h *Hub) createPlatega(ctx context.Context, req InvoiceRequest) (Invoice, error) {
	body := map[string]any{
		"amount":      float64(req.AmountCents) / 100,
		"currency":    req.Currency,
		"description": req.Description,
		"callback":    req.CallbackURL,
		"orderId":     req.ID,
	}
	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.PlategaAPIURL, bytes.NewReader(payload))
	if err != nil {
		return Invoice{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-MerchantId", h.cfg.PlategaMerchant)
	httpReq.Header.Set("X-Secret", h.cfg.PlategaSecret)
	var res map[string]any
	if err := h.doJSON(httpReq, &res); err != nil {
		return Invoice{}, err
	}
	return Invoice{ProviderID: stringFromMap(res, "id"), PayURL: firstString(res, "url", "link", "paymentUrl", "redirectUrl")}, nil
}

func (h *Hub) createHeleket(ctx context.Context, req InvoiceRequest) (Invoice, error) {
	body := map[string]any{
		"amount":       fmt.Sprintf("%.2f", float64(req.AmountCents)/100),
		"currency":     req.Currency,
		"order_id":     req.ID,
		"url_callback": req.CallbackURL,
	}
	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.HeleketAPIURL, bytes.NewReader(payload))
	if err != nil {
		return Invoice{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("merchant", h.cfg.HeleketMerchant)
	httpReq.Header.Set("sign", md5Hex(base64.StdEncoding.EncodeToString(payload)+h.cfg.HeleketPayKey))
	var res map[string]any
	if err := h.doJSON(httpReq, &res); err != nil {
		return Invoice{}, err
	}
	data, _ := res["result"].(map[string]any)
	if data == nil {
		data = res
	}
	return Invoice{ProviderID: firstString(data, "uuid", "id"), PayURL: firstString(data, "url", "payment_url", "link")}, nil
}

func (h *Hub) ParseWebhook(provider string, r *http.Request, raw []byte) (WebhookEvent, error) {
	if !h.cfg.PaymentEnabled(provider) {
		return WebhookEvent{}, fmt.Errorf("payment provider disabled")
	}
	switch provider {
	case "platega":
		return h.parsePlatega(r, raw)
	case "pally":
		return h.parsePally(r, raw)
	case "heleket":
		return h.parseHeleket(raw)
	case "cryptobot":
		return h.parseCryptoBot(r, raw)
	default:
		return WebhookEvent{}, fmt.Errorf("unknown webhook provider")
	}
}

func (h *Hub) parsePlatega(r *http.Request, raw []byte) (WebhookEvent, error) {
	if h.cfg.PlategaMerchant != "" && r.Header.Get("X-MerchantId") != h.cfg.PlategaMerchant {
		return WebhookEvent{}, fmt.Errorf("bad platega merchant")
	}
	if h.cfg.PlategaSecret != "" && r.Header.Get("X-Secret") != h.cfg.PlategaSecret {
		return WebhookEvent{}, fmt.Errorf("bad platega secret")
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	status := strings.ToUpper(stringFromMap(body, "status"))
	return WebhookEvent{
		Provider: "platega", ProviderID: stringFromMap(body, "id"), LocalID: firstString(body, "orderId", "order_id"),
		AmountCents: amountCents(body["amount"]), Currency: stringFromMap(body, "currency"), Status: paidStatus(status, "CONFIRMED"), Raw: raw,
	}, nil
}

func (h *Hub) parsePally(r *http.Request, raw []byte) (WebhookEvent, error) {
	if err := r.ParseForm(); err != nil {
		return WebhookEvent{}, err
	}
	outSum := r.Form.Get("OutSum")
	invID := r.Form.Get("InvId")
	expected := strings.ToUpper(md5Hex(outSum + ":" + invID + ":" + h.cfg.PallyWebhookSecret))
	if h.cfg.PallyWebhookSecret != "" && !hmac.Equal([]byte(expected), []byte(strings.ToUpper(r.Form.Get("SignatureValue")))) {
		return WebhookEvent{}, fmt.Errorf("bad pally signature")
	}
	return WebhookEvent{
		Provider: "pally", ProviderID: r.Form.Get("TrsId"), LocalID: invID,
		AmountCents: parseMoneyCents(outSum), Currency: r.Form.Get("CurrencyIn"), Status: paidStatus(strings.ToUpper(r.Form.Get("Status")), "SUCCESS"), Raw: []byte(r.Form.Encode()),
	}, nil
}

func (h *Hub) parseHeleket(raw []byte) (WebhookEvent, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return WebhookEvent{}, err
	}
	sign := stringFromMap(body, "sign")
	delete(body, "sign")
	checkBody, _ := json.Marshal(body)
	expected := md5Hex(base64.StdEncoding.EncodeToString(checkBody) + h.cfg.HeleketPayKey)
	if h.cfg.HeleketPayKey != "" && !hmac.Equal([]byte(expected), []byte(sign)) {
		return WebhookEvent{}, fmt.Errorf("bad heleket signature")
	}
	status := stringFromMap(body, "status")
	return WebhookEvent{
		Provider: "heleket", ProviderID: stringFromMap(body, "uuid"), LocalID: stringFromMap(body, "order_id"),
		AmountCents: amountCents(body["amount"]), Currency: stringFromMap(body, "currency"), Status: paidStatus(status, "paid", "paid_over"), Raw: raw,
	}, nil
}

func (h *Hub) parseCryptoBot(r *http.Request, raw []byte) (WebhookEvent, error) {
	secret := sha256.Sum256([]byte(h.cfg.CryptoBotToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write(raw)
	expected := hex.EncodeToString(mac.Sum(nil))
	got := r.Header.Get("crypto-pay-api-signature")
	if h.cfg.CryptoBotToken != "" && !hmac.Equal([]byte(expected), []byte(got)) {
		return WebhookEvent{}, fmt.Errorf("bad cryptobot signature")
	}
	var body struct {
		UpdateType string `json:"update_type"`
		Payload    struct {
			InvoiceID int64  `json:"invoice_id"`
			Status    string `json:"status"`
			Payload   string `json:"payload"`
			Amount    string `json:"amount"`
			Fiat      string `json:"fiat"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return WebhookEvent{}, err
	}
	return WebhookEvent{
		Provider: "cryptobot", ProviderID: fmt.Sprint(body.Payload.InvoiceID), LocalID: body.Payload.Payload,
		AmountCents: parseMoneyCents(body.Payload.Amount), Currency: body.Payload.Fiat, Status: paidStatus(body.Payload.Status, "paid"), Raw: raw,
	}, nil
}

func (h *Hub) doJSON(req *http.Request, dest any) error {
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("payment http %d: %s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, dest)
}

func paidStatus(status string, paid ...string) string {
	status = strings.ToLower(status)
	for _, p := range paid {
		if status == strings.ToLower(p) {
			return "paid"
		}
	}
	if strings.Contains(status, "cancel") || strings.Contains(status, "fail") || strings.Contains(status, "expired") {
		return "failed"
	}
	return "pending"
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func parseMoneyCents(raw string) int64 {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
	var whole, frac string
	parts := strings.SplitN(raw, ".", 2)
	whole = parts[0]
	if len(parts) > 1 {
		frac = parts[1]
	}
	for len(frac) < 2 {
		frac += "0"
	}
	if len(frac) > 2 {
		frac = frac[:2]
	}
	var w, f int64
	fmt.Sscanf(whole, "%d", &w)
	fmt.Sscanf(frac, "%d", &f)
	return w*100 + f
}

func amountCents(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x * 100)
	case string:
		return parseMoneyCents(x)
	default:
		return 0
	}
}

func stringFromMap(m map[string]any, key string) string {
	v, _ := m[key].(string)
	if v != "" {
		return v
	}
	if f, ok := m[key].(float64); ok {
		return fmt.Sprint(f)
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := stringFromMap(m, key); v != "" {
			return v
		}
	}
	return ""
}
