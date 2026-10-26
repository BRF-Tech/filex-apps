// Package ratelimit counts requests in fixed windows: in Redis when it is
// configured (counters shared by every process, gone when Redis is emptied -
// nothing is lost), otherwise, or while Redis fails, in this process.
package ratelimit

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Decision is one counted request.
type Decision struct {
	Allowed    bool
	Count      int
	RetryAfter time.Duration // until the window ends, when refused
}

// Limiter counts a request under key and decides whether it is within limit
// for the window.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (Decision, error)
}

// Rule is a named limit.
type Rule struct {
	Name   string
	Limit  int
	Window time.Duration
}

// The store's limits (docs/ARCHITECTURE.md "Security").
var (
	SignIn             = Rule{"signin", 10, time.Minute}
	API                = Rule{"api", 120, time.Minute}
	SubmitPerAccount   = Rule{"submit-account", 10, time.Hour}
	SubmitPerPublisher = Rule{"submit-publisher", 30, 24 * time.Hour}
	// License checks come from filex servers, about once a day each: per
	// address and per key (the key's HMAC, never the key).
	LicenseVerifyPerIP  = Rule{"license-verify-ip", 30, time.Minute}
	LicenseVerifyPerKey = Rule{"license-verify-key", 60, time.Hour}
	// Install links: made by a signed-in person, read and completed by a
	// filex server (the token is the secret; the limit only slows guessing).
	InstallIntentPerAccount = Rule{"install-intent", 30, time.Hour}
	InstallReadPerIP        = Rule{"install-read", 60, time.Minute}
	// Instance connections: codes made by a signed-in person, spent by a
	// filex server; a connected filex's signed requests, counted per address
	// before anything is checked and per instance for the intents it asks.
	ConnectCodePerAccount     = Rule{"connect-code", 10, time.Hour}
	InstanceConnectPerIP      = Rule{"instance-connect", 10, time.Minute}
	InstanceRequestPerIP      = Rule{"instance-request", 60, time.Minute}
	InstanceIntentPerInstance = Rule{"instance-intent", 30, time.Hour}
	// Notifications: a channel's test sends at once to an address the
	// person chose, so it is counted; adding channels too.
	NotifyTestPerAccount   = Rule{"notify-test", 20, time.Hour}
	NotifyChangePerAccount = Rule{"notify-change", 60, time.Hour}
)

// Check applies a rule to a subject (an address, an account id, ...).
func Check(ctx context.Context, l Limiter, r Rule, subject string) (Decision, error) {
	return l.Allow(ctx, r.Name+":"+subject, r.Limit, r.Window)
}

// ---------------------------------------------------------------- memory

// Memory is an in-process fixed-window limiter.
type Memory struct {
	mu    sync.Mutex
	now   func() time.Time
	wins  map[string]*window
	calls int
}

type window struct {
	start time.Time
	ttl   time.Duration
	n     int
}

// NewMemory makes an empty in-process limiter.
func NewMemory() *Memory { return &Memory{wins: map[string]*window{}, now: time.Now} }

// Allow counts and decides.
func (m *Memory) Allow(_ context.Context, key string, limit int, win time.Duration) (Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.calls++
	if m.calls%1024 == 0 {
		for k, w := range m.wins {
			if now.Sub(w.start) >= w.ttl {
				delete(m.wins, k)
			}
		}
	}
	w := m.wins[key]
	if w == nil || now.Sub(w.start) >= win {
		w = &window{start: now, ttl: win}
		m.wins[key] = w
	}
	w.n++
	d := Decision{Allowed: w.n <= limit, Count: w.n}
	if !d.Allowed {
		d.RetryAfter = w.start.Add(win).Sub(now)
	}
	return d, nil
}

// ---------------------------------------------------------------- redis

// incr counts in one round trip: the first request of a window sets its
// expiry; the answer is the count and the milliseconds left.
var incr = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then redis.call('PEXPIRE', KEYS[1], ARGV[1]); ttl = tonumber(ARGV[1]) end
return {n, ttl}
`)

// Redis is a limiter shared through Redis.
type Redis struct {
	Client *redis.Client
	Prefix string // e.g. "fapps:rl:"
}

// Allow counts and decides.
func (r *Redis) Allow(ctx context.Context, key string, limit int, win time.Duration) (Decision, error) {
	res, err := incr.Run(ctx, r.Client, []string{r.Prefix + key}, win.Milliseconds()).Int64Slice()
	if err != nil {
		return Decision{}, err
	}
	if len(res) != 2 {
		return Decision{}, redis.Nil
	}
	d := Decision{Allowed: res[0] <= int64(limit), Count: int(res[0])}
	if !d.Allowed {
		d.RetryAfter = time.Duration(res[1]) * time.Millisecond
	}
	return d, nil
}

// ---------------------------------------------------------------- fallback

// Fallback asks Primary and, when it fails, Secondary (logging the first
// failure of a run of failures).
type Fallback struct {
	Primary   Limiter
	Secondary Limiter
	Log       *slog.Logger
	failing   atomic.Bool
}

// Allow counts and decides.
func (f *Fallback) Allow(ctx context.Context, key string, limit int, win time.Duration) (Decision, error) {
	if f.Primary != nil {
		d, err := f.Primary.Allow(ctx, key, limit, win)
		if err == nil {
			if f.failing.Swap(false) && f.Log != nil {
				f.Log.Info("rate limiter: redis is back")
			}
			return d, nil
		}
		if !f.failing.Swap(true) && f.Log != nil {
			f.Log.Warn("rate limiter: redis failed, counting in process", "err", err)
		}
	}
	return f.Secondary.Allow(ctx, key, limit, win)
}

// New builds the server's limiter: Redis with an in-process fallback when
// redisURL is set, else in-process only.
func New(redisURL string, log *slog.Logger) (Limiter, func() error, error) {
	mem := NewMemory()
	if redisURL == "" {
		return mem, func() error { return nil }, nil
	}
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, nil, err
	}
	opt.DialTimeout, opt.ReadTimeout, opt.WriteTimeout = 2*time.Second, time.Second, time.Second
	c := redis.NewClient(opt)
	return &Fallback{Primary: &Redis{Client: c, Prefix: "fapps:rl:"}, Secondary: mem, Log: log}, c.Close, nil
}
