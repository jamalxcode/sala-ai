package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Pack struct {
	ID        string `json:"id"`
	NameEN    string `json:"name_en"`
	NameAR    string `json:"name_ar"`
	PriceFils int    `json:"price_fils"`
	Credits   int    `json:"credits"`
}

type Order struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id,omitempty"`
	PackID      string     `json:"pack_id"`
	PriceFils   int        `json:"price_fils"`
	Credits     int        `json:"credits"`
	Currency    string     `json:"currency"`
	Status      string     `json:"status"`
	TapChargeID *string    `json:"tap_charge_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	PaidAt      *time.Time `json:"paid_at,omitempty"`
}

type LedgerEntry struct {
	ID        int64     `json:"id"`
	Amount    int       `json:"amount"`
	Kind      string    `json:"kind"`
	Note      *string   `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// GET /v1/packs
func (a *App) handlePacks(w http.ResponseWriter, r *http.Request) {
	var packs []Pack
	q := url.Values{
		"select": {"id,name_en,name_ar,price_fils,credits"},
		"active": {"eq.true"},
		"order":  {"sort.asc"},
	}
	if err := a.db.Select(r.Context(), "packs", q, &packs); err != nil {
		a.fail(w, "list packs", err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{"currency": "KWD", "packs": packs})
}

// GET /v1/me
func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	bal, err := a.balance(r.Context(), u.ID)
	if err != nil {
		a.fail(w, "balance", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": u.Email, "balance": bal, "max_balance": maxBalance})
}

// GET /v1/me/ledger
func (a *App) handleLedger(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var rows []LedgerEntry
	q := url.Values{
		"select":  {"id,amount,kind,note,created_at"},
		"user_id": {"eq." + u.ID},
		"order":   {"created_at.desc"},
		"limit":   {"100"},
	}
	if err := a.db.Select(r.Context(), "ledger_entries", q, &rows); err != nil {
		a.fail(w, "ledger", err)
		return
	}
	if rows == nil {
		rows = []LedgerEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": rows})
}

// POST /v1/checkout {"pack_id": "plus"} -> {"order_id", "url"}
func (a *App) handleCheckout(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var in struct {
		PackID string `json:"pack_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || in.PackID == "" {
		writeError(w, http.StatusBadRequest, "pack_required")
		return
	}

	var order Order
	err := a.db.RPC(r.Context(), "create_order", map[string]string{"p_user": u.ID, "p_pack": in.PackID}, &order)
	var dbErr *DBError
	if errors.As(err, &dbErr) && (dbErr.Message == "unknown_pack" || dbErr.Message == "balance_cap") {
		writeError(w, http.StatusUnprocessableEntity, dbErr.Message)
		return
	}
	if err != nil {
		a.fail(w, "create order", err)
		return
	}

	charge, err := a.tap.CreateCharge(r.Context(), CreateChargeInput{
		OrderID:     order.ID,
		UserID:      u.ID,
		AmountFils:  order.PriceFils,
		Currency:    order.Currency,
		Email:       u.Email,
		FirstName:   firstNameFromEmail(u.Email),
		Description: fmt.Sprintf("Sala AI Credits: %s pack (%d credits)", order.PackID, order.Credits),
		RedirectURL: a.cfg.SiteURL + "/checkout/return/?order=" + order.ID,
		WebhookURL:  a.cfg.APIURL + "/webhooks/tap",
	})
	if err != nil {
		a.fail(w, "tap create charge", err)
		return
	}
	if charge.ID == "" || charge.Transaction.URL == "" {
		a.fail(w, "tap create charge", fmt.Errorf("charge without id or payment url: %+v", charge))
		return
	}

	err = a.db.RPC(r.Context(), "attach_charge",
		map[string]string{"p_order": order.ID, "p_charge": charge.ID, "p_tap_status": charge.Status}, nil)
	if err != nil {
		a.fail(w, "attach charge", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"order_id": order.ID, "url": charge.Transaction.URL})
}

// GET /v1/orders/{id}: the return page polls this after payment. If the
// order is still pending it re-checks the charge with Tap, so credits arrive
// even if the webhook is slow.
func (a *App) handleOrder(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	order, err := a.getOrder(r.Context(), id, u.ID)
	if err != nil {
		a.fail(w, "get order", err)
		return
	}
	if order == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if order.Status == "pending" && order.TapChargeID != nil {
		if err := a.reconcile(r.Context(), *order.TapChargeID); err != nil {
			log.Printf("reconcile %s: %v", *order.TapChargeID, err)
		} else if order, err = a.getOrder(r.Context(), id, u.ID); err != nil || order == nil {
			a.fail(w, "get order", err)
			return
		}
	}
	bal, err := a.balance(r.Context(), u.ID)
	if err != nil {
		a.fail(w, "balance", err)
		return
	}
	order.UserID = ""
	order.TapChargeID = nil
	writeJSON(w, http.StatusOK, map[string]any{"order": order, "balance": bal})
}

// POST /webhooks/tap: Tap posts the charge here when its status changes.
// The body is never trusted for the outcome: we always fetch the charge from
// Tap's API with our secret key before adding credits.
func (a *App) handleTapWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_body")
		return
	}
	var posted Charge
	if err := json.Unmarshal(body, &posted); err != nil || !strings.HasPrefix(posted.ID, "chg_") {
		writeError(w, http.StatusBadRequest, "bad_body")
		return
	}
	if !validWebhookHash(posted, a.cfg.TapSecretKey, r.Header.Get("hashstring")) {
		// Logged, not rejected: the outcome comes from Tap's API below, so a
		// forged post can't add credits. Watch these logs during testing.
		log.Printf("tap webhook %s: hashstring mismatch", posted.ID)
	}

	if err := a.reconcile(r.Context(), posted.ID); err != nil {
		var dbErr *DBError
		if errors.As(err, &dbErr) && dbErr.Message == "unknown_charge" {
			log.Printf("tap webhook %s: no matching order", posted.ID)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		a.fail(w, "tap webhook "+posted.ID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// reconcile fetches a charge from Tap and applies its outcome to the order.
func (a *App) reconcile(ctx context.Context, chargeID string) error {
	c, err := a.tap.GetCharge(ctx, chargeID)
	if err != nil {
		return err
	}
	switch {
	case c.Status == "CAPTURED":
		fils, err := toMinorUnits(c.Amount, c.Currency)
		if err != nil {
			return err
		}
		var res map[string]any
		if err := a.db.RPC(ctx, "fulfill_order", map[string]any{
			"p_charge": c.ID, "p_amount_fils": fils, "p_currency": c.Currency,
		}, &res); err != nil {
			return err
		}
		if res["already"] != true {
			log.Printf("order %v paid: charge %s, %d fils", res["order_id"], c.ID, fils)
		}
	case tapFailedStatuses[c.Status]:
		return a.db.RPC(ctx, "fail_order", map[string]string{"p_charge": c.ID, "p_tap_status": c.Status}, nil)
	}
	return nil
}

func (a *App) getOrder(ctx context.Context, id, userID string) (*Order, error) {
	var rows []Order
	q := url.Values{
		"select":  {"id,pack_id,price_fils,credits,currency,status,tap_charge_id,created_at,paid_at"},
		"id":      {"eq." + id},
		"user_id": {"eq." + userID},
	}
	if err := a.db.Select(ctx, "orders", q, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func (a *App) balance(ctx context.Context, userID string) (int, error) {
	var bal int
	err := a.db.RPC(ctx, "get_balance", map[string]string{"p_user": userID}, &bal)
	return bal, err
}

// Keep in step with max_balance() in the SQL migration.
const maxBalance = 50000

func firstNameFromEmail(email string) string {
	name, _, _ := strings.Cut(email, "@")
	if name == "" {
		return "Customer"
	}
	return name
}

func (a *App) fail(w http.ResponseWriter, what string, err error) {
	log.Printf("%s: %v", what, err)
	writeError(w, http.StatusInternalServerError, "server_error")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
