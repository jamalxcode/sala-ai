package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The AI gateway: an OpenAI-compatible /v1/chat/completions that Sala's apps
// call with the user's Supabase token. Each request holds the most it could
// cost, calls OpenRouter (Vercel AI Gateway if OpenRouter fails), then pays
// the real cost and gives the rest back.

const (
	openRouterURL = "https://openrouter.ai/api/v1/chat/completions"
	vercelURL     = "https://ai-gateway.vercel.sh/v1/chat/completions"
	maxChatBody   = 8 << 20
	streamTimeout = 10 * time.Minute
)

type upstream struct {
	name  string
	url   string
	key   string
	model string
}

// Usage is the OpenAI-style usage object. OpenRouter also reports Cost (USD).
type Usage struct {
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	Cost             *float64 `json:"cost"`
}

var (
	appNameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]{0,39}$`)
	aiHTTP      = &http.Client{Timeout: streamTimeout}
	errNoUpstrm = errors.New("no AI provider configured")
)

func (a *App) upstreamsFor(m Model) []upstream {
	var ups []upstream
	if a.cfg.OpenRouterKey != "" {
		ups = append(ups, upstream{"openrouter", openRouterURL, a.cfg.OpenRouterKey, m.OpenRouterID})
	}
	if a.cfg.VercelKey != "" && m.VercelID != nil && *m.VercelID != "" {
		ups = append(ups, upstream{"vercel", vercelURL, a.cfg.VercelKey, *m.VercelID})
	}
	return ups
}

// GET /v1/models: what apps can offer in their model picker, with prices in
// credits per 1,000 tokens.
func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	models, err := a.models.List(r.Context())
	if err != nil {
		a.fail(w, "list models", err)
		return
	}
	type item struct {
		ID           string  `json:"id"`
		Label        string  `json:"label"`
		Tier         string  `json:"tier"`
		InputPer1K   float64 `json:"credits_per_1k_input"`
		OutputPer1K  float64 `json:"credits_per_1k_output"`
		MaxOutput    int     `json:"max_output_tokens"`
		HasFallback  bool    `json:"has_fallback"`
		ProviderName string  `json:"owned_by"`
	}
	out := make([]item, 0, len(models))
	for _, m := range models {
		// USD per million tokens -> credits (fils) per thousand tokens.
		rate := a.cfg.USDToKWD * (1 + m.MarkupPct/100)
		owner, _, _ := strings.Cut(m.ID, "/")
		out = append(out, item{
			ID:           m.ID,
			Label:        m.Label,
			Tier:         m.Tier,
			InputPer1K:   round2(m.InputUSD * rate),
			OutputPer1K:  round2(m.OutputUSD * rate),
			MaxOutput:    m.MaxOutput,
			HasFallback:  m.VercelID != nil && *m.VercelID != "",
			ProviderName: owner,
		})
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// GET /v1/me/usage
func (a *App) handleUsage(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var rows []map[string]any
	q := url.Values{
		"select":  {"app,model_id,provider,prompt_tokens,completion_tokens,credits,created_at"},
		"user_id": {"eq." + u.ID},
		"order":   {"created_at.desc"},
		"limit":   {"100"},
	}
	if err := a.db.Select(r.Context(), "usage_events", q, &rows); err != nil {
		a.fail(w, "usage", err)
		return
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": rows})
}

// POST /v1/chat/completions
func (a *App) handleChat(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	// AI calls can run for minutes; lift the server's default write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(streamTimeout + time.Minute))
	app := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Sala-App")))
	if !appNameRe.MatchString(app) {
		app = "unknown"
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxChatBody+1))
	if err != nil || len(raw) > maxChatBody {
		writeAIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large.")
		return
	}
	var req map[string]json.RawMessage
	if err := json.Unmarshal(raw, &req); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_json", "Request body must be a JSON object.")
		return
	}
	var modelID string
	_ = json.Unmarshal(req["model"], &modelID)
	var stream bool
	_ = json.Unmarshal(req["stream"], &stream)

	model, ok, err := a.models.Get(r.Context(), modelID)
	if err != nil {
		a.fail(w, "load models", err)
		return
	}
	if !ok {
		writeAIError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("Model %q is not available. See GET /v1/models.", modelID))
		return
	}
	ups := a.upstreamsFor(model)
	if len(ups) == 0 {
		a.fail(w, "chat", errNoUpstrm)
		return
	}

	maxOut := model.MaxOutput
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		var n int
		if json.Unmarshal(req[key], &n) == nil && n > 0 && n < maxOut {
			maxOut = n
		}
	}
	promptEst := estimatePromptTokens(raw)
	hold := toCredits(model.costUSD(promptEst, maxOut), a.cfg.USDToKWD, model.MarkupPct)

	var holdID string
	err = a.db.RPC(r.Context(), "reserve_credits", map[string]any{
		"p_user": u.ID, "p_credits": hold, "p_app": app, "p_model": model.ID,
	}, &holdID)
	var dbErr *DBError
	if errors.As(err, &dbErr) && dbErr.Message == "insufficient_credits" {
		writeAIError(w, http.StatusPaymentRequired, "insufficient_credits",
			fmt.Sprintf("Not enough Sala credits: this request needs up to %d. Buy more at %s", hold, a.cfg.SiteURL))
		return
	}
	if err != nil {
		a.fail(w, "reserve credits", err)
		return
	}

	// From here on the hold must be settled or released, even if the client
	// goes away, so bookkeeping uses a context that outlives the request.
	bg := context.WithoutCancel(r.Context())
	settled := false
	defer func() {
		if !settled {
			a.releaseHold(bg, holdID)
		}
	}()

	resp, up, err := a.callUpstreams(r.Context(), ups, req, stream, maxOut, u.ID)
	if err != nil {
		log.Printf("chat %s: all providers failed: %v", model.ID, err)
		writeAIError(w, http.StatusBadGateway, "provider_unavailable", "The AI provider is unavailable. You were not charged.")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		log.Printf("chat %s via %s: %d %s", model.ID, up.name, resp.StatusCode, truncate(string(body), 300))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	var usage *Usage
	var outChars int
	if stream {
		usage, outChars = a.relayStream(w, resp.Body)
	} else {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			writeAIError(w, http.StatusBadGateway, "provider_error", "Reading the AI response failed. You were not charged.")
			return
		}
		var parsed struct {
			Usage *Usage `json:"usage"`
		}
		_ = json.Unmarshal(body, &parsed)
		usage, outChars = parsed.Usage, len(body)/2

		charged := a.settle(bg, holdID, model, up, usage, promptEst, outChars)
		settled = true
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Sala-Credits-Charged", strconv.Itoa(charged))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	a.settle(bg, holdID, model, up, usage, promptEst, outChars)
	settled = true
}

// callUpstreams tries each provider in order, moving on after network errors,
// rate limits and server errors. The returned response is still open.
func (a *App) callUpstreams(ctx context.Context, ups []upstream, req map[string]json.RawMessage, stream bool, maxOut int, userID string) (*http.Response, upstream, error) {
	userTag := sha256.Sum256([]byte("sala:" + userID))
	var lastErr error
	for i, up := range ups {
		body := make(map[string]any, len(req)+4)
		for k, v := range req {
			body[k] = v
		}
		delete(body, "max_completion_tokens")
		delete(body, "provider")
		body["model"] = up.model
		body["max_tokens"] = maxOut
		body["user"] = hex.EncodeToString(userTag[:8])
		if stream {
			body["stream_options"] = map[string]bool{"include_usage": true}
		}
		if up.name == "openrouter" {
			// Only use providers that don't store or train on prompts.
			body["provider"] = map[string]string{"data_collection": "deny"}
		}
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, up, err
		}

		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, up.url, bytes.NewReader(payload))
		if err != nil {
			return nil, up, err
		}
		hreq.Header.Set("Authorization", "Bearer "+up.key)
		hreq.Header.Set("Content-Type", "application/json")
		if up.name == "openrouter" {
			hreq.Header.Set("HTTP-Referer", a.cfg.SiteURL)
			hreq.Header.Set("X-Title", "Sala AI")
		}

		resp, err := aiHTTP.Do(hreq)
		last := i == len(ups)-1
		switch {
		case err != nil:
			lastErr = fmt.Errorf("%s: %w", up.name, err)
		case !last && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500):
			lastErr = fmt.Errorf("%s: %s", up.name, resp.Status)
			resp.Body.Close()
		default:
			return resp, up, nil
		}
		if ctx.Err() != nil {
			break
		}
		log.Printf("chat: %v, trying next provider", lastErr)
	}
	return nil, upstream{}, lastErr
}

// relayStream copies server-sent events to the client as they arrive and
// returns the usage from the final chunk (if any) plus how many characters
// of output were streamed, for estimating when usage is missing.
func (a *App) relayStream(w http.ResponseWriter, body io.Reader) (*Usage, int) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var st streamState
	br := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			st.observe(line)
			if _, werr := w.Write(line); werr != nil {
				break // client went away; settle with what we've seen
			}
			if len(bytes.TrimSpace(line)) == 0 {
				_ = rc.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	_ = rc.Flush()
	return st.usage, st.chars
}

type streamState struct {
	usage *Usage
	chars int
}

// observe reads one SSE line, keeping the latest usage and counting output text.
func (s *streamState) observe(line []byte) {
	data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:"))
	if !ok {
		return
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if json.Unmarshal(data, &chunk) != nil {
		return
	}
	for _, c := range chunk.Choices {
		s.chars += len(c.Delta.Content) + len(c.Delta.Reasoning)
	}
	if chunk.Usage != nil && (chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
		s.usage = chunk.Usage
	}
}

// settle charges the real cost. Without reported usage it falls back to the
// prompt estimate plus ~4 characters per output token. It never charges more
// than the hold (the database caps it too).
func (a *App) settle(ctx context.Context, holdID string, m Model, up upstream, usage *Usage, promptEst, outChars int) int {
	prompt, completion := promptEst, outChars/4+1
	if usage != nil {
		prompt, completion = usage.PromptTokens, usage.CompletionTokens
	} else {
		log.Printf("chat %s via %s: no usage reported, estimating", m.ID, up.name)
	}
	cost := m.costUSD(prompt, completion)
	if usage != nil && usage.Cost != nil && up.name == "openrouter" && *usage.Cost > 0 {
		cost = *usage.Cost
	}
	credits := toCredits(cost, a.cfg.USDToKWD, m.MarkupPct)

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var charged int
	err := a.db.RPC(ctx, "settle_hold", map[string]any{
		"p_hold": holdID, "p_credits": credits, "p_provider": up.name,
		"p_prompt_tokens": prompt, "p_completion_tokens": completion, "p_cost_usd": cost,
	}, &charged)
	if err != nil {
		// The hold stays open and release_stale_holds returns it later, so
		// the user is never overcharged; we lose this request's cost.
		log.Printf("SETTLE FAILED hold %s (%d credits): %v", holdID, credits, err)
		return 0
	}
	return charged
}

func (a *App) releaseHold(ctx context.Context, holdID string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := a.db.RPC(ctx, "release_hold", map[string]string{"p_hold": holdID}, nil); err != nil {
		log.Printf("release hold %s: %v", holdID, err)
	}
}

// writeAIError writes an OpenAI-style error so existing client libraries
// show the message.
func writeAIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{
		"message": msg, "type": "sala_error", "code": code,
	}})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
