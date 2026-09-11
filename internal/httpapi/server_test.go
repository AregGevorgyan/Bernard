package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"bernard/internal/auth"
	"bernard/internal/config"
	"bernard/internal/store"
)

// onePixelPNG is a real PNG, because uploads are accepted or refused on the
// strength of their leading bytes rather than on the client's Content-Type.
var onePixelPNG = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xde,
	0x00, 0x00, 0x00, 0x0c, 'I', 'D', 'A', 'T',
	0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x03, 0x01, 0x01, 0x00,
	0x18, 0xdd, 0x8d, 0xb0,
	0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
}

type harness struct {
	t      *testing.T
	server *Server
	store  *store.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir,
		// A client ID is set so the admin allowlist is enforced; no token is
		// ever verified here because the tests forge sessions directly.
		GoogleClientID:    "test-client.apps.googleusercontent.com",
		AllowedDomains:    []string{"umd.edu"},
		AdminEmails:       []string{"boss@umd.edu"},
		MaxUploadBytes:    1 << 20,
		DefaultDurationMs: 10000,
		MaxDurationMs:     120000,
		DisplayReloadHour: 4,
	}
	if err := os.MkdirAll(cfg.MediaDir(), 0o755); err != nil {
		t.Fatalf("media dir: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	assets := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>portal</html>")}}
	display := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>display</html>")}}

	return &harness{t: t, server: New(cfg, st, assets, display), store: st}
}

// signIn forges a session the way the real login path would, so tests can cover
// the authorization boundary without standing up Google.
func (h *harness) signIn(email string, isAdmin bool) *http.Cookie {
	h.t.Helper()
	token := "test-token-" + email
	sum := sha256.Sum256([]byte(token))
	if err := h.store.CreateSession(h.t.Context(), hex.EncodeToString(sum[:]), email, email, isAdmin, time.Hour); err != nil {
		h.t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: auth.CookieName, Value: token}
}

func (h *harness) do(method, path string, body io.Reader, cookie *http.Cookie, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.server.ServeHTTP(rec, req)
	return rec
}

func (h *harness) json(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	return h.do(method, path, bytes.NewBufferString(body), cookie, "application/json")
}

