// Package config loads Bernard's runtime configuration from the environment.
//
// Every setting has a usable default so the binary runs with no configuration
// at all (in that mode it falls back to password auth and a local data dir).
// Production settings live in deploy/bernard.env on the kiosk host.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr    string // listen address, e.g. ":8080"
	DataDir string // holds bernard.db and media/

	// Auth. When GoogleClientID is empty Bernard falls back to DevPassword,
	// which is intended for a laptop, not for the TV on a public network.
	GoogleClientID string
	AllowedDomains []string // email domains permitted to submit; empty = any
	AdminEmails    []string // exact addresses granted the admin console
	DevPassword    string

	// RequireWorkspace rejects any Google account without an `hd` claim
	// matching AllowedDomains. With it on, a personal gmail.com account cannot
	// get in even if someone adds gmail.com to the domain list by mistake.
	RequireWorkspace bool

	MaxUploadBytes    int64
	DefaultDurationMs int
	MaxDurationMs     int

	// DisplayReloadHour is the local hour (0-23) at which the display page
	// reloads itself. A full reload once a day is cheap insurance against any
	// slow leak in the browser; -1 disables it.
	DisplayReloadHour int

	// TrustProxy makes Bernard read the client IP from X-Forwarded-For. Enable
	// it only when something you control (cloudflared, nginx) sets that header.
	TrustProxy bool

	PublicURL string // external origin, used for absolute links in the UI
}

func (c *Config) MediaDir() string { return filepath.Join(c.DataDir, "media") }
func (c *Config) DBPath() string   { return filepath.Join(c.DataDir, "bernard.db") }

// GoogleEnabled reports whether real identity checking is available.
func (c *Config) GoogleEnabled() bool { return c.GoogleClientID != "" }

func (c *Config) IsAdmin(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, a := range c.AdminEmails {
		if a == email {
			return true
		}
	}
	return false
}

// WorkspaceAllowed reports whether a Google `hd` claim satisfies the policy.
// With RequireWorkspace off it always passes, so personal-account installs
// keep working.
func (c *Config) WorkspaceAllowed(hostedDomain string) bool {
	if !c.RequireWorkspace {
		return true
	}
	hostedDomain = strings.ToLower(strings.TrimSpace(hostedDomain))
	if hostedDomain == "" {
		return false // not a Workspace account at all
	}
	for _, d := range c.AllowedDomains {
		if hostedDomain == d {
			return true
		}
	}
	return false
}

// DomainAllowed reports whether an address may submit. An empty allowlist
// permits any domain, which is the right default for a single-org install
// that is not yet exposed to the internet.
func (c *Config) DomainAllowed(email string) bool {
	if len(c.AllowedDomains) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range c.AllowedDomains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}

func Load() (*Config, error) {
	c := &Config{
		Addr:              env("BERNARD_ADDR", ":8080"),
		DataDir:           env("BERNARD_DATA_DIR", defaultDataDir()),
		GoogleClientID:    env("BERNARD_GOOGLE_CLIENT_ID", ""),
		AllowedDomains:    csvLower(env("BERNARD_ALLOWED_DOMAINS", "")),
		AdminEmails:       csvLower(env("BERNARD_ADMIN_EMAILS", "")),
		DevPassword:       env("BERNARD_DEV_PASSWORD", ""),
		RequireWorkspace:  envBool("BERNARD_REQUIRE_WORKSPACE", false),
		MaxUploadBytes:    envInt64("BERNARD_MAX_UPLOAD_BYTES", 256<<20),
		DefaultDurationMs: envInt("BERNARD_DEFAULT_DURATION_MS", 10000),
		MaxDurationMs:     envInt("BERNARD_MAX_DURATION_MS", 120000),
		DisplayReloadHour: envInt("BERNARD_DISPLAY_RELOAD_HOUR", 4),
		TrustProxy:        envBool("BERNARD_TRUST_PROXY", false),
		PublicURL:         strings.TrimSuffix(env("BERNARD_PUBLIC_URL", ""), "/"),
	}

	if !c.GoogleEnabled() && c.DevPassword == "" {
		// Neither auth method configured: generate nothing and refuse to guess.
		// main prints the remedy; failing loudly beats shipping an open door.
		return nil, fmt.Errorf("no auth configured: set BERNARD_GOOGLE_CLIENT_ID (recommended) or BERNARD_DEV_PASSWORD")
	}
	if c.MaxUploadBytes <= 0 {
		return nil, fmt.Errorf("BERNARD_MAX_UPLOAD_BYTES must be positive")
	}
	if c.DefaultDurationMs <= 0 || c.DefaultDurationMs > c.MaxDurationMs {
		return nil, fmt.Errorf("BERNARD_DEFAULT_DURATION_MS must be in (0, %d]", c.MaxDurationMs)
	}
	if err := os.MkdirAll(c.MediaDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create media dir: %w", err)
	}
	return c, nil
}

func defaultDataDir() string {
	if _, err := os.Stat("/var/lib/bernard"); err == nil {
		return "/var/lib/bernard"
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "bernard")
	}
	return "./bernard-data"
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil {
		return v
	}
	return def
}

func envInt64(key string, def int64) int64 {
	if v, err := strconv.ParseInt(env(key, ""), 10, 64); err == nil {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, err := strconv.ParseBool(env(key, "")); err == nil {
		return v
	}
	return def
}

func csvLower(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}
