//go:build integration

package index_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/db/dbtest"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestDemoKeysAreMadeOnceAndConfiguredKeysReplaceThem(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)

	none, err := index.LoadKeys(ctx, pool, config.KeyConfig{DemoKeys: true}, "production", quiet)
	if err != nil || none.Index != nil || none.Artifact != nil {
		t.Fatalf("production must not make demo keys: %+v %v", none, err)
	}
	k1, err := index.LoadKeys(ctx, pool, config.KeyConfig{DemoKeys: true}, "demo", quiet)
	if err != nil || k1.Index == nil || k1.Artifact == nil || !strings.HasPrefix(k1.Index.ID, "demo-index-") {
		t.Fatalf("demo keys: %+v %v", k1, err)
	}
	k2, err := index.LoadKeys(ctx, pool, config.KeyConfig{DemoKeys: true}, "demo", quiet)
	if err != nil || k2.Index.ID != k1.Index.ID || k2.Index.PublicHex() != k1.Index.PublicHex() || k2.Artifact.ID != k1.Artifact.ID {
		t.Fatal("a restart made new demo keys")
	}
	if k1.Index.PublicHex() == k1.Artifact.PublicHex() {
		t.Fatal("the index and artifact keys are the same key")
	}

	seed := hex.EncodeToString(bytes.Repeat([]byte{5}, 32))
	cfg := config.KeyConfig{IndexKey: seed, IndexKeyID: "idx-2026", DemoKeys: true}
	k3, err := index.LoadKeys(ctx, pool, cfg, "demo", quiet)
	if err != nil || k3.Index.ID != "idx-2026" || k3.Artifact.ID != k1.Artifact.ID {
		t.Fatalf("configured index key: %+v %v", k3, err)
	}
	keys, _ := store.ListKeys(ctx, pool)
	status := map[string]string{}
	for _, k := range keys {
		status[k.ID] = k.Status
	}
	if status["idx-2026"] != "active" || status[k1.Index.ID] != "retired" || status[k1.Artifact.ID] != "active" {
		t.Fatalf("key statuses = %v", status)
	}
	other := config.KeyConfig{IndexKey: hex.EncodeToString(bytes.Repeat([]byte{6}, 32)), IndexKeyID: "idx-2026"}
	if _, err := index.LoadKeys(ctx, pool, other, "demo", quiet); err == nil {
		t.Fatal("a key id was reused for another key")
	}
	if _, err := index.LoadKeys(ctx, pool, config.KeyConfig{IndexKey: seed, IndexKeyID: k1.Index.ID}, "demo", quiet); err == nil {
		t.Fatal("a retired key id was used again")
	}
}

func TestPublishSerialsCanonicalBytesAndRefresh(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	keys, err := index.LoadKeys(ctx, pool, config.KeyConfig{DemoKeys: true}, "development", quiet)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	pub := &index.Releaser{Pool: pool, Keys: keys, PublicURL: "https://fapps.example", TTL: 60 * 24 * time.Hour,
		RefreshBefore: 30 * 24 * time.Hour, Log: quiet, Now: func() time.Time { return now }}

	if rel, err := pub.Latest(ctx); err != nil || rel != nil {
		t.Fatalf("before the first release: %v %v", rel, err)
	}
	r1, err := pub.EnsureFresh(ctx)
	if err != nil || !r1.Published || r1.Release.Serial != 1 || r1.Release.Reason != "initial" {
		t.Fatalf("initial: %+v %v", r1, err)
	}
	if r, _ := pub.EnsureFresh(ctx); r.Published {
		t.Fatal("a fresh index was published again")
	}
	if r, _ := pub.Publish(ctx, index.ReasonListing, nil, false); r.Published || r.Release.Serial != 1 {
		t.Fatalf("nothing changed, yet serial %d was published", r.Release.Serial)
	}

	stored, _ := store.GetRelease(ctx, pool, 1)
	var doc index.Document
	if err := json.Unmarshal(stored.Body, &doc); err != nil {
		t.Fatal(err)
	}
	canon, _ := index.Canonical(doc)
	if !bytes.Equal(canon, stored.Body) || stored.SHA256 != signing.SHA256Hex(stored.Body) {
		t.Fatal("the stored body is not the canonical bytes it was signed as")
	}
	pk, _ := signing.ParsePublicKey(keys.Index.PublicHex())
	if err := signing.Verify([]ed25519.PublicKey{pk}, stored.SHA256, stored.Signature); err != nil {
		t.Fatal(err)
	}
	if doc.GeneratedAt != "2026-10-03T12:00:00Z" || doc.ExpiresAt != "2026-12-02T12:00:00Z" || len(doc.Keys) != 2 {
		t.Fatalf("document = %+v", doc)
	}

	// 31 days later fewer than 30 are left: the same content is re-signed.
	now = now.Add(31 * 24 * time.Hour)
	r2, err := pub.EnsureFresh(ctx)
	if err != nil || !r2.Published || r2.Release.Serial != 2 || r2.Release.Reason != "refresh" {
		t.Fatalf("refresh: %+v %v", r2, err)
	}
	if cached, _ := pub.Latest(ctx); cached == nil || cached.Serial != 2 {
		t.Fatalf("latest after refresh = %+v", cached)
	}

	// Concurrent publishers take distinct, gap-free serials.
	var wg sync.WaitGroup
	serials := make([]int64, 6)
	for i := range serials {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := pub.Publish(ctx, index.ReasonRefresh, nil, true)
			if err != nil {
				t.Error(err)
				return
			}
			serials[i] = r.Release.Serial
		}(i)
	}
	wg.Wait()
	sort.Slice(serials, func(i, j int) bool { return serials[i] < serials[j] })
	for i, s := range serials {
		if s != int64(3+i) {
			t.Fatalf("serials = %v, want 3..8", serials)
		}
	}

	noKey := &index.Releaser{Pool: pool, Keys: &index.Keyring{}, TTL: time.Hour}
	if _, err := noKey.Publish(ctx, index.ReasonRefresh, nil, true); err != index.ErrNoKey {
		t.Fatalf("publishing without an index key: %v", err)
	}
}
