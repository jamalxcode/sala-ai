// Command backend is the Sala AI Credits API (api.sala.site): credit packs,
// Tap checkout and webhook, and each user's credits and history.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"
)

type App struct {
	cfg    Config
	db     *Supabase
	tap    *Tap
	auth   *authCache
	models *ModelStore
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	db := NewSupabase(cfg)
	app := &App{
		cfg:    cfg,
		db:     db,
		tap:    NewTap(cfg),
		auth:   &authCache{entries: map[[32]byte]authEntry{}},
		models: &ModelStore{db: db},
	}
	if cfg.OpenRouterKey == "" {
		log.Printf("OPENROUTER_API_KEY not set: /v1/chat/completions will fail until it is")
	}
	go app.backgroundJobs()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	go func() {
		log.Printf("listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /v1/packs", a.handlePacks)
	mux.HandleFunc("GET /v1/me", a.requireUser(a.handleMe))
	mux.HandleFunc("GET /v1/me/ledger", a.requireUser(a.handleLedger))
	mux.HandleFunc("POST /v1/checkout", a.requireUser(a.handleCheckout))
	mux.HandleFunc("GET /v1/orders/{id}", a.requireUser(a.handleOrder))
	mux.HandleFunc("POST /webhooks/tap", a.handleTapWebhook)
	mux.HandleFunc("GET /v1/models", a.handleModels)
	mux.HandleFunc("GET /v1/me/usage", a.requireUser(a.handleUsage))
	mux.HandleFunc("POST /v1/chat/completions", a.requireUser(a.handleChat))
	return a.cors(mux)
}

// backgroundJobs releases holds left open by crashes (every 5 minutes) and
// refreshes model prices from OpenRouter (hourly).
func (a *App) backgroundJobs() {
	syncPrices := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := a.models.SyncPrices(ctx); err != nil {
			log.Printf("price sync: %v", err)
		}
	}
	syncPrices()

	holds := time.NewTicker(5 * time.Minute)
	prices := time.NewTicker(time.Hour)
	for {
		select {
		case <-holds.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			var n int
			if err := a.db.RPC(ctx, "release_stale_holds", map[string]any{}, &n); err != nil {
				log.Printf("release stale holds: %v", err)
			} else if n > 0 {
				log.Printf("released %d stale holds", n)
			}
			cancel()
		case <-prices.C:
			syncPrices()
		}
	}
}

// cors lets the storefront (and how.sala.company etc., if listed in
// ALLOWED_ORIGINS) call the API from the browser.
func (a *App) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(a.cfg.AllowedOrigins, origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Sala-App")
			h.Set("Access-Control-Expose-Headers", "X-Sala-Credits-Charged")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
