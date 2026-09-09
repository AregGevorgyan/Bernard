package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"bernard/internal/store"
)

const (
	CookieName  = "bernard_session"
	SessionTTL  = 30 * 24 * time.Hour
	tokenLength = 32
)

var ErrNoSession = errors.New("no session")

// Manager issues and validates session cookies.
//
// Only a SHA-256 of the token is stored, so a leaked database snapshot does not
// hand over live sessions.
type Manager struct {
	store  *store.Store
	secure bool // set the Secure cookie flag (true once served over HTTPS)
}

func NewManager(s *store.Store, secure bool) *Manager {
	return &Manager{store: s, secure: secure}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Issue creates a session and writes the cookie.
func (m *Manager) Issue(ctx context.Context, w http.ResponseWriter, id Identity, isAdmin bool) error {
	raw := make([]byte, tokenLength)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := m.store.CreateSession(ctx, hashToken(token), id.Email, id.Name, isAdmin, SessionTTL); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(SessionTTL),
		MaxAge:   int(SessionTTL.Seconds()),
	})
	return nil
}

// Lookup resolves the session on a request, or ErrNoSession.
func (m *Manager) Lookup(r *http.Request) (*store.Session, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, ErrNoSession
	}
	sess, err := m.store.GetSession(r.Context(), hashToken(c.Value))
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// Revoke deletes the session and clears the cookie.
func (m *Manager) Revoke(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		_ = m.store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// ConstantTimeEqual compares two secrets without leaking length-independent
// timing, used for the dev-password fallback.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
