//go:build integration

package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/db/dbtest"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// publicWorld: four publishers in every standing and apps in every status.
//
//	acme  active     1 listed app with a version, plus an unlisted, a revoked and a versionless one
//	zeta  active     2 listed apps with a version; a description and a mirrored avatar
//	pend  pending    1 listed app (a publisher that is not active is not public)
//	susp  suspended  1 listed app
func publicWorld(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	acc, err := store.OperatorAccount(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InsertActiveKey(ctx, pool, store.SigningKey{ID: "art-1", Use: "artifact", PublicHex: strings.Repeat("a", 64)}); err != nil {
		t.Fatal(err)
	}
	for i, p := range []struct{ id, name, owner, status string }{
		{"acme", "Acme", "acme", "active"}, {"zeta", "Zeta", "zeta-org", "active"},
		{"pend", "Pending", "pend", "pending"}, {"susp", "Suspended", "susp", "suspended"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status)
			VALUES ($1, $2, $3, $4, 'Organization', $5)`, p.id, p.name, p.owner, 100+i, p.status); err != nil {
			t.Fatal(err)
		}
	}
	repoID := int64(5000)
	add := func(pub, name, status string, version bool) {
		repoID++
		appID, err := store.InsertApp(ctx, pool, store.App{Name: name, Kind: "app", PublisherID: pub, RepoOwner: pub, RepoName: name,
			RepoID: repoID, Label: map[string]string{"en": name}})
		if err != nil {
			t.Fatal(err)
		}
		switch status {
		case "unlisted":
			err = store.SetAppStatus(ctx, pool, appID, "unlisted")
		case "revoked":
			err = store.RevokeApp(ctx, pool, appID, "harmful")
		}
		if err != nil {
			t.Fatal(err)
		}
		if !version {
			return
		}
		sub, err := store.InsertSubmission(ctx, pool, pub, acc, pub+"/"+name, "v1.0.0", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.InsertVersion(ctx, pool, store.Version{AppID: appID, SubmissionID: sub, Version: "1.0.0", Ref: "v1.0.0",
			Commit: strings.Repeat("c", 40), ManifestURL: "https://m", ManifestSHA256: strings.Repeat("f", 64), ManifestSig: "sig",
			Manifest: json.RawMessage(`{}`), ArtifactKeyID: "art-1"}); err != nil {
			t.Fatal(err)
		}
	}
	add("acme", "a-listed", "listed", true)
	add("acme", "a-unlisted", "unlisted", true)
	add("acme", "a-revoked", "revoked", true)
	add("acme", "a-empty", "listed", false)
	add("zeta", "z-one", "listed", true)
	add("zeta", "z-two", "listed", true)
	add("pend", "p-one", "listed", true)
	add("susp", "s-one", "listed", true)

	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 64)))
	sum := sha256.Sum256(b.Bytes())
	sha := hex.EncodeToString(sum[:])
	if err := store.PutMedia(ctx, pool, store.Media{SHA256: sha, ContentType: "image/png", Width: 64, Height: 64, SourceURL: "https://avatars.githubusercontent.com/u/101", Blob: b.Bytes()}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPublisherProfile(ctx, pool, "zeta", "Diagrams and other drawings.", sha); err != nil {
		t.Fatal(err)
	}
	return pool, sha
}

func rows(ps []store.PublicPublisher) string {
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s:%d", p.ID, p.Apps))
	}
	return strings.Join(out, " ")
}

func TestPublicPublishers_ActiveOnlyWithTheirListedApps(t *testing.T) {
	ctx := context.Background()
	pool, sha := publicWorld(t)

	got, total, err := store.ListPublicPublishers(ctx, pool, store.PublicPublisherFilter{}, store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if rows(got) != "acme:1 zeta:2" || total != 2 {
		t.Fatalf("by name: %s (total %d); want acme:1 zeta:2 - pending and suspended publishers absent, unlisted/revoked/versionless apps not counted", rows(got), total)
	}
	if z := got[1]; z.Description != "Diagrams and other drawings." || z.AvatarSHA256 != sha || z.AvatarType != "image/png" || z.GitHubOwner != "zeta-org" {
		t.Errorf("zeta's profile %+v", z.Publisher)
	}

	got, _, _ = store.ListPublicPublishers(ctx, pool, store.PublicPublisherFilter{Sort: "apps"}, store.Page{})
	if rows(got) != "zeta:2 acme:1" {
		t.Errorf("by apps: %s", rows(got))
	}
	for q, want := range map[string]string{"diagrams": "zeta:2", "ACME": "acme:1", "zeta-org": "zeta:2", "pend": "", "acme drawings": ""} {
		got, total, _ := store.ListPublicPublishers(ctx, pool, store.PublicPublisherFilter{Words: strings.Fields(q)}, store.Page{})
		if rows(got) != want || total != len(got) {
			t.Errorf("q %q: %s (total %d), want %q", q, rows(got), total, want)
		}
	}
	got, total, _ = store.ListPublicPublishers(ctx, pool, store.PublicPublisherFilter{}, store.Page{Page: 2, PerPage: 1})
	if rows(got) != "zeta:2" || total != 2 {
		t.Errorf("page 2 of 1: %s (total %d)", rows(got), total)
	}

	p, err := store.GetPublisher(ctx, pool, "zeta")
	if err != nil || p.Description != "Diagrams and other drawings." || p.AvatarSHA256 != sha || p.AvatarType != "image/png" {
		t.Errorf("GetPublisher carries the profile: %+v %v", p, err)
	}
	if err := store.SetPublisherProfile(ctx, pool, "zeta", "", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := store.GetPublisher(ctx, pool, "zeta"); p.Description != "" || p.AvatarSHA256 != "" || p.AvatarType != "" {
		t.Errorf("a cleared profile: %+v", p)
	}
}
