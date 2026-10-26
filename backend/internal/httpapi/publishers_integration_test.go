//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/store"
)

type publisherPage struct {
	Items []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Verified    bool   `json:"verified"`
		Official    bool   `json:"official"`
		GitHubOwner string `json:"github_owner"`
		OwnerType   string `json:"owner_type"`
		Description string `json:"description"`
		AvatarURL   string `json:"avatar_url"`
		Apps        int    `json:"apps"`
	} `json:"items"`
	Total   int `json:"total"`
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
}

// The public publisher list answers anonymously: active publishers only,
// listed apps counted, the avatar at this store's media address.
func TestPublishersArePublicAndActiveOnly(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status, verified, official)
		 VALUES ('brf-tech', 'BRF Tech', 'BRF-Tech', 159972728, 'Organization', 'active', true, true)`,
		`INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status)
		 VALUES ('later', 'Later', 'later', 77, 'User', 'pending')`,
		`INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status)
		 VALUES ('gone', 'Gone', 'gone', 78, 'User', 'suspended')`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	icon := pngOf(128, 128)
	if err := store.PutMedia(ctx, s.pool, store.Media{SHA256: sha(icon), ContentType: "image/png", Width: 128, Height: 128,
		SourceURL: "https://avatars.githubusercontent.com/u/159972728?s=256&v=4", Blob: icon}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPublisherProfile(ctx, s.pool, "brf-tech", "Home of filex.", sha(icon)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertApp(ctx, s.pool, store.App{Name: "unapproved", Kind: "app", PublisherID: "brf-tech", RepoOwner: "BRF-Tech",
		RepoName: "unapproved", RepoID: 4242, Label: map[string]string{"en": "Unapproved"}}); err != nil {
		t.Fatal(err)
	}

	anon := s.newClient()
	r := anon.get("/api/v1/publishers")
	if r.status != http.StatusOK {
		t.Fatalf("anonymous list: %d %s", r.status, r.body)
	}
	var page publisherPage
	r.json(t, &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Page != 1 || page.PerPage != 50 {
		t.Fatalf("list %s", r.body)
	}
	p := page.Items[0]
	if p.ID != "brf-tech" || !p.Verified || !p.Official || p.GitHubOwner != "BRF-Tech" || p.OwnerType != "Organization" ||
		p.Description != "Home of filex." || p.Apps != 0 {
		t.Errorf("item %+v (an app without an approved version is not counted)", p)
	}
	if p.AvatarURL != s.srv.URL+"/v1/media/"+sha(icon)+".png" {
		t.Errorf("avatar_url %q: the mirror, never GitHub", p.AvatarURL)
	}
	if m := anon.get(strings.TrimPrefix(p.AvatarURL, s.srv.URL)); m.status != http.StatusOK || m.header.Get("Content-Type") != "image/png" {
		t.Errorf("the avatar is served: %d %s", m.status, m.header.Get("Content-Type"))
	}
	r = anon.get("/api/v1/publishers/brf-tech")
	var one struct {
		Publisher struct {
			Description string `json:"description"`
			AvatarURL   string `json:"avatar_url"`
		} `json:"publisher"`
	}
	if r.status != http.StatusOK || json.Unmarshal(r.body, &one) != nil || one.Publisher.Description != "Home of filex." || one.Publisher.AvatarURL != p.AvatarURL {
		t.Errorf("the publisher's page: %d %s", r.status, r.body)
	}

	for path, want := range map[string]int{
		"/api/v1/publishers?q=filex&sort=apps":    1,
		"/api/v1/publishers?q=later":              0,
		"/api/v1/publishers?q=gone":               0,
		"/api/v1/publishers?page=2&per_page=1":    0,
		"/api/v1/publishers?sort=name&per_page=1": 1,
	} {
		r := anon.get(path)
		var pg publisherPage
		r.json(t, &pg)
		if r.status != http.StatusOK || len(pg.Items) != want {
			t.Errorf("%s: %d, %d items, want %d", path, r.status, len(pg.Items), want)
		}
	}
	for _, path := range []string{"/api/v1/publishers?sort=newest", "/api/v1/publishers?per_page=101", "/api/v1/publishers?q=" + strings.Repeat("x", 101)} {
		if r := anon.get(path); r.status != http.StatusBadRequest || r.code(t) != "invalid_field" {
			t.Errorf("%s: %d %s", path, r.status, r.body)
		}
	}
}
