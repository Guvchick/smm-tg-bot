package smm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"smm-tg-bot/internal/domain"
)

type Client struct {
	baseURL string
	key     string
	http    *http.Client
}

func NewClient(baseURL, key string) *Client {
	return &Client{
		baseURL: normalizeBaseURL(baseURL),
		key:     key,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

type serviceDTO struct {
	Category string `json:"category"`
	Service  string `json:"service"`
	Name     string `json:"name"`
	Rate     string `json:"rate"`
	Min      string `json:"min"`
	Max      string `json:"max"`
	Social   string `json:"soc"`
	Type     string `json:"type"`
	Refill   bool   `json:"refill"`
	Cancel   bool   `json:"cancel"`
	Error    string `json:"error"`
}

func (c *Client) Services(ctx context.Context) ([]domain.Service, error) {
	var dto []serviceDTO
	if err := c.request(ctx, url.Values{"action": {"services"}}, &dto); err != nil {
		return nil, err
	}
	out := make([]domain.Service, 0, len(dto))
	for _, item := range dto {
		if item.Error != "" {
			return nil, errors.New(item.Error)
		}
		id, _ := strconv.ParseInt(item.Service, 10, 64)
		rate, _ := strconv.ParseFloat(item.Rate, 64)
		minQty, _ := strconv.ParseInt(item.Min, 10, 64)
		maxQty, _ := strconv.ParseInt(item.Max, 10, 64)
		out = append(out, domain.Service{
			ID: id, Name: item.Name, Category: item.Category, Rate: rate,
			Min: minQty, Max: maxQty, Social: item.Social, Type: item.Type,
			Refill: item.Refill, Cancel: item.Cancel, Enabled: true,
		})
	}
	return out, nil
}

func (c *Client) AddOrder(ctx context.Context, serviceID int64, link string, quantity int64, extras map[string]string) (string, error) {
	values := url.Values{
		"action":   {"add"},
		"service":  {strconv.FormatInt(serviceID, 10)},
		"link":     {link},
		"quantity": {strconv.FormatInt(quantity, 10)},
	}
	for k, v := range extras {
		if v != "" {
			values.Set(k, v)
		}
	}
	var res struct {
		Order string `json:"order"`
		Error string `json:"error"`
	}
	if err := c.request(ctx, values, &res); err != nil {
		return "", err
	}
	if res.Error != "" {
		return "", errors.New(res.Error)
	}
	return res.Order, nil
}

type Status struct {
	Charge      string `json:"charge"`
	Currency    string `json:"currency"`
	Service     string `json:"service"`
	Link        string `json:"link"`
	Quantity    string `json:"quantity"`
	StartCount  string `json:"start_count"`
	Date        string `json:"date"`
	Status      string `json:"status"`
	Remains     string `json:"remains"`
	Error       string `json:"error"`
}

func (c *Client) Status(ctx context.Context, orderID string) (Status, error) {
	var res Status
	err := c.request(ctx, url.Values{"action": {"status"}, "order": {orderID}}, &res)
	if res.Error != "" {
		return res, errors.New(res.Error)
	}
	return res, err
}

func (c *Client) request(ctx context.Context, values url.Values, dest any) error {
	values.Set("key", c.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "smm-tg-bot/1.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("socrocket http status %d: %s", resp.StatusCode, bodyPreview(body))
	}
	if looksHTML(body, resp.Header.Get("Content-Type")) {
		return fmt.Errorf("socrocket returned html instead of json; use SOC_ROCKET_API_URL=https://soc-rocket.ru/api/v2/; response=%s", bodyPreview(body))
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("socrocket json decode: %w; response=%s", err, bodyPreview(body))
	}
	return nil
}

func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasSuffix(raw, "/api.php") {
		return "https://soc-rocket.ru/api/v2/"
	}
	return strings.TrimRight(raw, "/") + "/"
}

func looksHTML(body []byte, contentType string) bool {
	trimmed := strings.TrimSpace(string(body))
	return strings.Contains(strings.ToLower(contentType), "text/html") || strings.HasPrefix(trimmed, "<") || strings.HasPrefix(strings.ToLower(trimmed), "<!doctype")
}

func bodyPreview(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	runes := []rune(text)
	if len(runes) > 240 {
		return string(runes[:240]) + "..."
	}
	return text
}

func NormalizeStatus(providerStatus string) string {
	switch strings.ToLower(strings.TrimSpace(providerStatus)) {
	case "completed":
		return "completed"
	case "canceled":
		return "canceled"
	case "partial":
		return "partial"
	case "pending":
		return "pending"
	case "in progress":
		return "in_progress"
	default:
		return "processing"
	}
}
