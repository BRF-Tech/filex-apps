// Package domain holds the types the lanes share: the backend lane stores
// and serves them, the validator lane produces them, the frontend reads them
// through api/openapi.yaml (whose schemas carry the same names and fields).
//
// ⚠ This file is a CONTRACT between the store's parts. A
// field added here is added to api/openapi.yaml and backend/migrations in
// the same change; a field renamed or removed is a coordination point, not a
// drive-by edit.
package domain

import (
	"encoding/json"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
)

// AppKind is what an entry installs as in filex: an app (module and/or
// interface), a language pack, or a storage plugin. The first two are the
// same words as filex's dry run `kind`; a storage plugin is filex's other
// plugin class (docs/ARCHITECTURE.md "Storage plugins"): a native program
// listed from a release's filex-storage.json feed.
type AppKind string

const (
	KindApp          AppKind = "app"
	KindLanguagePack AppKind = "language_pack"
	KindStorage      AppKind = "storage"
)

// SubmissionKind is what a publisher submits a repository at a tag as: an
// app or a language pack (filex-app.json at the commit; the manifest says
// which) or a storage plugin (the release's filex-storage.json).
type SubmissionKind string

const (
	SubmitApp     SubmissionKind = "app"
	SubmitStorage SubmissionKind = "storage"
)

// ValidSubmissionKind reports whether k is a kind a submission may name.
func ValidSubmissionKind(k string) bool {
	return k == string(SubmitApp) || k == string(SubmitStorage)
}

// StoragePlatforms are the platforms a storage plugin's feed may name a
// build for, in the order the store lists them; RequiredPlatforms must all
// be there (filex's own image and the arm64 hosts it runs on). A build for
// any other key is refused: filex chooses a build by GOOS/GOARCH, and a key
// nothing can choose is a listing nobody installs.
var (
	StoragePlatforms  = []string{"linux/amd64", "linux/arm64", "windows/amd64", "windows/arm64", "darwin/amd64", "darwin/arm64"}
	RequiredPlatforms = []string{"linux/amd64", "linux/arm64"}
)

// Conformance is what the store's plugin validator measured of a storage
// plugin build: filex's own conformance run (the probes filex runs at every
// start of a plugin) against the plugin's selftest area, in an isolated
// container. Kept with the approved version and carried by the signed index
// and the install intent, so a filex shows what the store checked.
type Conformance struct {
	Platform string `json:"platform"`
	Filex    string `json:"filex"`
	Verified bool   `json:"verified"`
	Passed   int    `json:"passed"`
	Failed   int    `json:"failed"`
	Skipped  int    `json:"skipped"`
	// Driver is the storage driver the plugin describes itself as
	// (plugin:<driver> in filex).
	Driver string `json:"driver,omitempty"`
	// Capabilities are the optional capabilities it declared and proved
	// (range, write, delete, move, copy, mkdir, set_mtime, watch, presign,
	// multipart), sorted.
	Capabilities []string `json:"capabilities"`
}

// AppStatus is an app's standing in the catalog.
type AppStatus string

const (
	// AppListed: in the index and in the catalog.
	AppListed AppStatus = "listed"
	// AppUnlisted: in the index (installed copies keep getting updates and
	// warnings) but not shown in the catalog's lists.
	AppUnlisted AppStatus = "unlisted"
	// AppRevoked: harmful. Stays in the index with `revoked` so installed
	// copies are told to remove it; no new installs.
	AppRevoked AppStatus = "revoked"
)

// SubmissionStatus is where a submission is in the pipeline
// (docs/ARCHITECTURE.md "Submission pipeline").
type SubmissionStatus string

const (
	SubQueued      SubmissionStatus = "queued"
	SubResolving   SubmissionStatus = "resolving"
	SubValidating  SubmissionStatus = "validating"
	SubNeedsReview SubmissionStatus = "needs_review"
	SubApproved    SubmissionStatus = "approved"
	SubRejected    SubmissionStatus = "rejected"
	SubFailed      SubmissionStatus = "failed"
	SubWithdrawn   SubmissionStatus = "withdrawn"
)

// CheckKind names one automated step of the pipeline.
type CheckKind string

