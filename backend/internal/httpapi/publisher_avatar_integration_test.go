//go:build integration

package httpapi_test

import (
	"context"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// An app's publisher carries its mirrored avatar wherever the app is shown:
// the app's page, the catalog (and its file-type search), a publisher's app
// list. A publisher without one has no avatar_url (the page draws the
// initial). The owner's report, 2026-10-05: the app page showed the initial
// although the publisher's page had the logo.
func TestAnAppsPublisherCarriesItsAvatar(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.seedTyped(t, "brf-tech", "sign", "app", "Sign PDFs.", map[string]string{"0.3.0": manifestOf(t, "sign")})
	s.seedTyped(t, "acme", "docs", "app", "Word files.", map[string]string{"1.0.0": docxTool})
	if _, err := store.BackfillFileTypes(ctx, s.pool); err != nil {
		t.Fatal(err)
	}
	logo := pngOf(96, 96)
	if err := store.PutMedia(ctx, s.pool, store.Media{SHA256: sha(logo), ContentType: "image/png", Width: 96, Height: 96,
		SourceURL: "https://avatars.githubusercontent.com/u/159972728?s=256&v=4", Blob: logo}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPublisherProfile(ctx, s.pool, "brf-tech", "Home of filex.", sha(logo)); err != nil {
		t.Fatal(err)
	}
	want := s.srv.URL + "/v1/media/" + sha(logo) + ".png"

	type pub struct {
		ID        string  `json:"id"`
		AvatarURL *string `json:"avatar_url"`
	}
	anon := s.newClient()

	var page struct {
		Publisher pub `json:"publisher"`
	}
	anon.get("/api/v1/apps/sign").json(t, &page)
	if page.Publisher.AvatarURL == nil || *page.Publisher.AvatarURL != want {
		t.Fatalf("the app page's publisher: %+v, want avatar_url %s", page.Publisher, want)
	}
	var plain struct {
		Publisher pub `json:"publisher"`
	}
	anon.get("/api/v1/apps/docs").json(t, &plain)
	if plain.Publisher.ID != "acme" || plain.Publisher.AvatarURL != nil {
		t.Fatalf("a publisher without an avatar has no avatar_url: %+v", plain.Publisher)
	}

	var list struct {
		Items []struct {
			Name      string `json:"name"`
			Publisher pub    `json:"publisher"`
		} `json:"items"`
	}
	for _, path := range []string{"/api/v1/apps", "/api/v1/apps?q=.pdf", "/api/v1/apps?q=sign"} {
		list.Items = nil
		anon.get(path).json(t, &list)
		found := false
		for _, it := range list.Items {
			if it.Name == "sign" {
				found = true
				if it.Publisher.AvatarURL == nil || *it.Publisher.AvatarURL != want {
					t.Errorf("%s: sign's publisher %+v", path, it.Publisher)
				}
			}
			if it.Name == "docs" && it.Publisher.AvatarURL != nil {
				t.Errorf("%s: docs's publisher has an avatar it does not have: %+v", path, it.Publisher)
			}
		}
		if !found {
			t.Errorf("%s: sign is not listed", path)
		}
	}

	var pp struct {
		Apps []struct {
			Name      string `json:"name"`
			Publisher pub    `json:"publisher"`
		} `json:"apps"`
	}
	anon.get("/api/v1/publishers/brf-tech").json(t, &pp)
	if len(pp.Apps) != 1 || pp.Apps[0].Publisher.AvatarURL == nil || *pp.Apps[0].Publisher.AvatarURL != want {
		t.Fatalf("the publisher page's apps: %+v", pp.Apps)
	}
}
