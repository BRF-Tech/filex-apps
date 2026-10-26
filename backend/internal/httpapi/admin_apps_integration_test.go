//go:build integration

package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/store"
)

type adminAppsPage struct {
	Items []struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		RevokedAt string `json:"revoked_at"`
	} `json:"items"`
	Total   int `json:"total"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
}

func (p adminAppsPage) names() []string {
	out := []string{}
	for _, i := range p.Items {
		out = append(out, i.Name+":"+i.Status)
	}
	return out
}

func TestAdminListsAppsInEveryStatus(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx, `INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status)
		VALUES ('acme', 'Acme', 'acme', 77, 'Organization', 'active')`); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"alpha", "bravo", "charlie", "delta"} {
		if _, err := store.InsertApp(ctx, s.pool, store.App{Name: name, Kind: "app", PublisherID: "acme", RepoOwner: "acme",
			RepoName: "filex-" + name, RepoID: int64(100 + i), Label: map[string]string{"en": "App " + name}}); err != nil {
			t.Fatal(err)
		}
	}
	bravo, _ := store.GetAppByName(ctx, s.pool, "bravo")
	charlie, _ := store.GetAppByName(ctx, s.pool, "charlie")
	delta, _ := store.GetAppByName(ctx, s.pool, "delta")
	if err := store.SetAppStatus(ctx, s.pool, bravo.ID, "unlisted"); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeApp(ctx, s.pool, charlie.ID, "harmful"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAppStatus(ctx, s.pool, delta.ID, "unlisted"); err != nil {
		t.Fatal(err)
	}

	anon := s.newClient()
	if r := anon.get("/api/v1/admin/apps"); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", r.status, r.body)
	}

	alice := s.gh.AddUser(1, "alice", "Alice")
	s.gh.SignInAs(alice)
	pubc := s.newClient()
	pubc.signIn("/auth/github/login")
	if r := pubc.get("/api/v1/admin/apps"); r.status != http.StatusForbidden || r.code(t) != "forbidden" {
		t.Fatalf("publisher: %d %s", r.status, r.body)
	}

	reviewer := s.signInWithRole(2, "rev", "triage")
	admin := s.signInWithRole(3, "adm", "maintain")

	for who, c := range map[string]*client{"reviewer": reviewer, "admin": admin} {
		var all adminAppsPage
		r := c.get("/api/v1/admin/apps")
		if r.status != http.StatusOK {
			t.Fatalf("%s: %d %s", who, r.status, r.body)
		}
		r.json(t, &all)
		if fmt.Sprint(all.names()) != "[alpha:listed bravo:unlisted charlie:revoked delta:unlisted]" || all.Total != 4 {
			t.Fatalf("%s all = %v total %d", who, all.names(), all.Total)
		}
		if all.Items[2].RevokedAt == "" || all.Items[0].RevokedAt != "" {
			t.Fatalf("revoked_at: %+v", all.Items)
		}
	}

	check := func(path, want string, total int) {
		t.Helper()
		var p adminAppsPage
		r := admin.get(path)
		if r.status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, r.status, r.body)
		}
		r.json(t, &p)
		if fmt.Sprint(p.names()) != want || p.Total != total {
			t.Fatalf("%s = %v total %d, want %s total %d", path, p.names(), p.Total, want, total)
		}
	}
	check("/api/v1/admin/apps?status=unlisted", "[bravo:unlisted delta:unlisted]", 2)
	check("/api/v1/admin/apps?status=revoked", "[charlie:revoked]", 1)
	check("/api/v1/admin/apps?status=listed", "[alpha:listed]", 1)
	check("/api/v1/admin/apps?status=all&q=ravo", "[bravo:unlisted]", 1)
	check("/api/v1/admin/apps?per_page=2&page=2", "[charlie:revoked delta:unlisted]", 4)
	check("/api/v1/admin/apps?per_page=2&page=3", "[]", 4)

	for _, bad := range []string{"?status=gone", "?sort=price", "?page=0"} {
		if r := admin.get("/api/v1/admin/apps" + bad); r.status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, r.status, r.body)
		}
	}
	var pub struct{ Items []struct{ Name string } }
	admin.get("/api/v1/apps").json(t, &pub)
	if len(pub.Items) != 0 {
		t.Fatalf("the public list changed: %+v", pub.Items)
	}
}