const (
	CheckRepoAccess CheckKind = "repo_access" // the submitter may act for the repository
	CheckTag        CheckKind = "tag"         // the ref is a tag; resolved to a commit
	CheckManifest   CheckKind = "manifest"    // filex-app.json at the commit, its sha256
	CheckConsent    CheckKind = "consent"     // .filex-apps.json names the publisher
	CheckPolicy     CheckKind = "policy"      // name, version order, reserved names, license
	CheckArtifacts  CheckKind = "artifacts"   // module / interface bundle: download, size, sha256
	CheckMedia      CheckKind = "media"       // icon and screenshots mirrored by sha256
	CheckDryRun     CheckKind = "dry_run"     // a real filex's install review (one row per validator)
)

// CheckStatus is one check's outcome.
type CheckStatus string

const (
	CheckPending CheckStatus = "pending"
	CheckRunning CheckStatus = "running"
	CheckPassed  CheckStatus = "passed"
	CheckWarned  CheckStatus = "warned" // passed, but the reviewer must look (e.g. a name close to another)
	CheckFailed  CheckStatus = "failed"
	CheckSkipped CheckStatus = "skipped"
)

// Role is what an account may do. Publisher rights come from the GitHub
// identity and the publisher memberships; reviewer and admin come from the
// account's permission on the administration repository (FAPPS_ADMIN_REPO),
// read from GitHub at each sign-in (the store does not grant them itself).
type Role string

const (
	RoleReviewer Role = "reviewer"
	RoleAdmin    Role = "admin"
)

// Text is a localized string, {language: text} - the shape filex uses.
type Text map[string]string

// Artifact is one downloadable piece a version names, pinned by sha256.
type Artifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Sig is the artifact key's signature over SHA256 (filex scheme);
	// empty until the version is approved.
	Sig string `json:"sig,omitempty"`
}

// Consent is the publisher's `.filex-apps.json` at the submitted commit:
// the proof that the repository's owner wants it listed under this
// publisher, and the listing's own metadata (pinned to the commit, reviewed
// with the code). Schema: schema/filex-apps.schema.json.
type Consent struct {
	Publisher   string        `json:"publisher"`
	Categories  []string      `json:"categories,omitempty"`
	Summary     Text          `json:"summary,omitempty"`
	Description string        `json:"description,omitempty"` // a Markdown file's path in the repository
	Icon        string        `json:"icon,omitempty"`        // a PNG/WebP path in the repository
	Screenshots []ConsentShot `json:"screenshots,omitempty"`
	// Conversions: which kinds of file the app converts into which, and what
	// each needs (filetypes.Conversion). Held to the schema and to the
	// manifest's engines by internal/consent; the catalog's file-type search
	// reads it (migration 0008, app_version_conversions).
	Conversions []filetypes.Conversion `json:"conversions,omitempty"`
}

// ConsentShot is one screenshot named by the consent file.
type ConsentShot struct {
	Path    string `json:"path"`
	Caption Text   `json:"caption,omitempty"`
}

// ResolveRequest is what a submission asks the resolver for.
type ResolveRequest struct {
	SubmissionID int64
	Repo         string // "owner/name"
	Ref          string // a tag
	Publisher    string // the publisher id the submitter acts as
	// Kind is what was submitted: SubmitApp (the default, "") or
	// SubmitStorage (the release's filex-storage.json is the manifest).
	Kind SubmissionKind
	// UserToken is the submitter's GitHub OAuth token, used for the
	// repository-access check and to spend the submitter's own rate limit.
	// Never stored, never logged.
	UserToken string
	// Operator is set for an operator import (docs/ARCHITECTURE.md
	// "Operator import (official publisher)"): the listing comes from the
	// operator instead of the repository's .filex-apps.json, and the consent
	// check passes only when the publisher is official and verified and the
	// repository's owner (numeric id) is the publisher's bound GitHub owner.
	// Nil for every submission made through the web interface.
	Operator *OperatorImport
}

