package core

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// principalCtxKey carries the authenticated Principal through the request
// context. Unexported type so no other package can collide with it.
type principalCtxKey struct{}

func withPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFrom returns the authenticated principal for a request, if any.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalCtxKey{}).(*Principal)
	return p
}

// Management API roles. Mirrors config.ManagementRole* — core cannot import
// config (that would be an import cycle), so the values are duplicated and the
// mapping happens in cmd/cc-connect.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// sessionCookieName is the cookie holding a signed login session. It is
// HttpOnly so that XSS cannot read it, unlike a token in localStorage.
const sessionCookieName = "cc_session"

// defaultSessionTTL is how long a login lasts when not configured.
const defaultSessionTTL = 12 * time.Hour

// loginRateWindow throttles password guessing per client address.
const (
	loginRateWindow   = time.Minute
	loginRateMaxTries = 10
)

// Principal is the authenticated identity behind a management API request.
type Principal struct {
	Name string
	Role string
}

// IsViewer reports whether the principal is restricted to read-only access.
func (p *Principal) IsViewer() bool {
	return p != nil && p.Role == RoleViewer
}

// ManagementUserInfo is the wire type for a configured management account.
// cmd/cc-connect maps config.ManagementUser onto this.
type ManagementUserInfo struct {
	Name         string
	PasswordHash string
	Token        string
	Role         string
}

// managementAuth holds the account table and live login sessions.
type managementAuth struct {
	mu sync.RWMutex
	// users is keyed by lowercased name for case-insensitive login.
	users map[string]ManagementUserInfo
	// sessions maps session ID → issued session. Sessions live in memory only,
	// so a restart logs everyone out; that is intentional for a local admin UI.
	sessions map[string]*authSession
	// signingKey signs session cookies. Regenerated per process start.
	signingKey []byte
	ttl        time.Duration
	loginLimit *RateLimiter
}

type authSession struct {
	principal Principal
	expiresAt time.Time
}

func newManagementAuth(ttl time.Duration) *managementAuth {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// crypto/rand failing is unrecoverable; a predictable key would let
		// anyone forge sessions, so refuse to come up with a weak one.
		panic(fmt.Sprintf("management auth: generate signing key: %v", err))
	}
	return &managementAuth{
		users:      make(map[string]ManagementUserInfo),
		sessions:   make(map[string]*authSession),
		signingKey: key,
		ttl:        ttl,
		loginLimit: NewRateLimiter(loginRateMaxTries, loginRateWindow),
	}
}

// SetUsers replaces the configured account table.
func (a *managementAuth) SetUsers(users []ManagementUserInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.users = make(map[string]ManagementUserInfo, len(users))
	for _, u := range users {
		name := strings.TrimSpace(u.Name)
		if name == "" {
			continue
		}
		if u.Role != RoleViewer {
			u.Role = RoleAdmin
		}
		u.Name = name
		a.users[strings.ToLower(name)] = u
	}
}

// hasUsers reports whether any named account is configured.
func (a *managementAuth) hasUsers() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.users) > 0
}

// VerifyPassword checks a username/password pair against the account table.
// It always runs a bcrypt comparison — even for unknown users — so that the
// response time does not reveal whether a name exists.
func (a *managementAuth) VerifyPassword(name, password string) *Principal {
	a.mu.RLock()
	u, ok := a.users[strings.ToLower(strings.TrimSpace(name))]
	a.mu.RUnlock()

	hash := u.PasswordHash
	if !ok || hash == "" {
		// Dummy hash of "x"; comparison fails but costs the same as a real one.
		hash = "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if !ok || u.PasswordHash == "" || err != nil {
		return nil
	}
	return &Principal{Name: u.Name, Role: u.Role}
}

// verifyUserToken matches a bearer token against per-user API tokens.
func (a *managementAuth) verifyUserToken(token string) *Principal {
	if token == "" {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, u := range a.users {
		if u.Token != "" && subtle.ConstantTimeCompare([]byte(u.Token), []byte(token)) == 1 {
			return &Principal{Name: u.Name, Role: u.Role}
		}
	}
	return nil
}

// issueSession creates a signed session cookie value for the principal.
func (a *managementAuth) issueSession(p Principal) (value string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session id: %w", err)
	}
	id := hex.EncodeToString(raw)
	expires = time.Now().Add(a.ttl)

	a.mu.Lock()
	a.sessions[id] = &authSession{principal: p, expiresAt: expires}
	a.mu.Unlock()

	return id + "." + a.sign(id), expires, nil
}

// lookupSession resolves a cookie value back to its principal.
func (a *managementAuth) lookupSession(value string) *Principal {
	id, sig, ok := strings.Cut(value, ".")
	if !ok || id == "" {
		return nil
	}
	// Verify the signature before touching the session map so that forged or
	// malformed IDs never reach it.
	if subtle.ConstantTimeCompare([]byte(sig), []byte(a.sign(id))) != 1 {
		return nil
	}

	a.mu.RLock()
	s, exists := a.sessions[id]
	a.mu.RUnlock()
	if !exists {
		return nil
	}
	if time.Now().After(s.expiresAt) {
		a.mu.Lock()
		delete(a.sessions, id)
		a.mu.Unlock()
		return nil
	}
	p := s.principal
	return &p
}

// revokeSession drops a session, used by logout.
func (a *managementAuth) revokeSession(value string) {
	id, _, ok := strings.Cut(value, ".")
	if !ok {
		return
	}
	a.mu.Lock()
	delete(a.sessions, id)
	a.mu.Unlock()
}

func (a *managementAuth) sign(id string) string {
	mac := hmac.New(sha256.New, a.signingKey)
	mac.Write([]byte(id))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// allowLoginAttempt rate-limits login attempts per client address.
func (a *managementAuth) allowLoginAttempt(r *http.Request) bool {
	return a.loginLimit.Allow(clientIP(r))
}

func (a *managementAuth) stop() {
	if a.loginLimit != nil {
		a.loginLimit.Stop()
	}
}

// clientIP extracts a rate-limiting key from the request. RemoteAddr is used
// directly: proxy headers are attacker-controlled unless a trusted proxy is
// configured, and treating them as identity would let one client bypass the
// limit by varying the header.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if host, _, err := splitHostPortLenient(addr); err == nil {
		return host
	}
	return addr
}

func splitHostPortLenient(addr string) (host, port string, err error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr, "", nil
	}
	host, port = addr[:i], addr[i+1:]
	if _, convErr := strconv.Atoi(port); convErr != nil {
		return addr, "", nil
	}
	return strings.Trim(host, "[]"), port, nil
}

// HashManagementPassword produces a bcrypt hash for `cc-connect web adduser`.
func HashManagementPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}
