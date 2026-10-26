package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryAllowsTheLimitThenRefusesUntilTheWindowEnds(t *testing.T) {
	m := NewMemory()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		d, _ := m.Allow(ctx, "k", 3, time.Minute)
		if !d.Allowed || d.Count != i {
			t.Fatalf("request %d: %+v", i, d)
		}
	}
	now = now.Add(20 * time.Second)
	d, _ := m.Allow(ctx, "k", 3, time.Minute)
	if d.Allowed || d.RetryAfter != 40*time.Second {
		t.Fatalf("4th request: %+v, want refused with 40s left", d)
	}
	if d, _ := m.Allow(ctx, "other", 3, time.Minute); !d.Allowed {
		t.Fatal("another key shares the count")
	}
	now = now.Add(41 * time.Second)
	if d, _ := m.Allow(ctx, "k", 3, time.Minute); !d.Allowed || d.Count != 1 {
		t.Fatalf("new window: %+v", d)
	}
}

type broken struct{ calls int }

func (b *broken) Allow(context.Context, string, int, time.Duration) (Decision, error) {
	b.calls++
	return Decision{}, errors.New("redis down")
}

func TestFallbackCountsInProcessWhenRedisFails(t *testing.T) {
	p := &broken{}
	f := &Fallback{Primary: p, Secondary: NewMemory()}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		d, err := f.Allow(ctx, "k", 2, time.Minute)
		if err != nil || !d.Allowed {
			t.Fatalf("request %d: %+v %v", i, d, err)
		}
	}
	if d, _ := f.Allow(ctx, "k", 2, time.Minute); d.Allowed {
		t.Fatal("the fallback did not count")
	}
	if p.calls != 3 {
		t.Fatalf("redis asked %d times, want every time (it may come back)", p.calls)
	}
}

func TestCheckNamesTheRuleInTheKey(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	for i := 0; i < SignIn.Limit; i++ {
		if d, _ := Check(ctx, m, SignIn, "203.0.113.7"); !d.Allowed {
			t.Fatalf("sign-in %d refused", i+1)
		}
	}
	if d, _ := Check(ctx, m, SignIn, "203.0.113.7"); d.Allowed {
		t.Fatal("the 11th sign-in a minute was allowed")
	}
	if d, _ := Check(ctx, m, API, "203.0.113.7"); !d.Allowed {
		t.Fatal("the API limit shares the sign-in count")
	}
}

func TestTheStoreLimits(t *testing.T) {
	for _, c := range []struct {
		r      Rule
		limit  int
		window time.Duration
	}{
		{SignIn, 10, time.Minute}, {API, 120, time.Minute},
		{SubmitPerAccount, 10, time.Hour}, {SubmitPerPublisher, 30, 24 * time.Hour},
		{LicenseVerifyPerIP, 30, time.Minute}, {LicenseVerifyPerKey, 60, time.Hour},
		{InstallIntentPerAccount, 30, time.Hour}, {InstallReadPerIP, 60, time.Minute},
	} {
		if c.r.Limit != c.limit || c.r.Window != c.window {
			t.Errorf("%s = %d per %v, want %d per %v", c.r.Name, c.r.Limit, c.r.Window, c.limit, c.window)
		}
	}
}

func TestNewWithoutRedisIsInProcess(t *testing.T) {
	l, closeFn, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, ok := l.(*Memory); !ok {
		t.Fatalf("limiter = %T, want *Memory", l)
	}
	if _, _, err := New("not a url", nil); err == nil {
		t.Fatal("a malformed REDIS_URL was accepted")
	}
}