// OperatorImport is an operator's listing for an official publisher's
// repository: the same metadata a .filex-apps.json carries (Listing, held to
// the same schema), the Markdown text its description names and the bytes
// of the images it names (keyed by the listing's paths). The pipeline keeps
// it with the submission; the publisher facts are read from the database at
// every resolve and never stored.
type OperatorImport struct {
	Listing     Consent           `json:"listing"`
	Description string            `json:"description_md,omitempty"`
	Files       map[string][]byte `json:"files,omitempty"`
	// Source names the listing file and its sha256, for the record.
	Source string `json:"source"`

	PublisherOfficial bool  `json:"-"`
	PublisherVerified bool  `json:"-"`
	PublisherOwnerID  int64 `json:"-"`
}

// Resolution is everything the resolver established about one repository at
// one tag. It is stored with the submission and copied into the version on
// approval; nothing in it is re-read from GitHub after that.
type Resolution struct {
	RepoOwner   string `json:"repo_owner"`
	RepoName    string `json:"repo_name"`
	RepoID      int64  `json:"repo_id"`    // GitHub's numeric id: survives renames and transfers
	OwnerID     int64  `json:"owner_id"`   // the owner's numeric id
	OwnerType   string `json:"owner_type"` // "User" | "Organization"
	Ref         string `json:"ref"`
	CommitSHA   string `json:"commit_sha"`
	TagVerified bool   `json:"tag_verified"` // GitHub's signature verification of the tag (informational)

	ManifestURL    string          `json:"manifest_url"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	Manifest       json.RawMessage `json:"manifest"`

	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Kind        AppKind  `json:"kind"`
	FilexRange  string   `json:"filex_range"`
	Permissions []string `json:"permissions"`
	Languages   []string `json:"languages"`
	Homepage    string   `json:"homepage,omitempty"`
	License     string   `json:"license,omitempty"` // SPDX id from GitHub's license detection

	Wasm *Artifact `json:"wasm,omitempty"`
	UI   *Artifact `json:"ui,omitempty"`

	// Binaries are a storage plugin's builds, by platform ("linux/amd64"),
	// each downloaded and held to the sha256 its feed names. Nil for an app.
	Binaries map[string]Artifact `json:"binaries,omitempty"`
	// FeedNotes are the feed's own release notes (plain text, at most
	// 8 KiB), shown to the reviewer. Not signed into anything.
	FeedNotes string `json:"feed_notes,omitempty"`

	Consent Consent `json:"consent"`
	// Description is the text of the Markdown file the consent file names
	// (Consent.Description), read at the commit, at most 64 KiB; empty when
	// the consent file names none. The backend renders and sanitizes it.
	Description string `json:"description_md,omitempty"`
	// Media are the consent file's images, fetched and stored by sha256.
	Media []MediaRef `json:"media,omitempty"`
}

// MediaRef is a mirrored image.
type MediaRef struct {
	Role    string `json:"role"` // "icon" | "screenshot"
	SHA256  string `json:"sha256"`
	Type    string `json:"content_type"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Caption Text   `json:"caption,omitempty"`
}

