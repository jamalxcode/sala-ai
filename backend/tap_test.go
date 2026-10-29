package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		minor    int
		currency string
		want     string
	}{
		{1000, "KWD", "1.000"},
		{5250, "KWD", "5.250"},
		{100, "KWD", "0.100"},
		{1999, "SAR", "19.99"},
		{500, "USD", "5.00"},
	}
	for _, c := range cases {
		if got := formatAmount(c.minor, c.currency); got != c.want {
			t.Errorf("formatAmount(%d, %s) = %q, want %q", c.minor, c.currency, got, c.want)
		}
	}
}

func TestToMinorUnits(t *testing.T) {
	cases := []struct {
		amount   string
		currency string
		want     int
	}{
		{"3", "KWD", 3000},
		{"3.000", "KWD", 3000},
		{"0.1", "KWD", 100},
		{"10.005", "KWD", 10005},
		{"19.99", "SAR", 1999},
	}
	for _, c := range cases {
		got, err := toMinorUnits(json.Number(c.amount), c.currency)
		if err != nil || got != c.want {
			t.Errorf("toMinorUnits(%s, %s) = %d, %v; want %d", c.amount, c.currency, got, err, c.want)
		}
	}
	if _, err := toMinorUnits(json.Number("abc"), "KWD"); err == nil {
		t.Error("expected error for non-numeric amount")
	}
}

func TestWebhookHash(t *testing.T) {
	const secret = "sk_test_example"
	var c Charge
	err := json.Unmarshal([]byte(`{
		"id": "chg_TS01A1234567890",
		"status": "CAPTURED",
		"amount": 3,
		"currency": "KWD",
		"reference": {"gateway": "123456", "payment": "pay_789"},
		"transaction": {"created": "1793270000000"}
	}`), &c)
	if err != nil {
		t.Fatal(err)
	}

	// Built by hand from Tap's documented format, amount rounded to 3 decimals for KWD.
	msg := "x_idchg_TS01A1234567890x_amount3.000x_currencyKWDx_gateway_reference123456" +
		"x_payment_referencepay_789x_statusCAPTUREDx_created1793270000000"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	want := hex.EncodeToString(mac.Sum(nil))

	got, err := webhookHash(c, secret)
	if err != nil || got != want {
		t.Fatalf("webhookHash = %s, %v; want %s", got, err, want)
	}
	if !validWebhookHash(c, secret, want) {
		t.Error("valid hash rejected")
	}
	if validWebhookHash(c, secret, "deadbeef") || validWebhookHash(c, secret, "") {
		t.Error("bad hash accepted")
	}

	// Numeric created timestamps must hash the same as quoted ones.
	c.Transaction.Created = json.RawMessage(`1793270000000`)
	if got, _ := webhookHash(c, secret); got != want {
		t.Error("numeric created timestamp hashed differently")
	}
}

func TestFirstNameFromEmail(t *testing.T) {
	if got := firstNameFromEmail("sara@example.com"); got != "sara" {
		t.Errorf("got %q", got)
	}
	if got := firstNameFromEmail(""); got != "Customer" {
		t.Errorf("got %q", got)
	}
}
