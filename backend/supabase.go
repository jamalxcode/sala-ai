package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Supabase talks to Postgres through PostgREST (/rest/v1) with the secret
// key, and verifies user sessions through GoTrue (/auth/v1).
type Supabase struct {
	baseURL string
	anon    string
	secret  string
	http    *http.Client
}

// DBError is a PostgREST error. For our own `raise exception` calls the
// Message is a short code such as "balance_cap".
type DBError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *DBError) Error() string {
	return fmt.Sprintf("supabase %d %s: %s", e.Status, e.Code, e.Message)
}

func NewSupabase(cfg Config) *Supabase {
	return &Supabase{
		baseURL: cfg.SupabaseURL,
		anon:    cfg.SupabaseAnon,
		secret:  cfg.SupabaseSecret,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *Supabase) setServiceAuth(req *http.Request) {
	req.Header.Set("apikey", s.secret)
	// Legacy service_role keys are JWTs and must also be sent as a bearer
	// token. New-style sb_secret_ keys go in the apikey header only.
	if strings.HasPrefix(s.secret, "eyJ") {
		req.Header.Set("Authorization", "Bearer "+s.secret)
	}
}

// RPC calls a Postgres function and decodes its result into out (may be nil).
func (s *Supabase) RPC(ctx context.Context, fn string, args any, out any) error {
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/rest/v1/rpc/"+fn, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	s.setServiceAuth(req)
	return s.do(req, out)
}

// Select reads rows from a table. query holds PostgREST filters, e.g.
// {"user_id": {"eq.<uuid>"}, "order": {"created_at.desc"}}.
func (s *Supabase) Select(ctx context.Context, table string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/rest/v1/"+table+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	s.setServiceAuth(req)
	return s.do(req, out)
}

// Update patches the rows matched by query with the fields in patch.
func (s *Supabase) Update(ctx context.Context, table string, query url.Values, patch any) error {
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, s.baseURL+"/rest/v1/"+table+"?"+query.Encode(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	s.setServiceAuth(req)
	return s.do(req, nil)
}

func (s *Supabase) do(req *http.Request, out any) error {
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		dbErr := &DBError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, dbErr)
		if dbErr.Message == "" {
			dbErr.Message = strings.TrimSpace(string(data))
		}
		return dbErr
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// User is the subset of a Supabase auth user we use.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// GetUser checks a user's access token with Supabase and returns the user.
func (s *Supabase) GetUser(ctx context.Context, token string) (User, error) {
	var u User
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/auth/v1/user", nil)
	if err != nil {
		return u, err
	}
	req.Header.Set("apikey", s.anon)
	req.Header.Set("Authorization", "Bearer "+token)
	if err := s.do(req, &u); err != nil {
		return u, err
	}
	if u.ID == "" {
		return u, fmt.Errorf("supabase returned no user")
	}
	return u, nil
}