// CheckResult is one step's outcome, shown to the publisher and the reviewer.
type CheckResult struct {
	Kind      CheckKind       `json:"kind"`
	Validator string          `json:"validator,omitempty"` // dry_run rows: which filex
	Status    CheckStatus     `json:"status"`
	Code      string          `json:"code,omitempty"`    // machine-readable reason, e.g. "sha256_mismatch"
	Message   string          `json:"message,omitempty"` // a sentence for a person
	Data      json.RawMessage `json:"data,omitempty"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   *time.Time      `json:"ended_at,omitempty"`
}

// DryRunRequest asks every configured filex to review one version.
type DryRunRequest struct {
	Repo       string // "owner/name"
	Ref        string
	Kind       AppKind
	FilexRange string // to pick the floor validator
	// Expected pins the comparison: the resolver's own hashes and the
	// manifest's permission ids.
	ExpectManifestSHA256 string
	ExpectWasmSHA256     string
	// ExpectUISHA256 pins the interface bundle (an interface-only app has no
	// module: filex answers wasm_sha256 empty and ui_sha256 set).
	ExpectUISHA256    string
	ExpectPermissions []string

	// A storage plugin's run (Kind == KindStorage): the feed's name and
	// version, which the build must describe itself as, and every build
	// pinned by sha256 - a plugin validator runs the one for its own
	// platform, downloaded again and held to that hash.
	Name     string
	Version  string
	Binaries map[string]Artifact
}

// DryRunResult is one filex's answer, reduced to what the store compares
// and keeps. Raw is the whole answer, kept for the reviewer.
type DryRunResult struct {
	Validator      string          `json:"validator"`
	FilexVersion   string          `json:"filex_version"`
	OK             bool            `json:"ok"`
	Code           string          `json:"code,omitempty"`
	Message        string          `json:"message,omitempty"`
	Kind           AppKind         `json:"kind"`
	ManifestSHA256 string          `json:"manifest_sha256"`
	WasmSHA256     string          `json:"wasm_sha256,omitempty"`
	UISHA256       string          `json:"ui_sha256,omitempty"`
	CompatOK       *bool           `json:"compat_ok,omitempty"`
	Permissions    []PermissionRow `json:"permissions"`
	EnginesMissing []string        `json:"engines_missing,omitempty"`
	// Languages is this filex's coverage of each language the version
	// speaks (filex's dry run `languages`; it differs between filex versions
	// because their catalogues differ).
	Languages []LanguageCoverage `json:"languages,omitempty"`
	// Storage is a plugin validator's run of a storage plugin build (nil for
	// an app's dry run).
	Storage *StorageRun     `json:"storage,omitempty"`
	Raw     json.RawMessage `json:"raw"`
}

// StorageRun is what a plugin validator reported about one storage plugin
// build: which build it ran (its platform and sha256), what the plugin
// described itself as, and filex's conformance report, probe by probe.
type StorageRun struct {
	Platform     string          `json:"platform"`
	BinarySHA256 string          `json:"binary_sha256"`
	Driver       string          `json:"driver,omitempty"`
	Version      string          `json:"version,omitempty"`
	Label        string          `json:"label,omitempty"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
	// Report is filex's conformance report as filex wrote it
	// ({verified, scratch, results: [{name, status, detail, took_ms}]}).
	Report json.RawMessage `json:"report,omitempty"`
	// Summary is the report counted, the part the version keeps.
	Summary *Conformance `json:"summary,omitempty"`
}

// LanguageCoverage is how much of one filex's text a language covers.
type LanguageCoverage struct {
	Code    string `json:"code"`
	Percent int    `json:"percent"`
	Keys    int    `json:"keys"`
	Total   int    `json:"total"`
}

// PermissionRow is one permission as filex's review words it.
type PermissionRow struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Reason Text   `json:"reason,omitempty"`
}

// ---------------------------------------------------------------- licensing

// PricingModel is how an app is priced. The store only shows it: it takes
// no payment (a paid app names where to buy it).
type PricingModel string

const (
	PricingFree PricingModel = "free"
	PricingPaid PricingModel = "paid"
)

// Pricing is an app's price as the catalog shows it (api/openapi.yaml
// Pricing). PriceText and PurchaseURL are set only on a paid app.
type Pricing struct {
	Model       PricingModel `json:"model"`
	PriceText   string       `json:"price_text,omitempty"`
	PurchaseURL string       `json:"purchase_url,omitempty"`
}

// LicenseResult is the verdict of one license check.
type LicenseResult string

const (
	LicenseValid          LicenseResult = "valid"
	LicenseInvalid        LicenseResult = "invalid" // unknown or malformed key: says nothing about any license
	LicenseRevoked        LicenseResult = "revoked"
	LicenseExpired        LicenseResult = "expired"
	LicenseSeatsExhausted LicenseResult = "seats_exhausted"
	LicenseWrongApp       LicenseResult = "wrong_app"
)

