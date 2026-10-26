//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/db/dbtest"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

func signIn(t *testing.T, pool *pgxpool.Pool, in store.SignIn) (store.Account, store.Identity) {
	t.Helper()
	var acc store.Account
	var id store.Identity
	err := db.InTx(context.Background(), pool, func(tx pgx.Tx) error {
		var err error
		acc, id, err = store.SignInIdentity(context.Background(), tx, in)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return acc, id
}

func TestIdentitiesAreNeverMergedByEmail(t *testing.T) {
	pool := dbtest.Migrated(t)
	a1, i1 := signIn(t, pool, store.SignIn{Provider: "github", Issuer: "https://github.com", Subject: "1", Login: "alice", Email: "a@example"})
	a2, i2 := signIn(t, pool, store.SignIn{Provider: "github", Issuer: "https://github.com", Subject: "1", Login: "alice-renamed", Email: "a@example"})
	if a1.ID != a2.ID || i1.ID != i2.ID || i2.Login != "alice-renamed" {
		t.Fatalf("the same identity made a new account: %d %d", a1.ID, a2.ID)
	}
	a3, _ := signIn(t, pool, store.SignIn{Provider: "github", Issuer: "https://github.com", Subject: "2", Login: "alice-two", Email: "a@example"})
	if a3.ID == a1.ID {
		t.Fatal("two identities with the same e-mail share an account")
	}
	_, err := pool.Exec(context.Background(), `UPDATE accounts SET disabled_at = now() WHERE id = $1`, a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTx(context.Background(), pool, func(tx pgx.Tx) error {
		_, _, err := store.SignInIdentity(context.Background(), tx, store.SignIn{Provider: "github", Issuer: "https://github.com", Subject: "1", Login: "alice"})
		return err
	})
	if !errors.Is(err, store.ErrAccountDisabled) {
		t.Fatalf("a disabled account signed in: %v", err)
	}
}

func TestSessionsExpireAndDieWithTheAccount(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	acc, id := signIn(t, pool, store.SignIn{Provider: "github", Issuer: "https://github.com", Subject: "s", Login: "rev"})
	live := store.Session{IDHash: make([]byte, 32), AccountID: acc.ID, IdentityID: id.ID, CSRFToken: "c", ExpiresAt: time.Now().Add(time.Hour),
		Roles: []string{"reviewer"}, RoleSource: "github repo permission o/r: triage"}
	live.IDHash[0] = 1
	old := live
	old.IDHash = make([]byte, 32)
	old.ExpiresAt = time.Now().Add(-time.Minute)
	for _, s := range []store.Session{live, old} {
		if err := store.InsertSession(ctx, pool, s, "203.0.113.4", "test"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetSession(ctx, pool, live.IDHash)
	if err != nil || got.Provider != "github" || got.Login != "rev" || len(got.Roles) != 1 || got.Roles[0] != "reviewer" ||
		got.RoleSource != "github repo permission o/r: triage" || got.CSRFToken != "c" {
		t.Fatalf("session = %+v %v", got, err)
	}
	if _, err := store.GetSession(ctx, pool, old.IDHash); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("an expired session was returned")
	}
	if n, _ := store.DeleteExpiredSessions(ctx, pool); n != 1 {
		t.Fatalf("expired sessions deleted: %d", n)
	}
	if n, _ := store.DeleteSessionsOf(ctx, pool, []int64{acc.ID}); n != 1 {
		t.Fatalf("sessions of the account deleted: %d", n)
	}
}

// fixture writes a publisher, an app, a key and an approved version.
func fixture(t *testing.T, pool *pgxpool.Pool) (store.Publisher, store.App, store.Version) {
	t.Helper()
	ctx := context.Background()
	acc, _ := signIn(t, pool, store.SignIn{Provider: "github", Issuer: "https://github.com", Subject: "1", Login: "alice"})
	var pub store.Publisher
	var app store.App
	var v store.Version
	err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		if pub, err = store.CreatePublisher(ctx, tx, store.Publisher{ID: "alice-pub", Name: "Alice", GitHubOwner: "alice", GitHubOwnerID: 1, OwnerType: "User"}, acc.ID); err != nil {
			return err
		}
		if err := store.InsertActiveKey(ctx, tx, store.SigningKey{ID: "art-1", Use: "artifact", PublicHex: strings.Repeat("a", 64)}); err != nil {
			return err
		}
		subID, err := store.InsertSubmission(ctx, tx, pub.ID, acc.ID, "alice/hello", "v1.0.0", "")
		if err != nil {
			return err
		}
		app.ID, err = store.InsertApp(ctx, tx, store.App{Name: "hello", Kind: "app", PublisherID: pub.ID, RepoOwner: "alice", RepoName: "hello", RepoID: 9001,
			Label: map[string]string{"en": "Hello"}})
		if err != nil {
			return err
		}
		v = store.Version{AppID: app.ID, SubmissionID: subID, Version: "1.0.0", Ref: "v1.0.0", Commit: strings.Repeat("c", 40),
			ManifestURL: "https://m", ManifestSHA256: strings.Repeat("f", 64), ManifestSig: "sig", Manifest: json.RawMessage(`{}`),
			ArtifactKeyID: "art-1"}
		v.ID, err = store.InsertVersion(ctx, tx, v)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return pub, app, v
}

func TestPublisherConflictsAndMembership(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	pub, _, _ := fixture(t, pool)
	acc, _ := signIn(t, pool, store.SignIn{Provider: "github", Issuer: "https://github.com", Subject: "2", Login: "bob"})
	for name, p := range map[string]store.Publisher{
		"same id":    {ID: pub.ID, Name: "X", GitHubOwner: "bob", GitHubOwnerID: 2, OwnerType: "User"},
		"same owner": {ID: "bob-pub", Name: "X", GitHubOwner: "alice", GitHubOwnerID: 1, OwnerType: "User"},
	} {
		err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := store.CreatePublisher(ctx, tx, p, acc.ID)
			return err
		})
		if !errors.Is(err, store.ErrConflict) {
			t.Errorf("%s: err = %v, want ErrConflict", name, err)
		}
	}
	ms, err := store.MembershipsOf(ctx, pool, 1)
	if err != nil || len(ms) != 1 || ms[0].Role != "owner" || ms[0].Status != "pending" {
		t.Fatalf("memberships = %+v %v", ms, err)
	}
}

func TestOneOpenSubmissionPerRepositoryAndTag(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	pub, _, _ := fixture(t, pool)
	first, err := store.InsertSubmission(ctx, pool, pub.ID, 1, "Alice/World", "v2", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.InsertSubmission(ctx, pool, pub.ID, 1, "alice/world", "v2", "")
	var open *store.OpenSubmissionError
	if !errors.As(err, &open) || open.ID != first || !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second open submission: %v", err)
	}
	if ok, _ := store.Withdraw(ctx, pool, first); !ok {
		t.Fatal("withdraw")
	}
	if _, err := store.InsertSubmission(ctx, pool, pub.ID, 1, "alice/world", "v2", ""); err != nil {
		t.Fatalf("after a withdrawal a new submission is allowed: %v", err)
	}
	if ok, _ := store.Transition(ctx, pool, first, 1, []string{"queued"}, "resolving"); ok {
		t.Fatal("a withdrawn submission moved")
	}
}

func TestApprovedVersionsAndIndexReleasesAreImmutable(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	_, _, v := fixture(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE app_versions SET manifest_sha256 = $2 WHERE id = $1`, v.ID, strings.Repeat("0", 64)); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("a version's hash changed: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM app_versions WHERE id = $1`, v.ID); err == nil {
		t.Fatal("a version was deleted")
	}
	if err := store.YankVersion(ctx, pool, v.ID, "bad", 1); err != nil {
		t.Fatalf("the yank fields stay writable: %v", err)
	}
	if err := store.UnyankVersion(ctx, pool, v.ID); err != nil {
		t.Fatalf("unyank: %v", err)
	}
	rel := store.Release{Serial: 1, GeneratedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), SHA256: strings.Repeat("e", 64),
		Signature: "s", KeyID: "art-1", Body: []byte("{}\n"), Reason: "initial"}
	if err := store.InsertRelease(ctx, pool, rel); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE index_releases SET body = 'x'`); err == nil {
		t.Fatal("a served index was rewritten")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM index_releases`); err == nil {
		t.Fatal("a served index was deleted")
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET action = 'x'`); err != nil {
		t.Fatalf("no audit rows yet, the update touches none: %v", err)
	}
}

func TestJobQueueClaimsEachJobOnce(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	for i := 0; i < 20; i++ {
		if _, err := store.Enqueue(ctx, pool, store.JobResolve, map[string]int{"submission_id": i, "attempt": 1}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Enqueue(ctx, pool, store.JobResolve, map[string]int{"submission_id": 99}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, err := store.Claim(ctx, pool, []string{store.JobResolve}, "w", time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if j == nil {
					return
				}
				mu.Lock()
				seen[j.ID]++
				mu.Unlock()
				_ = store.CompleteJob(ctx, pool, j.ID)
			}
		}()
	}
	wg.Wait()
	if len(seen) != 20 {
		t.Fatalf("claimed %d jobs, want 20 (the delayed one waits)", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("job %d claimed %d times", id, n)
		}
	}

	// A worker that dies leaves its job to the reaper.
	id, _ := store.Enqueue(ctx, pool, store.JobDryRun, map[string]int{"submission_id": 1}, time.Time{})
	if j, _ := store.Claim(ctx, pool, []string{store.JobDryRun}, "dead", time.Millisecond); j == nil || j.ID != id || j.Attempts != 1 {
		t.Fatalf("claim = %+v", j)
	}
	time.Sleep(20 * time.Millisecond)
	if n, _ := store.RequeueExpired(ctx, pool); n != 1 {
		t.Fatalf("requeued %d", n)
	}
	if j, _ := store.Claim(ctx, pool, []string{store.JobDryRun}, "alive", time.Minute); j == nil || j.ID != id || j.Attempts != 2 {
		t.Fatalf("reclaim = %+v", j)
	}
	if err := store.RetryJob(ctx, pool, id, "network", time.Hour); err != nil {
		t.Fatal(err)
	}
	if j, _ := store.Claim(ctx, pool, []string{store.JobDryRun}, "w", time.Minute); j != nil {
		t.Fatal("a job waiting for its retry time was claimed")
	}
	if n, _ := store.CancelSubmissionJobs(ctx, pool, 1); n != 1 {
		t.Fatalf("cancelled %d", n)
	}
}

// A listing keeps the kind it was created with: an approval that would turn
// an app into a storage plugin (or back) changes nothing.
func TestUpdateAppListing_KeepsTheKind(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	_, app, _ := fixture(t, pool)
	listed, err := store.GetAppByID(ctx, pool, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	turned := listed
	turned.Kind = "storage"
	turned.Homepage = "https://example.org/turned"
	err = db.InTx(ctx, pool, func(tx pgx.Tx) error { return store.UpdateAppListing(ctx, tx, turned) })
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("want ErrConflict for a change of kind, got %v", err)
	}
	got, err := store.GetAppByID(ctx, pool, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "app" || got.Homepage == turned.Homepage {
		t.Fatalf("the refused update changed the listing: kind %q, homepage %q", got.Kind, got.Homepage)
	}
	same := got
	same.Homepage = "https://example.org/hello"
	if err := db.InTx(ctx, pool, func(tx pgx.Tx) error { return store.UpdateAppListing(ctx, tx, same) }); err != nil {
		t.Fatalf("an update of the same kind: %v", err)
	}
}
