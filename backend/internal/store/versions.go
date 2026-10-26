package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// Artifact is a pinned download of a version and the artifact key's
// signature over its sha256.
type Artifact struct {
	URL    string
	SHA256 string
	Size   int64
	Sig    string
}

// Version is an approved version. Immutable except the yank fields (the
// database refuses any other change, migration 0002).
type Version struct {
	ID             int64
	AppID          int64
	SubmissionID   int64
	Version        string
	Ref            string
	Commit         string
	FilexRange     string
	ManifestURL    string
	ManifestSHA256 string
	ManifestSig    string
	Manifest       json.RawMessage
	Wasm           *Artifact
	UI             *Artifact
	// Binaries are a storage plugin's builds by platform ("linux/amd64"),
	// each with the artifact key's signature over its sha256; nil for an
	// app (migration 0010).
	Binaries map[string]Artifact
	// Conformance is what the store's plugin validator measured of a storage
	// plugin build; nil for an app.
	Conformance    *domain.Conformance
	Permissions    []string
	PermissionRows json.RawMessage // [{id, label, reason}] in filex's words
	Languages      json.RawMessage // [{code, percent}]
	Engines        []string
	FileTypes      json.RawMessage
	NotesURL       string
	Security       bool
	ArtifactKeyID  string
	PublishedAt    time.Time
	YankedReason   string
	YankedAt       *time.Time
	YankedBy       *int64
}

const versionCols = `v.id, v.app_id, v.submission_id, v.version, v.ref, v.commit_sha, v.filex_range,
	v.manifest_url, v.manifest_sha256, v.manifest_sig, v.manifest,
	COALESCE(v.wasm_url, ''), COALESCE(v.wasm_sha256, ''), COALESCE(v.wasm_size, 0), COALESCE(v.wasm_sig, ''),
	COALESCE(v.ui_url, ''), COALESCE(v.ui_sha256, ''), COALESCE(v.ui_size, 0), COALESCE(v.ui_sig, ''),
	v.permissions, v.permission_rows, v.languages, v.engines, v.file_types, COALESCE(v.notes_url, ''),
	v.security, v.artifact_key_id, v.published_at, COALESCE(v.yanked_reason, ''), v.yanked_at, v.yanked_by,
	v.binaries, v.conformance`

func scanVersion(row scanner) (Version, error) {
	var v Version
	var w, u Artifact
	var bins, conf []byte
	err := row.Scan(&v.ID, &v.AppID, &v.SubmissionID, &v.Version, &v.Ref, &v.Commit, &v.FilexRange,
		&v.ManifestURL, &v.ManifestSHA256, &v.ManifestSig, &v.Manifest,
		&w.URL, &w.SHA256, &w.Size, &w.Sig,
		&u.URL, &u.SHA256, &u.Size, &u.Sig,
		&v.Permissions, &v.PermissionRows, &v.Languages, &v.Engines, &v.FileTypes, &v.NotesURL,
		&v.Security, &v.ArtifactKeyID, &v.PublishedAt, &v.YankedReason, &v.YankedAt, &v.YankedBy,
		&bins, &conf)
	if w.URL != "" {
		v.Wasm = &w
	}
	if u.URL != "" {
		v.UI = &u
	}
	v.Permissions = emptyIfNil(v.Permissions)
	v.Engines = emptyIfNil(v.Engines)
	if err == nil && len(bins) > 0 {
		var stored map[string]storedBinary
		if jerr := json.Unmarshal(bins, &stored); jerr == nil {
			v.Binaries = map[string]Artifact{}
			for plat, b := range stored {
				v.Binaries[plat] = Artifact{URL: b.URL, SHA256: b.SHA256, Size: b.Size, Sig: b.Sig}
			}
		}
	}
	if err == nil && len(conf) > 0 {
		var c domain.Conformance
		if jerr := json.Unmarshal(conf, &c); jerr == nil {
			if c.Capabilities == nil {
				c.Capabilities = []string{}
			}
			v.Conformance = &c
		}
	}
	return v, err
}

// storedBinary is one build in app_versions.binaries.
type storedBinary struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Sig    string `json:"sig"`
}

