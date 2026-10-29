package main

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ctxKey int

const userKey ctxKey = 0

// authCache remembers verified tokens for a minute so each API call doesn't
// need a round trip to Supabase.
type authCache struct {
	mu      sync.Mutex
	entries map[[32]byte]authEntry
}

type authEntry struct {
	user    User
	expires time.Time
}

const authCacheTTL = time.Minute

func (c *authCache) get(token string) (User, bool) {
	key := sha256.Sum256([]byte(token))
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.entries, key)
		return User{}, false
	}
	return e.user, true
}

func (c *authCache) put(token string, u User) {
	key := sha256.Sum256([]byte(token))
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > 10000 {
		c.entries = map[[32]byte]authEntry{}
	}
	c.entries[key] = authEntry{user: u, expires: time.Now().Add(authCacheTTL)}
}

// requireUser rejects requests without a valid Supabase session and puts the
// user into the request context.
func (a *App) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "login_required")
			return
		}
		u, cached := a.auth.get(token)
		if !cached {
			var err error
			u, err = a.db.GetUser(r.Context(), token)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "login_required")
				return
			}
			a.auth.put(token, u)
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	}
}

func userFrom(r *http.Request) User {
	u, _ := r.Context().Value(userKey).(User)
	return u
}