// LicenseVerdict is the payload of POST /v1/licenses/verify, signed with
// the store's license key (api/openapi.yaml LicenseVerdict). Optional
// fields are absent, never null: the payload is signed as canonical JSON
// and a filex re-canonicalizes what it receives. Dates are YYYY-MM-DD
// (through the end of that UTC day); times RFC 3339, UTC, seconds.
type LicenseVerdict struct {
	Result       LicenseResult `json:"result"`
	App          string        `json:"app"`
	Licensee     string        `json:"licensee,omitempty"`
	Seats        int           `json:"seats,omitempty"`
	SeatsUsed    *int          `json:"seats_used,omitempty"`
	ValidUntil   string        `json:"valid_until,omitempty"`
	UpdatesUntil string        `json:"updates_until,omitempty"`
	InstanceID   string        `json:"instance_id"`
	CheckedAt    string        `json:"checked_at"`
	NextCheckBy  string        `json:"next_check_by"`
	GraceUntil   string        `json:"grace_until"`
}

// License check timing (LicenseVerdict.NextCheckBy and GraceUntil are
// CheckedAt plus these).
const (
	LicenseCheckEvery = 24 * time.Hour
	LicenseGrace      = 14 * 24 * time.Hour
)

// ---------------------------------------------------------------- install intents

// InstallIntentPayload is the payload of GET /v1/install/{token}, signed
// with the index key (api/openapi.yaml InstallIntentPayload): the approved
// version a person asked to install on their filex. Optional fields are
// absent, never null.
type InstallIntentPayload struct {
	Store string `json:"store"` // the store's origin
	// FilexOrigin is the filex the intent was made for (the magic link's
	// origin, normalized): a filex refuses an intent that names another.
	FilexOrigin    string   `json:"filex_origin"`
	TokenID        string   `json:"token_id"` // the intent's short id, not a hash of the token
	App            string   `json:"app"`
	Kind           AppKind  `json:"kind"`
	Version        string   `json:"version"`
	Repo           string   `json:"repo"` // "owner/name"
	Ref            string   `json:"ref"`
	Commit         string   `json:"commit"`          // the tag's commit at approval; always set
	ManifestSHA256 string   `json:"manifest_sha256"` // always set
	WasmSHA256     string   `json:"wasm_sha256,omitempty"`
	UISHA256       string   `json:"ui_sha256,omitempty"`
	Permissions    []string `json:"permissions"`
	FilexRange     string   `json:"filex_range"`
	Paid           bool     `json:"paid"`
	LicenseKey     string   `json:"license_key,omitempty"`
	ExpiresAt      string   `json:"expires_at"`
	// A storage plugin's intent (Kind == KindStorage): the release's feed it
	// was listed from (its sha256 is ManifestSHA256), every build with the
	// artifact key's signature over its sha256, and what the store's plugin
	// validator measured. Absent for an app.
	FeedURL     string                  `json:"feed_url,omitempty"`
	Binaries    map[string]IntentBinary `json:"binaries,omitempty"`
	Conformance *Conformance            `json:"conformance,omitempty"`
}

// IntentBinary is one build of a storage plugin as an intent names it.
type IntentBinary struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
	Sig    string `json:"sig"`
}

// InstanceConnection is the payload of POST /v1/instances/connect, signed
// with the index key (api/openapi.yaml InstanceConnection): the saved
// instance a filex's key is now bound to. The filex checks that FilexOrigin
// is its own address and KeyFingerprint its own key's before it keeps the
// connection.
type InstanceConnection struct {
	Store          string `json:"store"`
	InstanceID     string `json:"instance_id"`
	FilexOrigin    string `json:"filex_origin"`
	KeyFingerprint string `json:"key_fingerprint"` // lower-hex sha256 of the 32 raw key bytes
	ConnectedAt    string `json:"connected_at"`
}

// InstallIntentTTL is how long an install link stays usable.
const InstallIntentTTL = 30 * time.Minute

// InstallResult is what a filex reports about an install link.
type InstallResult string

const (
	InstallInstalled InstallResult = "installed"
	InstallCancelled InstallResult = "cancelled"
)

// SignedEnvelope is every signed answer that is not the index: Signature is
// the key KeyID's ed25519 over the lower-hex sha256 of Payload, which holds
// the payload's canonical JSON bytes (keys sorted, no insignificant space,
// no HTML escaping, no trailing newline). api/openapi.yaml, the conventions.
type SignedEnvelope struct {
	Payload   json.RawMessage `json:"payload"`
	KeyID     string          `json:"key_id"`
	Signature string          `json:"signature"`
}