// binariesJSON is what app_versions.binaries holds for v (nil: NULL).
func binariesJSON(v Version) (any, error) {
	if len(v.Binaries) == 0 {
		return nil, nil
	}
	out := map[string]storedBinary{}
	for plat, b := range v.Binaries {
		out[plat] = storedBinary{URL: b.URL, SHA256: b.SHA256, Size: b.Size, Sig: b.Sig}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// conformanceJSON is what app_versions.conformance holds for v (nil: NULL).
func conformanceJSON(v Version) (any, error) {
	if v.Conformance == nil {
		return nil, nil
	}
	b, err := json.Marshal(v.Conformance)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// InsertVersion writes an approved version. ErrConflict when the app
// already has this version.
func InsertVersion(ctx context.Context, q Q, v Version) (int64, error) {
	var wURL, wSHA, wSig, uURL, uSHA, uSig *string
	var wSize, uSize *int64
	if v.Wasm != nil {
		wURL, wSHA, wSig, wSize = &v.Wasm.URL, &v.Wasm.SHA256, &v.Wasm.Sig, &v.Wasm.Size
	}
	if v.UI != nil {
		uURL, uSHA, uSig, uSize = &v.UI.URL, &v.UI.SHA256, &v.UI.Sig, &v.UI.Size
	}
	bins, err := binariesJSON(v)
	if err != nil {
		return 0, err
	}
	conf, err := conformanceJSON(v)
	if err != nil {
		return 0, err
	}
	var id int64
	err = q.QueryRow(ctx, `
		INSERT INTO app_versions (app_id, submission_id, version, ref, commit_sha, filex_range,
			manifest_url, manifest_sha256, manifest_sig, manifest,
			wasm_url, wasm_sha256, wasm_size, wasm_sig, ui_url, ui_sha256, ui_size, ui_sig,
			permissions, permission_rows, languages, engines, file_types, notes_url, security, artifact_key_id,
			binaries, conformance)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
			$19, $20, $21, $22, $23, NULLIF($24, ''), $25, $26, $27, $28)
		RETURNING id`,
		v.AppID, v.SubmissionID, v.Version, v.Ref, v.Commit, v.FilexRange,
		v.ManifestURL, v.ManifestSHA256, v.ManifestSig, jsonOr(v.Manifest, "{}"),
		wURL, wSHA, wSize, wSig, uURL, uSHA, uSize, uSig,
		emptyIfNil(v.Permissions), jsonOr(v.PermissionRows, "[]"), jsonOr(v.Languages, "[]"), emptyIfNil(v.Engines),
		jsonOrNil(v.FileTypes), v.NotesURL, v.Security, v.ArtifactKeyID, bins, conf).Scan(&id)
	if err != nil && isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// VersionsOf lists an app's versions (any order; callers sort by semver).
func VersionsOf(ctx context.Context, q Q, appID int64) ([]Version, error) {
	rows, err := q.Query(ctx, `SELECT `+versionCols+` FROM app_versions v WHERE v.app_id = $1 ORDER BY v.published_at DESC, v.id DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetVersion finds one version of an app.
func GetVersion(ctx context.Context, q Q, appID int64, version string) (Version, error) {
	v, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionCols+` FROM app_versions v WHERE v.app_id = $1 AND v.version = $2`, appID, version))
	return v, notFound(err)
}

// YankVersion marks a version yanked (no-op if it already is).
func YankVersion(ctx context.Context, q Q, id int64, reason string, by int64) error {
	_, err := q.Exec(ctx, `
		UPDATE app_versions SET yanked_reason = $2, yanked_at = now(), yanked_by = $3
		WHERE id = $1 AND yanked_at IS NULL`, id, reason, by)
	return err
}

// UnyankVersion lifts a yank.
func UnyankVersion(ctx context.Context, q Q, id int64) error {
	_, err := q.Exec(ctx, `
		UPDATE app_versions SET yanked_reason = NULL, yanked_at = NULL, yanked_by = NULL
		WHERE id = $1 AND yanked_at IS NOT NULL`, id)
	return err
}

// VersionAtRepoRef reports an approved version of the repository at the
// tag, if any (the repository named "owner/name", case-insensitively).
func VersionAtRepoRef(ctx context.Context, q Q, repo, ref string) (string, string, error) {
	var app, version string
	err := q.QueryRow(ctx, `
		SELECT a.name, v.version FROM app_versions v JOIN apps a ON a.id = v.app_id
		WHERE lower(a.repo_owner || '/' || a.repo_name) = lower($1) AND v.ref = $2
		LIMIT 1`, repo, ref).Scan(&app, &version)
	if err = notFound(err); err == ErrNotFound {
		return "", "", nil
	}
	return app, version, err
}

func jsonOr(b json.RawMessage, def string) json.RawMessage {
	if len(b) == 0 || string(b) == "null" {
		return json.RawMessage(def)
	}
	return b
}

func jsonOrNil(b json.RawMessage) any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}
