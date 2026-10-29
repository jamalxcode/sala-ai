package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Tap is a minimal client for Tap Payments' Charges API.
// Docs: https://developers.tap.company/reference/create-a-charge
type Tap struct {
	base   string
	secret string
	http   *http.Client
}

func NewTap(cfg Config) *Tap {
	return &Tap{base: cfg.TapAPIBase, secret: cfg.TapSecretKey, http: &http.Client{Timeout: 20 * time.Second}}
}

// Charge is the part of Tap's charge object we read.
type Charge struct {
	ID        string      `json:"id"`
	Status    string      `json:"status"`
	Amount    json.Number `json:"amount"`
	Currency  string      `json:"currency"`
	Reference struct {
		Gateway string `json:"gateway"`
		Payment string `json:"payment"`
	} `json:"reference"`
	Transaction struct {
		URL     string          `json:"url"`
		Created json.RawMessage `json:"created"`
	} `json:"transaction"`
	Metadata map[string]string `json:"metadata"`
}

// Charge statuses that will never become CAPTURED.
var tapFailedStatuses = map[string]bool{
	"ABANDONED": true, "CANCELLED": true, "FAILED": true, "DECLINED": true,
	"RESTRICTED": true, "VOID": true, "TIMEDOUT": true,
}

type CreateChargeInput struct {
	OrderID     string
	UserID      string
	AmountFils  int
	Currency    string
	Email       string
	FirstName   string
	Description string
	RedirectURL string
	WebhookURL  string
}

func (t *Tap) CreateCharge(ctx context.Context, in CreateChargeInput) (Charge, error) {
	body := map[string]any{
		"amount":      json.Number(formatAmount(in.AmountFils, in.Currency)),
		"currency":    in.Currency,
		"description": in.Description,
		"customer": map[string]any{
			"first_name": in.FirstName,
			"email":      in.Email,
		},
		"source":   map[string]string{"id": "src_all"},
		"redirect": map[string]string{"url": in.RedirectURL},
		"post":     map[string]string{"url": in.WebhookURL},
		"reference": map[string]string{
			"transaction": in.OrderID,
			"order":       in.OrderID,
			"idempotent":  in.OrderID,
		},
		"metadata": map[string]string{"order_id": in.OrderID, "user_id": in.UserID},
		"receipt":  map[string]bool{"email": true, "sms": false},
	}
	var c Charge
	err := t.call(ctx, http.MethodPost, "/charges", body, &c)
	return c, err
}

func (t *Tap) GetCharge(ctx context.Context, id string) (Charge, error) {
	var c Charge
	err := t.call(ctx, http.MethodGet, "/charges/"+id, nil, &c)
	return c, err
}

func (t *Tap) call(ctx context.Context, method, path string, in any, out any) error {
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.secret)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("tap %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

// currencyDecimals follows Tap's webhook docs: 3 decimals for BHD, KWD, OMR
// and JOD, 2 for everything else.
func currencyDecimals(currency string) int {
	switch strings.ToUpper(currency) {
	case "BHD", "KWD", "OMR", "JOD":
		return 3
	default:
		return 2
	}
}

// formatAmount turns minor units (fils for KWD) into Tap's decimal string.
func formatAmount(minor int, currency string) string {
	d := currencyDecimals(currency)
	return strconv.FormatFloat(float64(minor)/math.Pow10(d), 'f', d, 64)
}

// toMinorUnits turns Tap's decimal amount into minor units (fils for KWD).
func toMinorUnits(amount json.Number, currency string) (int, error) {
	f, err := strconv.ParseFloat(string(amount), 64)
	if err != nil {
		return 0, fmt.Errorf("bad amount %q: %w", amount, err)
	}
	return int(math.Round(f * math.Pow10(currencyDecimals(currency)))), nil
}

// webhookHash computes the value Tap sends in the `hashstring` header:
// HMAC-SHA256, keyed with the secret key, over the charge fields with x_ prefixes.
func webhookHash(c Charge, secret string) (string, error) {
	f, err := strconv.ParseFloat(string(c.Amount), 64)
	if err != nil {
		return "", err
	}
	amount := strconv.FormatFloat(f, 'f', currencyDecimals(c.Currency), 64)
	created := strings.Trim(string(c.Transaction.Created), `"`)

	msg := "x_id" + c.ID +
		"x_amount" + amount +
		"x_currency" + c.Currency +
		"x_gateway_reference" + c.Reference.Gateway +
		"x_payment_reference" + c.Reference.Payment +
		"x_status" + c.Status +
		"x_created" + created

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func validWebhookHash(c Charge, secret, header string) bool {
	want, err := webhookHash(c, secret)
	if err != nil || header == "" {
		return false
	}
	return hmac.Equal([]byte(want), []byte(strings.ToLower(strings.TrimSpace(header))))
}
