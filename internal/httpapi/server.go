// Package httpapi is Bernard's HTTP surface: the submission portal, the admin
// console, the media files, and the playlist the TV consumes.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"bernard/internal/auth"
	"bernard/internal/config"
	"bernard/internal/media"
	"bernard/internal/store"
)

type Server struct {
	cfg      *config.Config
	store    *store.Store
	media    *media.Store
	sessions *auth.Manager
	google   *auth.GoogleVerifier
	hub      *Hub

	portalFS  fs.FS // built React app
	displayFS fs.FS // the display page

	mux http.Handler
}

func New(cfg *config.Config, st *store.Store, portalFS, displayFS fs.FS) *Server {
	s := &Server{
		cfg:       cfg,
		store:     st,
		media:     media.NewStore(cfg.MediaDir()),
		sessions:  auth.NewManager(st, strings.HasPrefix(cfg.PublicURL, "https://")),
		hub:       NewHub(),
		portalFS:  portalFS,
		displayFS: displayFS,
	}
	if cfg.GoogleEnabled() {
		s.google = auth.NewGoogleVerifier(cfg.GoogleClientID)
	}
	s.mux = s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Hub exposes the event hub so background jobs can nudge the display when the
// clock moves an ad in or out of its scheduled window.
func (s *Server) Hub() *Hub { return s.hub }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// ── Display and public data ───────────────────────────────────────────────
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/playlist", s.handlePlaylist)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/play-event", s.handlePlayEvent)
	mux.HandleFunc("GET /api/config", s.handlePublicConfig)
	mux.Handle("GET /media/{file}", s.handleMedia())
	mux.HandleFunc("GET /display", s.handleDisplay)
	mux.HandleFunc("GET /display/", s.handleDisplay)

	// ── Session ───────────────────────────────────────────────────────────────
	mux.HandleFunc("POST /api/auth/google", s.handleGoogleLogin)
	mux.HandleFunc("POST /api/auth/password", s.handlePasswordLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/me", s.handleMe)

	// ── Member ────────────────────────────────────────────────────────────────
	mux.Handle("POST /api/submissions", s.requireMember(s.handleCreateSubmission))
	mux.Handle("GET /api/submissions", s.requireMember(s.handleMySubmissions))
	mux.Handle("DELETE /api/submissions/{id}", s.requireMember(s.handleDeleteMySubmission))

	// ── Admin ─────────────────────────────────────────────────────────────────
	mux.Handle("GET /api/admin/ads", s.requireAdmin(s.handleAdminList))
	mux.Handle("GET /api/admin/stats", s.requireAdmin(s.handleAdminStats))
	mux.Handle("PATCH /api/admin/ads/{id}", s.requireAdmin(s.handleAdminPatch))
	mux.Handle("DELETE /api/admin/ads/{id}", s.requireAdmin(s.handleAdminDelete))
	mux.Handle("POST /api/admin/ads/{id}/review", s.requireAdmin(s.handleAdminReview))
	mux.Handle("PUT /api/admin/order", s.requireAdmin(s.handleAdminReorder))
	mux.Handle("POST /api/admin/display/{command}", s.requireAdmin(s.handleAdminDisplayCommand))

	// ── Portal SPA (everything else) ──────────────────────────────────────────
	mux.Handle("GET /", s.spaHandler())

	return securityHeaders(s.logRequests(mux))
}

// ─── Middleware ───────────────────────────────────────────────────────────────

type ctxKey int

const sessionKey ctxKey = iota

func sessionFrom(ctx context.Context) *store.Session {
	sess, _ := ctx.Value(sessionKey).(*store.Session)
	return sess
}

func (s *Server) requireMember(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.sessions.Lookup(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := s.sessions.Lookup(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		if !s.isAdmin(sess) {
			writeError(w, http.StatusForbidden, "admins only")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

// isAdmin decides whether a session may use the admin console.
//
// Under Google sign-in the address is re-checked against the live allowlist on
// every request, so removing someone from BERNARD_ADMIN_EMAILS takes effect at
// once rather than whenever their 30-day session happens to expire. Password
// mode has no allowlist to check — knowing the password is the whole test — so
// there the flag on the session stands.
func (s *Server) isAdmin(sess *store.Session) bool {
	if !sess.IsAdmin {
		return false
	}
	if !s.cfg.GoogleEnabled() {
		return true
	}
	return s.cfg.IsAdmin(sess.Email)
}

// clientIP returns the address a request came from.
//
// Behind cloudflared every connection arrives from 127.0.0.1, so the real
// address is only available in X-Forwarded-For. That header is trivially
// forged by anyone talking to the port directly, which is why it is read only
// when BERNARD_TRUST_PROXY says something trustworthy sets it. The leftmost
// entry is the original client; the rest are the proxy chain.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			if ip := strings.TrimSpace(xff); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SSE stream is long-lived and the display polls media constantly;
		// logging those at info level would bury everything useful.
		quiet := r.URL.Path == "/api/events" || strings.HasPrefix(r.URL.Path, "/media/")
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if quiet && rec.status < 400 {
			return
		}
		slog.Info("http",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "dur", time.Since(start).Round(time.Millisecond),
			"ip", s.clientIP(r))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the real writer, which the SSE
// handler needs in order to flush.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		// This governs whether our pages may be framed by someone else; it has
		// no bearing on the sandboxed iframes the display renders inside itself.
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("malformed request body")
	}
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"displays":  s.hub.Count(),
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// CollectOrphanedMedia deletes files in the media directory that no ad refers
// to. Uploads that failed after the file landed, and files left by a crash,
// would otherwise sit on the NUC's disk forever.
func (s *Server) CollectOrphanedMedia(ctx context.Context) (int, error) {
	ads, err := s.store.ListAds(ctx)
	if err != nil {
		return 0, err
	}
	referenced := make(map[string]bool, len(ads))
	for _, a := range ads {
		if a.MediaFile != "" {
			referenced[a.MediaFile] = true
		}
	}
	// The grace period keeps this from racing an upload that is still writing.
	return s.media.GC(referenced, time.Hour)
}
