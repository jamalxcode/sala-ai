package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Model is a row of the models table: what apps may call and what it costs us.
type Model struct {
	ID           string  `json:"id"`
	Label        string  `json:"label"`
	Tier         string  `json:"tier"`
	OpenRouterID string  `json:"openrouter_id"`
	VercelID     *string `json:"vercel_id"`
	InputUSD     float64 `json:"input_usd_per_mtok"`
	OutputUSD    float64 `json:"output_usd_per_mtok"`
	MarkupPct    float64 `json:"markup_pct"`
	MaxOutput    int     `json:"max_output_tokens"`
	Sort         int     `json:"sort"`
}

// costUSD is what a request costs us at the model's list price.
func (m Model) costUSD(promptTokens, completionTokens int) float64 {
	return (float64(promptTokens)*m.InputUSD + float64(completionTokens)*m.OutputUSD) / 1e6
}

// toCredits prices a cost in credits (1 credit = 1 fils), rounding up and
// charging at least 1 credit per request.
func toCredits(costUSD, usdToKWD, markupPct float64) int {
	c := int(math.Ceil(costUSD*usdToKWD*1000*(1+markupPct/100) - 1e-9))
	if c < 1 {
		return 1
	}
	return c
}

// estimatePromptTokens over-estimates input tokens from the request size
// (about 3 bytes per token), which is safe for holding credits.
func estimatePromptTokens(body []byte) int {
	return len(body)/3 + 16
}

// ModelStore caches the models table for a minute at a time.
type ModelStore struct {
	db     *Supabase
	mu     sync.Mutex
	models map[string]Model
	order  []Model
	loaded time.Time
}

const modelCacheTTL = time.Minute

func (s *ModelStore) load(ctx context.Context) error {
	var rows []Model
	q := url.Values{
		"select": {"id,label,tier,openrouter_id,vercel_id,input_usd_per_mtok,output_usd_per_mtok,markup_pct,max_output_tokens,sort"},
		"active": {"eq.true"},
		"order":  {"sort.asc"},
	}
	if err := s.db.Select(ctx, "models", q, &rows); err != nil {
		return err
	}
	m := make(map[string]Model, len(rows))
	for _, r := range rows {
		m[r.ID] = r
	}
	s.models, s.order, s.loaded = m, rows, time.Now()
	return nil
}

func (s *ModelStore) fresh(ctx context.Context) error {
	if time.Since(s.loaded) < modelCacheTTL && s.models != nil {
		return nil
	}
	return s.load(ctx)
}

// Get returns an active model by id.
func (s *ModelStore) Get(ctx context.Context, id string) (Model, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fresh(ctx); err != nil {
		return Model{}, false, err
	}
	m, ok := s.models[id]
	return m, ok, nil
}

// List returns active models in display order.
func (s *ModelStore) List(ctx context.Context) ([]Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fresh(ctx); err != nil {
		return nil, err
	}
	return s.order, nil
}

// SyncPrices copies OpenRouter's current list prices into the models table so
// usage is priced from what we actually pay. Markup is left alone.
func (s *ModelStore) SyncPrices(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openrouter.ai/api/v1/models", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openrouter models: %s", resp.Status)
	}
	var list struct {
		Data []struct {
			ID      string `json:"id"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return err
	}
	prices := map[string][2]float64{}
	for _, d := range list.Data {
		in, err1 := strconv.ParseFloat(d.Pricing.Prompt, 64)
		out, err2 := strconv.ParseFloat(d.Pricing.Completion, 64)
		if err1 == nil && err2 == nil && in > 0 && out > 0 {
			prices[d.ID] = [2]float64{in * 1e6, out * 1e6}
		}
	}

	ours, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, m := range ours {
		p, ok := prices[m.OpenRouterID]
		if !ok {
			log.Printf("price sync: %s not found on OpenRouter", m.OpenRouterID)
			continue
		}
		if p[0] == m.InputUSD && p[1] == m.OutputUSD {
			continue
		}
		err := s.db.Update(ctx, "models", url.Values{"id": {"eq." + m.ID}}, map[string]any{
			"input_usd_per_mtok":  p[0],
			"output_usd_per_mtok": p[1],
			"prices_synced_at":    time.Now().UTC(),
		})
		if err != nil {
			return err
		}
		log.Printf("price sync: %s now $%.4f / $%.4f per M tokens", m.ID, p[0], p[1])
	}

	s.mu.Lock()
	s.loaded = time.Time{}
	s.mu.Unlock()
	return nil
}
