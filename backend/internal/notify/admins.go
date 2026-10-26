package notify

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/auth/adminrepo"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// AdminChecker says whether an account is an administrator now. known is
// false when it could not be told (GitHub did not answer): the caller waits
// and asks again, and in the end leaves that account out.
type AdminChecker interface {
	IsAdmin(ctx context.Context, account int64) (admin, known bool)
}

// RepoAdmins asks the role sources, as the sign-in does
// (internal/auth/adminrepo: the administration repository and the
// FAPPS_ADMINS list): an administrator whose permission was taken away
// stops hearing the administrators' events within TTL, without signing in
// again. Answers are kept for TTL (default 10 minutes).
type RepoAdmins struct {
	Pool  *pgxpool.Pool
	Roles *adminrepo.Checker
	TTL   time.Duration
	Now   func() time.Time

	mu    sync.Mutex
	cache map[int64]adminVerdict
}

type adminVerdict struct {
	admin bool
	at    time.Time
}

// IsAdmin implements AdminChecker.
func (a *RepoAdmins) IsAdmin(ctx context.Context, account int64) (bool, bool) {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	ttl := a.TTL
	if ttl == 0 {
		ttl = 10 * time.Minute
	}
	a.mu.Lock()
	if v, ok := a.cache[account]; ok && now().Sub(v.at) < ttl {
		a.mu.Unlock()
		return v.admin, true
	}
	a.mu.Unlock()
	if a.Roles == nil || !a.Roles.MayGrant() {
		// No role source on this server: nobody is an administrator.
		return false, true
	}
	id, login, err := store.GitHubIdentity(ctx, a.Pool, account)
	if errors.Is(err, store.ErrNotFound) {
		return false, true
	}
	if err != nil {
		return false, false
	}
	res := a.Roles.Resolve(ctx, id, login)
	admin := slices.Contains(res.Roles, domain.RoleAdmin)
	if !admin && res.Unknown {
		// The repository was not checked (GitHub away, the repository not
		// visible) and no list settles it: unknown.
		return false, false
	}
	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[int64]adminVerdict{}
	}
	a.cache[account] = adminVerdict{admin: admin, at: now()}
	a.mu.Unlock()
	return admin, true
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// mustJSON marshals data the package builds itself.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