// submitPNG posts a well-formed multipart submission and returns the new ad ID.
func (h *harness) submitPNG(title string, cookie *http.Cookie, data []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("title", title)
	part, err := w.CreateFormFile("file", "slide.png")
	if err != nil {
		h.t.Fatalf("form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		h.t.Fatalf("write part: %v", err)
	}
	w.Close()
	return h.do(http.MethodPost, "/api/submissions", &buf, cookie, w.FormDataContentType())
}

func TestUnauthenticatedIsLockedOut(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/submissions"},
		{http.MethodGet, "/api/submissions"},
		{http.MethodGet, "/api/admin/ads"},
		{http.MethodGet, "/api/admin/stats"},
		{http.MethodPut, "/api/admin/order"},
		{http.MethodPost, "/api/admin/display/next"},
	} {
		rec := h.do(tc.method, tc.path, nil, nil, "")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

// A signed-in member is not an organizer. This is the boundary the old build
// did not have at all.
func TestMemberCannotReachAdmin(t *testing.T) {
	h := newHarness(t)
	member := h.signIn("member@umd.edu", false)

	for _, path := range []string{"/api/admin/ads", "/api/admin/stats"} {
		if rec := h.do(http.MethodGet, path, nil, member, ""); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s as member: got %d, want 403", path, rec.Code)
		}
	}

	// Even a session flagged admin is refused when the address is not on the
	// live allowlist, so revoking an organizer takes effect immediately.
	stale := h.signIn("expartner@umd.edu", true)
	if rec := h.do(http.MethodGet, "/api/admin/ads", nil, stale, ""); rec.Code != http.StatusForbidden {
		t.Errorf("de-listed admin: got %d, want 403", rec.Code)
	}

	boss := h.signIn("boss@umd.edu", true)
	if rec := h.do(http.MethodGet, "/api/admin/ads", nil, boss, ""); rec.Code != http.StatusOK {
		t.Errorf("real admin: got %d, want 200", rec.Code)
	}
}

func TestSubmissionLifecycle(t *testing.T) {
	h := newHarness(t)
	member := h.signIn("member@umd.edu", false)
	boss := h.signIn("boss@umd.edu", true)

	rec := h.submitPNG("Demo Night", member, onePixelPNG)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: got %d — %s", rec.Code, rec.Body.String())
	}
	var created store.Ad
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode submission: %v", err)
	}
	if created.Status != store.StatusPending {
		t.Errorf("new ad should be pending, got %q", created.Status)
	}

	// Pending ads must never reach the TV.
	if items := h.playlistIDs(); len(items) != 0 {
		t.Errorf("playlist should be empty before approval, got %v", items)
	}

	if rec := h.json(http.MethodPost, "/api/admin/ads/"+created.ID+"/review", `{"decision":"approve","note":""}`, boss); rec.Code != http.StatusOK {
		t.Fatalf("approve: got %d — %s", rec.Code, rec.Body.String())
	}

	// Approving is the only step: no separate activation.
	ids := h.playlistIDs()
	if len(ids) != 1 || ids[0] != created.ID {
		t.Errorf("playlist after approval: got %v, want [%s]", ids, created.ID)
	}

	// Pausing takes it off without discarding the review decision.
	if rec := h.json(http.MethodPatch, "/api/admin/ads/"+created.ID, `{"enabled":false}`, boss); rec.Code != http.StatusOK {
		t.Fatalf("pause: got %d — %s", rec.Code, rec.Body.String())
	}
	if ids := h.playlistIDs(); len(ids) != 0 {
		t.Errorf("paused ad should leave the playlist, got %v", ids)
	}
}

func TestOwnershipIsCheckedAgainstTheSession(t *testing.T) {
	h := newHarness(t)
	owner := h.signIn("owner@umd.edu", false)
	other := h.signIn("other@umd.edu", false)

	rec := h.submitPNG("Mine", owner, onePixelPNG)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit: got %d — %s", rec.Code, rec.Body.String())
	}
	var ad store.Ad
	_ = json.Unmarshal(rec.Body.Bytes(), &ad)

	if rec := h.do(http.MethodDelete, "/api/submissions/"+ad.ID, nil, other, ""); rec.Code != http.StatusForbidden {
		t.Errorf("deleting someone else's submission: got %d, want 403", rec.Code)
	}

	// And the list is scoped to the signed-in address, not to a parameter.
	rec = h.do(http.MethodGet, "/api/submissions", nil, other, "")
	var theirs []store.Ad
	_ = json.Unmarshal(rec.Body.Bytes(), &theirs)
	if len(theirs) != 0 {
		t.Errorf("other member sees %d submissions, want 0", len(theirs))
	}

	if rec := h.do(http.MethodDelete, "/api/submissions/"+ad.ID, nil, owner, ""); rec.Code != http.StatusNoContent {
		t.Errorf("owner deleting own submission: got %d, want 204", rec.Code)
	}
}

