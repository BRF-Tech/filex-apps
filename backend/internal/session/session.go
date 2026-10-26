// Package session keeps the store's cookie sessions. The cookie
// (fapps_session: HttpOnly, SameSite=Lax, Secure when the store is served
// over HTTPS) carries a random id; the database keeps only its sha256, the
// CSRF token, the person's GitHub token sealed with a key derived from
// FAPPS_SESSION_KEY, and the store roles the sign-in read from GitHub with
// their source (fixed for the session's life). The package also seals the
// short-lived OAuth flow cookies (state, return path).
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// CookieName is the session cookie.
const CookieName = "fapps_session"

// Lifetimes (docs/ARCHITECTURE.md "Sessions"): a session with a store role
// (reviewer, administrator) is short.
const (
	PublisherLifetime  = 7 * 24 * time.Hour
	PrivilegedLifetime = 12 * time.Hour
)

// Principal is the signed-in person of one request.
type Principal struct {
	IDHash      []byte
	AccountID   int64
	IdentityID  int64
	Provider    string // always github
	Login       string
	Subject     string
	DisplayName string
	Email       string
	Roles       []domain.Role
	RoleSource  string // why it holds Roles (adminrepo.Result.Source)
	CSRFToken   string
	ExpiresAt   time.Time
	sealed      []byte
}

// Has reports a role. An administrator also holds the reviewer's rights.
func (p *Principal) Has(r domain.Role) bool {
	if p == nil {
		return false
	}
	if r == domain.RoleReviewer && slices.Contains(p.Roles, domain.RoleAdmin) {
		return true
	}
	return slices.Contains(p.Roles, r)
}

// IsGitHub reports a GitHub session (every live session is one).
func (p *Principal) IsGitHub() bool { return p != nil && p.Provider == "github" }

// Manager creates, reads and ends sessions.
type Manager struct {
	DB     db.Querier
	Secure bool // set the Secure flag (the store is served over HTTPS)
	token  *sealer
	flow   *sealer
}

// NewManager derives the sealing keys from the session secret.
func NewManager(q db.Querier, secret []byte, secure bool) (*Manager, error) {
	if len(secret) < 32 {
		return nil, errors.New("session secret shorter than 32 bytes")
	}
	t, err := newSealer(secret, purposeGitHubToken)
	if err != nil {
		return nil, err
	}
	f, err := newSealer(secret, purposeFlow)
	if err != nil {
		return nil, err
	}
	return &Manager{DB: q, Secure: secure, token: t, flow: f}, nil
}

// NewToken is 32 random bytes, URL-safe base64.
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashID is what the table stores for a cookie value.
func HashID(id string) []byte {
	sum := sha256.Sum256([]byte(id))
	return sum[:]
}

// New is a session to create.
type New struct {
	AccountID   int64
	IdentityID  int64
	Roles       []domain.Role // any role: the short lifetime
	RoleSource  string
	GitHubToken string // sealed into the row
	IP          string
	UserAgent   string
}

// Create stores a session and sets its cookie.
func (m *Manager) Create(ctx context.Context, w http.ResponseWriter, n New) error {
	id := NewToken()
	hash := HashID(id)
	life := PublisherLifetime
	if len(n.Roles) > 0 {
		life = PrivilegedLifetime
	}
	roles := make([]string, 0, len(n.Roles))
	for _, r := range n.Roles {
		roles = append(roles, string(r))
	}
	row := store.Session{
		IDHash: hash, AccountID: n.AccountID, IdentityID: n.IdentityID, CSRFToken: NewToken(),
		ExpiresAt: time.Now().Add(life), Roles: roles, RoleSource: n.RoleSource,
	}
	if n.GitHubToken != "" {
		sealed, err := m.token.seal([]byte(n.GitHubToken), hash)
		if err != nil {
			return err
		}
		row.GitHubSealed = sealed
	}
	if err := store.InsertSession(ctx, m.DB, row, n.IP, n.UserAgent); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: id, Path: "/", HttpOnly: true, Secure: m.Secure,
		SameSite: http.SameSiteLaxMode, Expires: row.ExpiresAt, MaxAge: int(life / time.Second),
	})
	return nil
}

