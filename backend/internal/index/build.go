package index

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// Time formats the index uses.
const (
	TimeLayout = "2006-01-02T15:04:05Z" // RFC 3339, UTC, seconds
	DateLayout = "2006-01-02"
)

// MediaURL is a mirrored image's address on this store.
func MediaURL(publicURL, sha, contentType string) string {
	return publicURL + "/v1/media/" + sha + MediaExt(contentType)
}

// MediaExt is the file extension the store serves an image type under.
func MediaExt(contentType string) string {
	switch contentType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	}
	return ""
}

// Build turns a snapshot into the index document. Serial and the two times
// are left for the publisher. Every list is non-nil and in a fixed order, so
// the same state gives the same canonical bytes.
func Build(s store.IndexSnapshot, publicURL string) Document {
	doc := Document{
		Schema:     SchemaVersion,
		Keys:       []Key{},
		Publishers: []Publisher{},
		Apps:       []App{},
	}
	for _, k := range s.Keys {
		doc.Keys = append(doc.Keys, Key{ID: k.ID, Use: k.Use, Ed25519: k.PublicHex, Status: k.Status})
	}
	sort.Slice(doc.Keys, func(i, j int) bool {
		if doc.Keys[i].Use != doc.Keys[j].Use {
			return doc.Keys[i].Use < doc.Keys[j].Use
		}
		return doc.Keys[i].ID < doc.Keys[j].ID
	})
	for _, p := range s.Publishers {
		doc.Publishers = append(doc.Publishers, Publisher{
			ID: p.ID, Name: p.Name, GitHub: p.GitHubOwner, Verified: p.Verified, Official: p.Official,
		})
	}
	sort.Slice(doc.Publishers, func(i, j int) bool { return doc.Publishers[i].ID < doc.Publishers[j].ID })

	for _, a := range s.Apps {
		doc.Apps = append(doc.Apps, buildApp(a, publicURL))
	}
	sort.Slice(doc.Apps, func(i, j int) bool { return doc.Apps[i].Name < doc.Apps[j].Name })
	return doc
}

func buildApp(a store.IndexApp, publicURL string) App {
	out := App{
		Name:        a.Name,
		Kind:        a.Kind,
		Publisher:   a.PublisherID,
		Repo:        a.Repo(),
		Homepage:    a.Homepage,
		License:     a.License,
		Categories:  append([]string{}, a.Categories...),
		Label:       nonNilText(a.Label),
		Summary:     a.Summary,
		Screenshots: []Media{},
		Versions:    []Version{},
	}
	if len(out.Summary) == 0 {
		out.Summary = nil
	}
	if a.IconSHA256 != "" && a.IconType != "" {
		out.Icon = &Media{URL: MediaURL(publicURL, a.IconSHA256, a.IconType), SHA256: a.IconSHA256}
	}
	shots := append([]store.AppMedia{}, a.Screenshots...)
	sort.Slice(shots, func(i, j int) bool { return shots[i].Position < shots[j].Position })
	for _, m := range shots {
		media := Media{URL: MediaURL(publicURL, m.SHA256, m.ContentType), SHA256: m.SHA256}
		if len(m.Caption) > 0 {
			media.Caption = m.Caption
		}
		out.Screenshots = append(out.Screenshots, media)
	}
	if a.Status == "revoked" && a.RevokedAt != nil {
		out.Revoked = &Withdrawal{Reason: a.RevokedReason, At: a.RevokedAt.UTC().Format(TimeLayout)}
	}

	versions := append([]store.Version{}, a.Versions...)
	SortVersions(versions)
	for _, v := range versions {
		out.Versions = append(out.Versions, buildVersion(v))
	}
	out.Latest = Latest(versions)
	return out
}

// SortVersions orders versions newest first (semver), ties by approval.
func SortVersions(vs []store.Version) {
	sort.SliceStable(vs, func(i, j int) bool {
		if c := CompareVersions(vs[i].Version, vs[j].Version); c != 0 {
			return c > 0
		}
		return vs[i].ID > vs[j].ID
	})
}

// Latest is the highest version that is not yanked ("" when every version
// is).
func Latest(vs []store.Version) string {
	best := ""
	for _, v := range vs {
		if v.YankedAt != nil {
			continue
		}
		if best == "" || CompareVersions(v.Version, best) > 0 {
			best = v.Version
		}
	}
	return best
}

