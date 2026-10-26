package index

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/signing"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

func TestCanonicalBytes(t *testing.T) {
	type inner struct {
		Z string `json:"z"`
		A int    `json:"a"`
	}
	v := map[string]any{"b": []any{}, "a": inner{Z: "<b>&</b>", A: 1}, "c": map[string]any{}}
	got, err := Canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"a\": {\n    \"a\": 1,\n    \"z\": \"<b>&</b>\"\n  },\n  \"b\": [],\n  \"c\": {}\n}\n"
	if string(got) != want {
		t.Fatalf("canonical bytes:\n%s\nwant:\n%s", got, want)
	}
	again, _ := Canonical(v)
	if !bytes.Equal(got, again) {
		t.Fatal("the same value gave different bytes")
	}
}

// The order is filex's own (update.Compare): a pre-release ranks below its
// release, and two pre-releases compare as text - so beta.11 is below beta.2
// here, unlike strict semver. filex's update notices see the same order.
func TestCompareVersions(t *testing.T) {
	ordered := []string{"0.9.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.11",
		"1.0.0-beta.2", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.2.0", "1.10.0", "2.0.0"}
	for i := 0; i < len(ordered); i++ {
		for j := 0; j < len(ordered); j++ {
			got := CompareVersions(ordered[i], ordered[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got != want {
				t.Errorf("CompareVersions(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	if CompareVersions("1.0.0+build.5", "1.0.0") != 0 || CompareVersions("v1.2.3", "1.2.3") != 0 {
		t.Error("build metadata or a leading v changed the order")
	}
	if CompareVersions("banana", "0.0.1") != -1 || CompareVersions("1.0", "0.0.1") != -1 {
		t.Error("an invalid version must sort below every valid one")
	}
	for _, bad := range []string{"1.0", "1.0.0.0", "x.y.z", "1.-1.0", ""} {
		if ValidVersion(bad) {
			t.Errorf("ValidVersion(%q) = true", bad)
		}
	}
	for _, good := range []string{"1.0.0", "v0.7.6", "0.1.0-dev", "1.0.0+build7"} {
		if !ValidVersion(good) {
			t.Errorf("ValidVersion(%q) = false", good)
		}
	}
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func snapshot() store.IndexSnapshot {
	yanked := ts("2026-10-02T10:00:00Z")
	revoked := ts("2026-10-03T08:30:00Z")
	return store.IndexSnapshot{
		Keys: []store.SigningKey{
			{ID: "idx-2", Use: "index", PublicHex: strings.Repeat("b", 64), Status: "next"},
			{ID: "art-1", Use: "artifact", PublicHex: strings.Repeat("c", 64), Status: "active"},
			{ID: "idx-1", Use: "index", PublicHex: strings.Repeat("a", 64), Status: "active"},
		},
		Publishers: []store.Publisher{
			{ID: "zeta", Name: "Zeta", GitHubOwner: "zeta-org"},
			{ID: "brf-tech", Name: "BRF Tech", GitHubOwner: "BRF-Tech", Verified: true, Official: true},
		},
		Apps: []store.IndexApp{
			{
				App: store.App{ID: 2, Name: "sign", Kind: "app", PublisherID: "brf-tech", RepoOwner: "BRF-Tech", RepoName: "filex-sign",
					Categories: []string{"documents"}, Label: map[string]string{"en": "Sign", "tr": "İmza"},
					Summary: map[string]string{"en": "Sign PDFs"}, IconSHA256: strings.Repeat("1", 64), Status: "listed"},
				IconType: "image/png",
				Screenshots: []store.AppMedia{
					{SHA256: strings.Repeat("3", 64), ContentType: "image/webp", Position: 1},
					{SHA256: strings.Repeat("2", 64), ContentType: "image/jpeg", Position: 0, Caption: map[string]string{"en": "Wizard"}},
				},
				Versions: []store.Version{
					{ID: 10, Version: "0.2.0", Ref: "v0.2.0", Commit: strings.Repeat("e", 40), FilexRange: ">=0.49.0",
						ManifestURL: "https://example/m", ManifestSHA256: strings.Repeat("f", 64), ManifestSig: "s1",
						PublishedAt: ts("2026-09-01T00:00:00Z"), Permissions: []string{"files:read"}, Engines: []string{}},
					{ID: 12, Version: "0.10.0", Ref: "v0.10.0", Commit: strings.Repeat("d", 40), FilexRange: ">=0.50.0",
						ManifestURL: "https://example/m2", ManifestSHA256: strings.Repeat("9", 64), ManifestSig: "s2",
						Wasm:        &store.Artifact{URL: "https://example/w", SHA256: strings.Repeat("8", 64), Size: 42, Sig: "s3"},
						PublishedAt: ts("2026-10-01T12:00:00Z"), Permissions: []string{}, Engines: []string{"ffmpeg"},
						Languages: json.RawMessage(`[{"code":"tr","percent":100},{"code":"en","percent":100}]`),
						YankedAt:  &yanked, YankedReason: "crashes"},
					{ID: 11, Version: "0.3.0", Ref: "v0.3.0", Commit: strings.Repeat("c", 40),
						ManifestURL: "https://example/m3", ManifestSHA256: strings.Repeat("7", 64), ManifestSig: "s4",
						PublishedAt: ts("2026-09-15T00:00:00Z")},
				},
			},
			{
				App:      store.App{ID: 1, Name: "convert", Kind: "app", PublisherID: "zeta", RepoOwner: "zeta-org", RepoName: "convert", Status: "revoked", RevokedReason: "malware", RevokedAt: &revoked},
				Versions: []store.Version{{ID: 1, Version: "1.0.0", Ref: "v1", Commit: strings.Repeat("a", 40), ManifestSHA256: strings.Repeat("6", 64), PublishedAt: ts("2026-08-01T00:00:00Z")}},
			},
		},
	}
}

func TestBuildIsOrderedAndComplete(t *testing.T) {
	doc := Build(snapshot(), "https://fapps.example")
	if doc.Schema != 1 {
		t.Fatalf("schema = %d", doc.Schema)
	}
	if len(doc.Keys) != 3 || doc.Keys[0].ID != "art-1" || doc.Keys[1].ID != "idx-1" || doc.Keys[2].ID != "idx-2" {
		t.Fatalf("keys not sorted by use then id: %+v", doc.Keys)
	}
	if doc.Publishers[0].ID != "brf-tech" || doc.Publishers[0].GitHub != "BRF-Tech" || !doc.Publishers[0].Official {
		t.Fatalf("publishers = %+v", doc.Publishers)
	}
	if doc.Apps[0].Name != "convert" || doc.Apps[1].Name != "sign" {
		t.Fatalf("apps not sorted by name: %s, %s", doc.Apps[0].Name, doc.Apps[1].Name)
	}
	conv := doc.Apps[0]
	if conv.Revoked == nil || conv.Revoked.Reason != "malware" || conv.Revoked.At != "2026-10-03T08:30:00Z" {
		t.Fatalf("revoked = %+v", conv.Revoked)
	}
	sign := doc.Apps[1]
	var order []string
	for _, v := range sign.Versions {
		order = append(order, v.Version)
	}
	if strings.Join(order, ",") != "0.10.0,0.3.0,0.2.0" {
		t.Fatalf("versions not newest first by semver: %v", order)
	}
	if sign.Latest != "0.3.0" {
		t.Fatalf("latest = %q, want 0.3.0 (0.10.0 is yanked)", sign.Latest)
	}
	if y := sign.Versions[0].Yanked; y == nil || y.Reason != "crashes" {
		t.Fatalf("yanked = %+v", y)
	}
	if sign.Versions[1].Yanked != nil || sign.Revoked != nil {
		t.Fatal("not-yanked / not-revoked must be null")
	}
	if sign.Repo != "BRF-Tech/filex-sign" || sign.Icon == nil || sign.Icon.URL != "https://fapps.example/v1/media/"+strings.Repeat("1", 64)+".png" {
		t.Fatalf("repo/icon = %s %+v", sign.Repo, sign.Icon)
	}
	if len(sign.Screenshots) != 2 || !strings.HasSuffix(sign.Screenshots[0].URL, ".jpg") || sign.Screenshots[0].Caption["en"] != "Wizard" {
		t.Fatalf("screenshots = %+v", sign.Screenshots)
	}
	v := sign.Versions[0]
	if v.Wasm == nil || v.Wasm.Sig != "s3" || v.UI != nil || v.PublishedAt != "2026-10-01" {
		t.Fatalf("version 0.10.0 = %+v", v)
	}
	if len(v.Languages) != 2 || v.Languages[0].Code != "en" {
		t.Fatalf("languages not sorted: %+v", v.Languages)
	}

	body, err := Canonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{`"wasm": null`, `"ui": null`, `"yanked": null`, `"revoked": null`, `"permissions": []`, `"engines": []`} {
		if !bytes.Contains(body, []byte(must)) {
			t.Errorf("canonical body lacks %s", must)
		}
	}
}

func TestBuildOfAnEmptyCatalogHasEmptyListsNotNull(t *testing.T) {
	body, err := Canonical(Build(store.IndexSnapshot{}, "https://fapps.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{`"keys": []`, `"publishers": []`, `"apps": []`} {
		if !bytes.Contains(body, []byte(must)) {
			t.Errorf("empty index lacks %s:\n%s", must, body)
		}
	}
}

func TestSignStampsAndSignsTheCanonicalBytes(t *testing.T) {
	key, err := signing.NewKeyFromSeed("idx-1", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	doc := Build(snapshot(), "https://fapps.example")
	now := time.Date(2026, 10, 3, 15, 4, 5, 999, time.FixedZone("x", 3*3600))
	rel, err := Sign(&doc, 7, now, 60*24*time.Hour, key)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Serial != 7 || doc.GeneratedAt != "2026-10-03T12:04:05Z" || doc.ExpiresAt != "2026-12-02T12:04:05Z" {
		t.Fatalf("stamp: serial %d generated %s expires %s", doc.Serial, doc.GeneratedAt, doc.ExpiresAt)
	}
	canon, _ := Canonical(doc)
	if !bytes.Equal(rel.Body, canon) || rel.Body[len(rel.Body)-1] != '\n' {
		t.Fatal("stored body is not the canonical bytes")
	}
	if rel.SHA256 != signing.SHA256Hex(rel.Body) || rel.KeyID != "idx-1" || rel.Serial != 7 {
		t.Fatalf("release = %+v", rel)
	}
	pub, _ := signing.ParsePublicKey(key.PublicHex())
	if err := signing.Verify([]ed25519.PublicKey{pub}, rel.SHA256, rel.Signature); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	if !rel.ExpiresAt.Equal(ts("2026-12-02T12:04:05Z")) {
		t.Fatalf("expires = %v", rel.ExpiresAt)
	}
}

func TestSameContentIgnoresSerialAndTimesOnly(t *testing.T) {
	key, _ := signing.NewKeyFromSeed("k", bytes.Repeat([]byte{1}, 32))
	doc := Build(snapshot(), "https://fapps.example")
	rel, _ := Sign(&doc, 3, time.Now(), time.Hour, key)

	again := Build(snapshot(), "https://fapps.example")
	if !sameContent(rel.Body, again) {
		t.Fatal("the same catalog was seen as different")
	}
	s := snapshot()
	now := time.Now()
	s.Apps[1].Versions[0].YankedAt, s.Apps[1].Versions[0].YankedReason = &now, "yanked now"
	if sameContent(rel.Body, Build(s, "https://fapps.example")) {
		t.Fatal("a yank was seen as no change")
	}
	if sameContent([]byte("not json"), again) {
		t.Fatal("an unreadable body was seen as the same")
	}
}

func TestMediaExt(t *testing.T) {
	for typ, ext := range map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/svg+xml": ""} {
		if got := MediaExt(typ); got != ext {
			t.Errorf("MediaExt(%s) = %q, want %q", typ, got, ext)
		}
	}
}