// Load reads the request's session; nil (no error) when there is none, it
// is over, or it is not a GitHub session (the Keycloak sign-in is gone;
// migration 0005 deleted its sessions, this keeps any straggler out).
func (m *Manager) Load(ctx context.Context, r *http.Request) (*Principal, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" || len(c.Value) > 128 {
		return nil, nil
	}
	hash := HashID(c.Value)
	s, err := store.GetSession(ctx, m.DB, hash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.Provider != "github" {
		return nil, nil
	}
	_ = store.TouchSession(ctx, m.DB, hash)
	p := &Principal{
		IDHash: hash, AccountID: s.AccountID, IdentityID: s.IdentityID, Provider: s.Provider, Login: s.Login,
		Subject: s.Subject, DisplayName: s.DisplayName, Email: s.Email, CSRFToken: s.CSRFToken,
		ExpiresAt: s.ExpiresAt, RoleSource: s.RoleSource, sealed: s.GitHubSealed,
	}
	for _, r := range s.Roles {
		switch domain.Role(r) {
		case domain.RoleReviewer, domain.RoleAdmin:
			p.Roles = append(p.Roles, domain.Role(r))
		}
	}
	return p, nil
}

// GitHubToken opens the publisher's sealed GitHub token.
func (m *Manager) GitHubToken(p *Principal) (string, error) {
	if p == nil || len(p.sealed) == 0 {
		return "", ErrSealed
	}
	b, err := m.token.open(p.sealed, p.IDHash)
	return string(b), err
}

// Destroy deletes the session row and clears the cookie.
func (m *Manager) Destroy(ctx context.Context, w http.ResponseWriter, p *Principal) error {
	m.ClearCookie(w)
	if p == nil {
		return nil
	}
	return store.DeleteSession(ctx, m.DB, p.IDHash)
}

// ClearCookie removes the session cookie.
func (m *Manager) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: m.Secure,
		SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0)})
}

// CSRFValid compares the header token with the session's in constant time.
func CSRFValid(p *Principal, token string) bool {
	if p == nil || p.CSRFToken == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(p.CSRFToken), []byte(token)) == 1
}

// ---------------------------------------------------------------- OAuth flow cookies

// Flow is what a sign-in remembers between the redirect out and the
// callback: sealed into a cookie scoped to /auth/.
type Flow struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	ReturnTo string `json:"r,omitempty"`
	Expires  int64  `json:"e"`
}

// FlowTTL bounds how long a sign-in may take.
const FlowTTL = 10 * time.Minute

func flowCookie(provider string) string { return "fapps_flow_" + provider }

// StartFlow seals f into the provider's flow cookie.
func (m *Manager) StartFlow(w http.ResponseWriter, f Flow) error {
	f.Expires = time.Now().Add(FlowTTL).Unix()
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	sealed, err := m.flow.seal(b, []byte(f.Provider))
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie(f.Provider), Value: base64.RawURLEncoding.EncodeToString(sealed), Path: "/auth/",
		HttpOnly: true, Secure: m.Secure, SameSite: http.SameSiteLaxMode, MaxAge: int(FlowTTL / time.Second),
	})
	return nil
}

// ErrFlow is returned when the callback's flow cookie is missing, expired,
// tampered with, or its state does not match.
var ErrFlow = errors.New("sign-in flow invalid")

// FinishFlow opens the provider's flow cookie, checks the state the
// provider sent back, and clears the cookie (one use).
func (m *Manager) FinishFlow(w http.ResponseWriter, r *http.Request, provider, state string) (Flow, error) {
	http.SetCookie(w, &http.Cookie{Name: flowCookie(provider), Value: "", Path: "/auth/", HttpOnly: true,
		Secure: m.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0)})
	c, err := r.Cookie(flowCookie(provider))
	if err != nil || c.Value == "" {
		return Flow{}, ErrFlow
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return Flow{}, ErrFlow
	}
	b, err := m.flow.open(raw, []byte(provider))
	if err != nil {
		return Flow{}, ErrFlow
	}
	var f Flow
	if json.Unmarshal(b, &f) != nil || f.Provider != provider || time.Now().Unix() > f.Expires {
		return Flow{}, ErrFlow
	}
	if state == "" || subtle.ConstantTimeCompare([]byte(f.State), []byte(state)) != 1 {
		return Flow{}, ErrFlow
	}
	return f, nil
}

// SafeReturnTo keeps a return path only when it is a path on this store
// (one leading slash, no scheme or host, no backslash).
func SafeReturnTo(s string) string {
	if s == "" || len(s) > 512 || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") ||
		strings.ContainsAny(s, "\\\r\n\t") {
		return "/"
	}
	return s
}