// versionPermissions is what the index says a version asks a filex for:
// filex's own review list - the ids of the rows the validator's dry run
// answered (store.Version.PermissionRows, the rows the app's page shows) -
// else, for a version kept without rows, the manifest's declared list.
//
// ⚠ Not the declared list alone (since 2026-10-16): filex adds every `ui`, `ui:*`,
// `ui-viewer:*`, `ui-new:*` and `thumbnail:*` permission from the manifest's
// `ui` and `thumbnails` blocks, files:e2e-plaintext from `encrypted_folders`
// and files:co-edit from `co_edit`, never from `permissions`. A filex shows
// the index's list in its store (the count, the request a person sends, the
// administrator's first look at it) and its install review shows the full
// one: measured on the office editor 0.3.1, the index said 2 permissions
// (files:read, files:write) where the install review and this store's page
// said 21 (unsafe-eval and the encrypted folders' plaintext among them). A
// storage plugin's entry keeps its empty list.
func versionPermissions(v store.Version) []string {
	if len(v.Binaries) == 0 && len(v.PermissionRows) > 0 {
		var rows []domain.PermissionRow
		if json.Unmarshal(v.PermissionRows, &rows) == nil {
			ids := make([]string, 0, len(rows))
			for _, r := range rows {
				ids = append(ids, r.ID)
			}
			if out := domain.NormalizePermissions(ids); len(out) > 0 {
				return out
			}
		}
	}
	return append([]string{}, v.Permissions...)
}

func buildVersion(v store.Version) Version {
	out := Version{
		Version:     v.Version,
		Ref:         v.Ref,
		Commit:      v.Commit,
		Filex:       v.FilexRange,
		PublishedAt: v.PublishedAt.UTC().Format(DateLayout),
		Manifest:    Artifact{URL: v.ManifestURL, SHA256: v.ManifestSHA256, Sig: v.ManifestSig},
		Permissions: versionPermissions(v),
		Languages:   []Language{},
		Engines:     append([]string{}, v.Engines...),
		NotesURL:    v.NotesURL,
		Security:    v.Security,
	}
	if v.Wasm != nil {
		out.Wasm = &Artifact{URL: v.Wasm.URL, SHA256: v.Wasm.SHA256, Size: v.Wasm.Size, Sig: v.Wasm.Sig}
	}
	if v.UI != nil {
		out.UI = &Artifact{URL: v.UI.URL, SHA256: v.UI.SHA256, Size: v.UI.Size, Sig: v.UI.Sig}
	}
	if len(v.Binaries) > 0 {
		out.Binaries = map[string]Artifact{}
		for plat, b := range v.Binaries {
			out.Binaries[plat] = Artifact{URL: b.URL, SHA256: b.SHA256, Size: b.Size, Sig: b.Sig}
		}
	}
	if c := v.Conformance; c != nil {
		out.Conformance = &Conformance{Platform: c.Platform, Filex: c.Filex, Verified: c.Verified, Passed: c.Passed,
			Failed: c.Failed, Skipped: c.Skipped, Driver: c.Driver, Capabilities: append([]string{}, c.Capabilities...)}
	}
	var langs []Language
	if len(v.Languages) > 0 && json.Unmarshal(v.Languages, &langs) == nil {
		for _, l := range langs {
			if l.Code != "" {
				out.Languages = append(out.Languages, Language{Code: l.Code, Percent: l.Percent})
			}
		}
	}
	sort.Slice(out.Languages, func(i, j int) bool { return out.Languages[i].Code < out.Languages[j].Code })
	if v.YankedAt != nil {
		out.Yanked = &Withdrawal{Reason: v.YankedReason, At: v.YankedAt.UTC().Format(TimeLayout)}
	}
	return out
}

func nonNilText(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// Stamp sets the serial and the two times of a document.
func Stamp(doc *Document, serial int64, now time.Time, ttl time.Duration) {
	now = now.UTC().Truncate(time.Second)
	doc.Serial = serial
	doc.GeneratedAt = now.Format(TimeLayout)
	doc.ExpiresAt = now.Add(ttl).Format(TimeLayout)
}
