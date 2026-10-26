// Package index builds, signs and serves the catalog's signed index:
// GET /v1/index.json and GET /v1/index.json.sig.
//
// ⚠⚠ The document below is what filex clients read (docs/ARCHITECTURE.md
// "The signed index"). Its shape is versioned by `schema`; a field may be
// ADDED within schema 1 (clients ignore what they do not know), nothing may
// be renamed, retyped or removed without schema 2.
//
// Bytes: the index is written canonically - object keys sorted, two-space
// indent, no HTML escaping, a trailing newline - so the same catalog state
// gives the same bytes, and the stored bytes (index_releases.body) are the
// ones signed and served. The signature is the index key's ed25519 over the
// lower-hex sha256 of those bytes (internal/signing), served as lower-case
// hex plus a newline.
package index

// SchemaVersion is the `schema` this server writes.
const SchemaVersion = 1

// Document is /v1/index.json.
type Document struct {
	Schema      int         `json:"schema"`
	Serial      int64       `json:"serial"`
	GeneratedAt string      `json:"generated_at"` // RFC 3339, UTC, seconds
	ExpiresAt   string      `json:"expires_at"`
	Keys        []Key       `json:"keys"`
	Publishers  []Publisher `json:"publishers"`
	Apps        []App       `json:"apps"`
}

// Key is a public key the store signs with, or will sign with.
type Key struct {
	ID      string `json:"id"`
	Use     string `json:"use"`     // "index" | "artifact"
	Ed25519 string `json:"ed25519"` // public key, lower-case hex
	Status  string `json:"status"`  // "active" | "next" | "retired"
}

// Publisher is who stands behind apps.
type Publisher struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	GitHub   string `json:"github"`
	Verified bool   `json:"verified"`
	Official bool   `json:"official"`
}

// App is one catalog entry and every listed version of it.
type App struct {
	Name        string            `json:"name"`
	Kind        string            `json:"kind"` // "app" | "language_pack" | "storage"
	Publisher   string            `json:"publisher"`
	Repo        string            `json:"repo"` // "owner/name"
	Homepage    string            `json:"homepage,omitempty"`
	License     string            `json:"license,omitempty"`
	Categories  []string          `json:"categories"`
	Label       map[string]string `json:"label"`
	Summary     map[string]string `json:"summary,omitempty"`
	Icon        *Media            `json:"icon,omitempty"`
	Screenshots []Media           `json:"screenshots"`
	Latest      string            `json:"latest"`
	Revoked     *Withdrawal       `json:"revoked"`
	Versions    []Version         `json:"versions"` // newest first
}

// Media is a mirrored image, addressed by its sha256.
type Media struct {
	URL     string            `json:"url"`
	SHA256  string            `json:"sha256"`
	Caption map[string]string `json:"caption,omitempty"`
}

// Version is one approved version.
type Version struct {
	Version     string      `json:"version"`
	Ref         string      `json:"ref"`
	Commit      string      `json:"commit"`
	Filex       string      `json:"filex"`
	PublishedAt string      `json:"published_at"` // the approval's date, YYYY-MM-DD
	Manifest    Artifact    `json:"manifest"`
	Wasm        *Artifact   `json:"wasm"`
	UI          *Artifact   `json:"ui"`
	Permissions []string    `json:"permissions"`
	Languages   []Language  `json:"languages"`
	Engines     []string    `json:"engines"`
	NotesURL    string      `json:"notes_url,omitempty"`
	Security    bool        `json:"security"`
	Yanked      *Withdrawal `json:"yanked"`
	// Binaries are a storage plugin's builds by platform ("linux/amd64"),
	// each pinned by sha256 and signed by the artifact key; Manifest is then
	// the release's filex-storage.json. Absent for an app or a language pack
	// (added within schema 1: a client that does not know it ignores it, and
	// filex before 0.55 skips a storage entry altogether).
	Binaries map[string]Artifact `json:"binaries,omitempty"`
	// Conformance is what the store's plugin validator measured of the
	// build for its platform (a storage plugin only).
	Conformance *Conformance `json:"conformance,omitempty"`
}

// Conformance is a storage plugin build's conformance run, as the index
// carries it (domain.Conformance).
type Conformance struct {
	Platform     string   `json:"platform"`
	Filex        string   `json:"filex"`
	Verified     bool     `json:"verified"`
	Passed       int      `json:"passed"`
	Failed       int      `json:"failed"`
	Skipped      int      `json:"skipped"`
	Driver       string   `json:"driver,omitempty"`
	Capabilities []string `json:"capabilities"`
}

// Artifact is a pinned download and the artifact key's signature over its
// sha256 (the shape filex's `signature` field takes).
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
	Sig    string `json:"sig"`
}

// Language is one language a version speaks and how much of filex's text it
// covers (language packs), from the dry run.
type Language struct {
	Code    string `json:"code"`
	Percent int    `json:"percent"`
}

// Withdrawal marks a yanked version or a revoked app.
type Withdrawal struct {
	Reason string `json:"reason"`
	At     string `json:"at"`
}
