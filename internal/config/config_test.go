package config

import "testing"

func TestDomainAllowed(t *testing.T) {
	cases := []struct {
		name    string
		domains []string
		email   string
		want    bool
	}{
		{"empty allowlist accepts anyone", nil, "someone@gmail.com", true},
		{"exact match", []string{"umd.edu"}, "a@umd.edu", true},
		{"subdomain match", []string{"umd.edu"}, "a@terpmail.umd.edu", true},
		{"case insensitive", []string{"umd.edu"}, "a@UMD.EDU", true},
		{"different domain", []string{"umd.edu"}, "a@gmail.com", false},
		// The suffix check must anchor on a dot, or "notumd.edu" slips through.
		{"lookalike suffix", []string{"umd.edu"}, "a@notumd.edu", false},
		{"second entry", []string{"umd.edu", "startupshell.org"}, "a@startupshell.org", true},
		{"no at sign", []string{"umd.edu"}, "nonsense", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{AllowedDomains: tc.domains}
			if got := c.DomainAllowed(tc.email); got != tc.want {
				t.Errorf("DomainAllowed(%q) = %v, want %v", tc.email, got, tc.want)
			}
		})
	}
}

func TestIsAdmin(t *testing.T) {
	c := &Config{AdminEmails: []string{"areg@terpmail.umd.edu"}}
	if !c.IsAdmin("AREG@terpmail.umd.edu") {
		t.Error("admin check should be case insensitive")
	}
	if !c.IsAdmin("  areg@terpmail.umd.edu  ") {
		t.Error("admin check should tolerate surrounding whitespace")
	}
	if c.IsAdmin("someone@else.org") {
		t.Error("non-admin was granted admin")
	}
	if (&Config{}).IsAdmin("anyone@example.com") {
		t.Error("an empty admin list must grant nobody")
	}
}

func TestLoadRequiresAnAuthMethod(t *testing.T) {
	t.Setenv("BERNARD_DATA_DIR", t.TempDir())
	// Neither BERNARD_GOOGLE_CLIENT_ID nor BERNARD_DEV_PASSWORD is set: refusing
	// to start beats starting with no way to tell who anyone is.
	if _, err := Load(); err == nil {
		t.Fatal("Load should fail when no auth method is configured")
	}

	t.Setenv("BERNARD_DEV_PASSWORD", "something")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with a password: %v", err)
	}
	if cfg.GoogleEnabled() {
		t.Error("GoogleEnabled should be false with no client ID")
	}
}

func TestCSVParsing(t *testing.T) {
	t.Setenv("BERNARD_DATA_DIR", t.TempDir())
	t.Setenv("BERNARD_DEV_PASSWORD", "x")
	t.Setenv("BERNARD_ALLOWED_DOMAINS", " UMD.edu , startupshell.org ,, ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"umd.edu", "startupshell.org"}
	if len(cfg.AllowedDomains) != len(want) {
		t.Fatalf("got %v, want %v", cfg.AllowedDomains, want)
	}
	for i := range want {
		if cfg.AllowedDomains[i] != want[i] {
			t.Errorf("domain %d: got %q, want %q", i, cfg.AllowedDomains[i], want[i])
		}
	}
}
