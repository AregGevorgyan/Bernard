// Package auth verifies Google sign-ins and manages Bernard's session cookies.
package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const googleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

var googleIssuers = []string{"accounts.google.com", "https://accounts.google.com"}

// Identity is what we trust about a signed-in person after verification.
type Identity struct {
	Email string
	Name  string
	// HostedDomain is Google's `hd` claim: the Workspace domain the account
	// belongs to. It is empty for a personal gmail.com account, which is what
	// makes it a stronger signal than the email suffix alone.
	HostedDomain string
}

// GoogleVerifier validates Google Identity Services ID tokens locally, against
// Google's published signing keys.
//
// The old kiosk decoded the token in the browser and trusted whatever email the
// client then posted, so anyone could submit as anyone. Verification has to
// happen here, on the server, or it is decoration.
type GoogleVerifier struct {
	clientID string
	client   *http.Client

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func NewGoogleVerifier(clientID string) *GoogleVerifier {
	return &GoogleVerifier{
		clientID: clientID,
		client:   &http.Client{Timeout: 10 * time.Second},
		keys:     map[string]*rsa.PublicKey{},
	}
}

// Verify checks the signature, issuer, audience and expiry of an ID token and
// returns the verified email. A token whose email is not marked verified by
// Google is rejected: an unverified address is not proof of anything.
func (v *GoogleVerifier) Verify(ctx context.Context, rawToken string) (*Identity, error) {
	var claims struct {
		jwt.RegisteredClaims
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"` // Google has sent both bool and string
		Name          string `json:"name"`
		HostedDomain  string `json:"hd"`
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithAudience(v.clientID),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	_, err := parser.ParseWithClaims(rawToken, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("token has no kid")
		}
		return v.keyForKID(ctx, kid)
	})
	if err != nil {
		return nil, fmt.Errorf("invalid google token: %w", err)
	}

	issuerOK := false
	for _, iss := range googleIssuers {
		if claims.Issuer == iss {
			issuerOK = true
			break
		}
	}
	if !issuerOK {
		return nil, fmt.Errorf("unexpected issuer %q", claims.Issuer)
	}
	if !truthy(claims.EmailVerified) {
		return nil, fmt.Errorf("google has not verified that address")
	}
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email == "" {
		return nil, fmt.Errorf("token carries no email")
	}
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = email
	}
	return &Identity{
		Email:        email,
		Name:         name,
		HostedDomain: strings.ToLower(strings.TrimSpace(claims.HostedDomain)),
	}, nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

// keyForKID returns the cached signing key, refreshing the key set when the kid
// is unknown or the cache is older than an hour. Google rotates these keys.
func (v *GoogleVerifier) keyForKID(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key, ok := v.keys[kid]
	fresh := time.Since(v.fetchedAt) < time.Hour
	v.mu.RUnlock()
	if ok && fresh {
		return key, nil
	}
	if err := v.refresh(ctx); err != nil {
		if ok {
			return key, nil // stale key beats failing every login on a network blip
		}
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

func (v *GoogleVerifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleJWKSURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch google keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch google keys: http %d", resp.StatusCode)
	}

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decode google keys: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(new(big.Int).SetBytes(eBytes).Int64()),
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("google returned no usable RSA keys")
	}

	v.mu.Lock()
	v.keys, v.fetchedAt = keys, time.Now()
	v.mu.Unlock()
	return nil
}
