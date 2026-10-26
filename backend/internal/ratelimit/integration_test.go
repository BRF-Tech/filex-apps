//go:build integration

package ratelimit_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
)

func client(t *testing.T) *redis.Client {
	t.Helper()
	u := os.Getenv("FAPPS_TEST_REDIS_URL")
	if u == "" {
		t.Fatal("FAPPS_TEST_REDIS_URL is not set: the integration tests need a Redis 7 server")
	}
	opt, err := redis.ParseURL(u)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opt)
	t.Cleanup(func() { _ = c.Close() })
	if err := c.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRedisCountsAcrossLimiters(t *testing.T) {
	ctx := context.Background()
	c := client(t)
	a := &ratelimit.Redis{Client: c, Prefix: "t:"}
	b := &ratelimit.Redis{Client: c, Prefix: "t:"} // another process, the same counters
	for i := 1; i <= 4; i++ {
		l := a
		if i%2 == 0 {
			l = b
		}
		d, err := l.Allow(ctx, "k", 4, time.Minute)
		if err != nil || !d.Allowed || d.Count != i {
			t.Fatalf("request %d: %+v %v", i, d, err)
		}
	}
	d, err := a.Allow(ctx, "k", 4, time.Minute)
	if err != nil || d.Allowed || d.RetryAfter <= 0 || d.RetryAfter > time.Minute {
		t.Fatalf("5th request: %+v %v", d, err)
	}
	ttl := c.PTTL(ctx, "t:k").Val()
	if ttl <= 0 || ttl > time.Minute {
		t.Fatalf("the counter's expiry = %v", ttl)
	}
	if d, _ := a.Allow(ctx, "other", 4, time.Minute); !d.Allowed {
		t.Fatal("keys share a count")
	}
	short := &ratelimit.Redis{Client: c, Prefix: "s:"}
	_, _ = short.Allow(ctx, "k", 1, 100*time.Millisecond)
	if d, _ := short.Allow(ctx, "k", 1, 100*time.Millisecond); d.Allowed {
		t.Fatal("over the limit")
	}
	time.Sleep(150 * time.Millisecond)
	if d, _ := short.Allow(ctx, "k", 1, 100*time.Millisecond); !d.Allowed {
		t.Fatal("the window did not end")
	}
}

func TestUnreachableRedisFallsBackInProcess(t *testing.T) {
	l, closeFn, err := ratelimit.New("redis://127.0.0.1:1/0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if d, err := l.Allow(ctx, "k", 2, time.Minute); err != nil || !d.Allowed {
			t.Fatalf("request %d: %+v %v", i, d, err)
		}
	}
	if d, _ := l.Allow(ctx, "k", 2, time.Minute); d.Allowed {
		t.Fatal("the in-process fallback did not count")
	}
}