func TestUploadValidation(t *testing.T) {
	h := newHarness(t)
	member := h.signIn("member@umd.edu", false)

	t.Run("wrong type is refused on its bytes", func(t *testing.T) {
		rec := h.submitPNG("Not an image", member, []byte("#!/bin/sh\nrm -rf /\n"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("got %d, want 400 — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("oversize is refused", func(t *testing.T) {
		big := append(append([]byte{}, onePixelPNG...), bytes.Repeat([]byte{0}, 2<<20)...)
		rec := h.submitPNG("Too big", member, big)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("got %d, want a rejection — %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("missing title is refused", func(t *testing.T) {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		part, _ := w.CreateFormFile("file", "x.png")
		_, _ = part.Write(onePixelPNG)
		w.Close()
		rec := h.do(http.MethodPost, "/api/submissions", &buf, member, w.FormDataContentType())
		if rec.Code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", rec.Code)
		}
	})
}

func TestDisplayCommandValidation(t *testing.T) {
	h := newHarness(t)
	boss := h.signIn("boss@umd.edu", true)

	for _, cmd := range []string{"next", "prev", "reload"} {
		if rec := h.do(http.MethodPost, "/api/admin/display/"+cmd, nil, boss, ""); rec.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200", cmd, rec.Code)
		}
	}
	if rec := h.do(http.MethodPost, "/api/admin/display/shutdown", nil, boss, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown command: got %d, want 400", rec.Code)
	}
}

func (h *harness) playlistIDs() []string {
	h.t.Helper()
	rec := h.do(http.MethodGet, "/api/playlist", nil, nil, "")
	if rec.Code != http.StatusOK {
		h.t.Fatalf("playlist: got %d", rec.Code)
	}
	var body struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		h.t.Fatalf("decode playlist: %v", err)
	}
	ids := make([]string, 0, len(body.Items))
	for _, it := range body.Items {
		ids = append(ids, it.ID)
	}
	return ids
}

// A mistyped API path must not fall through to the portal's index.html: the
// caller would get HTML with a 200 and fail somewhere much less obvious.
func TestUnknownAPIPathIs404JSON(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/api/nope", "/media/", "/api/admin/nothing"} {
		rec := h.do(http.MethodGet, path, nil, nil, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: got %d, want 404", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !bytes.Contains([]byte(ct), []byte("json")) {
			t.Errorf("GET %s: content-type %q, want JSON", path, ct)
		}
	}

	// Client routes still resolve to the SPA.
	if rec := h.do(http.MethodGet, "/admin", nil, nil, ""); rec.Code != http.StatusOK {
		t.Errorf("GET /admin: got %d, want the portal", rec.Code)
	}
}

// X-Forwarded-For is attacker-controlled unless something trusted overwrites
// it, so it must be ignored until BERNARD_TRUST_PROXY says otherwise.
func TestClientIPHonoursTrustProxy(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "10.0.0.5:41234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 70.41.3.18")

	h.server.cfg.TrustProxy = false
	if got := h.server.clientIP(req); got != "10.0.0.5" {
		t.Errorf("untrusted proxy: got %q, want the socket address 10.0.0.5", got)
	}

	h.server.cfg.TrustProxy = true
	if got := h.server.clientIP(req); got != "203.0.113.9" {
		t.Errorf("trusted proxy: got %q, want the leftmost forwarded address", got)
	}

	// With the header absent the socket address stands either way.
	req.Header.Del("X-Forwarded-For")
	if got := h.server.clientIP(req); got != "10.0.0.5" {
		t.Errorf("no header: got %q, want 10.0.0.5", got)
	}
}

// The SSE stream must open with enough bytes to clear a proxy's write buffer,
// and must still be parseable — the padding is a comment line, which every SSE
// client ignores.
func TestEventStreamOpensPastProxyBuffering(t *testing.T) {
	h := newHarness(t)

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.server.ServeHTTP(rec, req)
		close(done)
	}()

	// Give the handler a moment to write its preamble, then close the stream.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if got := len(body); got < 2048 {
		t.Errorf("stream opened with %d bytes, want >= 2048 to clear a proxy buffer", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type %q, want text/event-stream", ct)
	}
	if !strings.Contains(body, "retry: 2000") {
		t.Error("stream is missing the reconnect hint")
	}
	if !strings.Contains(body, "event: hello") {
		t.Error("stream is missing the hello event")
	}
	// Every padding line must be a comment, or clients would try to parse it.
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "padding") && !strings.HasPrefix(line, ":") {
			t.Errorf("padding leaked outside a comment line: %.40q", line)
		}
	}
}
