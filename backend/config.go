package main

import (
	"fmt"
	"os"
	"strings"
)

// Config is read from environment variables (set in Render -> Environment).
type Config struct {
	Port           string
	SiteURL        string   // public storefront, e.g. https://ai.sala.site
	APIURL         string   // this service, e.g. https://api.sala.site
	AllowedOrigins []string // browsers allowed to call the API
	SupabaseURL    string
	SupabaseAnon   string // public anon key, used to verify user tokens
	SupabaseSecret string // service_role / secret key, server only
	TapSecretKey   string // sk_test_... or sk_live_...
	TapAPIBase     string
}

func loadConfig() (Config, error) {
	c := Config{
		Port:           env("PORT", "8080"),
		SiteURL:        strings.TrimRight(env("SITE_URL", "https://ai.sala.site"), "/"),
		APIURL:         strings.TrimRight(env("API_URL", "https://api.sala.site"), "/"),
		SupabaseURL:    strings.TrimRight(os.Getenv("SUPABASE_URL"), "/"),
		SupabaseAnon:   os.Getenv("SUPABASE_ANON_KEY"),
		SupabaseSecret: os.Getenv("SUPABASE_SERVICE_KEY"),
		TapSecretKey:   os.Getenv("TAP_SECRET_KEY"),
		TapAPIBase:     strings.TrimRight(env("TAP_API_BASE", "https://api.tap.company/v2"), "/"),
	}
	for _, o := range strings.Split(env("ALLOWED_ORIGINS", c.SiteURL), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.AllowedOrigins = append(c.AllowedOrigins, strings.TrimRight(o, "/"))
		}
	}

	var missing []string
	for name, v := range map[string]string{
		"SUPABASE_URL":         c.SupabaseURL,
		"SUPABASE_ANON_KEY":    c.SupabaseAnon,
		"SUPABASE_SERVICE_KEY": c.SupabaseSecret,
		"TAP_SECRET_KEY":       c.TapSecretKey,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
