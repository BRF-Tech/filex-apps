// Package resolve implements pipeline.Resolver: what a repository serves at
// a tag, measured by the store itself before any filex is asked
// (docs/ARCHITECTURE.md "Submission pipeline", checks 1-4, 6 and 7).
//
// The policy check (5: names, version order, license) is the backend's: it
// needs the catalog. So is the comparison of the owner id with the
// publisher's bound owner (the resolver records RepoID and OwnerID). The
// submitter's permission is checked synchronously by the backend at
// submission with the submitter's token (github.Client.Repository); a
// background Resolve gets UserToken "" and re-measures only that the
// repository is public and who owns it.
//
// Everything is read at the tag's COMMIT, not the tag name, except the
// artifact addresses, which are expanded exactly as filex expands them
// (`{tag}` = the tag name): the store must download what filex will download,
// and every byte is pinned by sha256 anyway.
package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brf-tech/filex-apps/backend/internal/consent"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
	"github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// Check codes the resolver writes (docs/ARCHITECTURE.md pipeline table).
const (
	CodeRepoPrivate     = "repo_private"
	CodeRepoAddress     = "repo_address"
	CodeRepoArchived    = "repo_archived"
	CodeRepoPermission  = "repo_permission"
	CodeNotATag         = "not_a_tag"
	CodeTagNotFound     = "tag_not_found"
	CodeManifestMissing = "manifest_missing"
	CodeManifestBad     = "manifest_unreadable"
	CodeTooLarge        = "too_large"
	CodeConsentMissing  = "consent_missing"
	CodeConsentInvalid  = "consent_invalid"
	CodeConsentPub      = "consent_publisher"
	CodeSHA256Mismatch  = "sha256_mismatch"
	CodeSHA256Required  = "sha256_required"
	CodeFetchFailed     = "fetch_failed"
	CodeHostNotAllowed  = "host_not_allowed"
	CodeMediaInvalid    = "media_invalid"
	// An operator import's consent by ownership (docs/ARCHITECTURE.md
	// "Operator import (official publisher)").
	CodePublisherNotOfficial = "publisher_not_official"
	CodeOwnerMismatch        = "owner_mismatch"
	// GitHub refused the caller's token, or could not be asked at all: two
	// answers that say nothing about the repository (BRF-Tech/filex#118).
	CodeGitHubTokenInvalid = "github_token_invalid"
	CodeGitHubUnavailable  = "github_unavailable"
)

// OperatorConsent is the consent check's sentence for an operator import
// (and the words the audit log records).
const OperatorConsent = pipeline.OperatorConsent

func sum256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// MaxDescriptionBytes caps the Markdown file the consent names.
const MaxDescriptionBytes = 64 << 10

const (
	roleIcon       = "icon"
	roleScreenshot = "screenshot"
)

// Limits are the size caps (config.FetchConfig).
type Limits struct {
	Manifest int64
	Wasm     int64
	UI       int64
	Media    int64
	// Binary caps one build of a storage plugin (FAPPS_MAX_BINARY_MB).
	Binary int64
}

// Options builds a Resolver.
type Options struct {
	GitHub *github.Client
	// Media stores the mirrored images (internal/media). Nil: images are
	// checked but not stored (tests, a dry check).
	Media  pipeline.MediaSink
	Limits Limits
	// Now is the clock for the checks' times (tests fix it).
	Now func() time.Time
}

// Resolver implements pipeline.Resolver.
type Resolver struct {
	gh  *github.Client
	ms  pipeline.MediaSink
	lim Limits
	now func() time.Time
}

var _ pipeline.Resolver = (*Resolver)(nil)

// New builds a Resolver.
func New(o Options) *Resolver {
	r := &Resolver{gh: o.GitHub, ms: o.Media, lim: o.Limits, now: o.Now}
	if r.now == nil {
		r.now = time.Now
	}
	if r.lim.Manifest <= 0 {
		r.lim.Manifest = 16 << 20
	}
	if r.lim.Wasm <= 0 {
		r.lim.Wasm = 64 << 20
	}
	if r.lim.UI <= 0 {
		r.lim.UI = 128 << 20
	}
	if r.lim.Media <= 0 {
		r.lim.Media = 5 << 20
	}
	if r.lim.Binary <= 0 {
		r.lim.Binary = 256 << 20
	}
	return r
}

// run is one Resolve call's state.
type run struct {
	r      *Resolver
	ctx    context.Context
	req    domain.ResolveRequest
	res    *domain.Resolution
	checks []domain.CheckResult
	owner  string
	name   string
	parsed *parsed
	// feed is a storage plugin's feed (storage.go), nil for an app.
	feed *parsedFeed
	repo *github.Repo
}

// Resolve runs the checks in order and stops at the first that fails. err is
// only for a failure worth retrying (GitHub down or rate-limited, a network
// error); a submission that fails a check returns its checks and nil.
func (r *Resolver) Resolve(ctx context.Context, req domain.ResolveRequest) (*domain.Resolution, []domain.CheckResult, error) {
	x := &run{r: r, ctx: ctx, req: req, res: &domain.Resolution{Ref: req.Ref}}
	steps := []func() (bool, error){x.repoAccess, x.tag, x.manifestAndConsent, x.artifacts, x.media}
	if req.Kind == domain.SubmitStorage {
		// A storage plugin: the release's feed stands where an app's manifest
		// does, its builds where an app's module does (storage.go).
		steps = []func() (bool, error){x.repoAccess, x.tag, x.feedAndConsent, x.binaries, x.media}
	}
	for _, step := range steps {
		ok, err := step()
		if err != nil {
			return nil, x.checks, err
		}
		if !ok {
			return x.res, x.checks, nil
		}
	}
	return x.res, x.checks, nil
}

// check records one step. fn returns (code, message, data, err): err is a
// retryable failure; a non-empty code is a failed check.
func (x *run) check(kind domain.CheckKind, fn func() (status domain.CheckStatus, code, msg string, data any, err error)) (bool, error) {
	start := x.r.now().UTC()
	status, code, msg, data, err := fn()
	if err != nil {
		return false, err
	}
	end := x.r.now().UTC()
	c := domain.CheckResult{Kind: kind, Status: status, Code: code, Message: msg, StartedAt: start, EndedAt: &end}
	if data != nil {
		if b, jerr := json.Marshal(data); jerr == nil {
			c.Data = b
		}
	}
	x.checks = append(x.checks, c)
	return status != domain.CheckFailed, nil
}

func failed(code, msg string, data any) (domain.CheckStatus, string, string, any, error) {
	return domain.CheckFailed, code, msg, data, nil
}

// retryable reports whether err is the pipeline's to retry (GitHub down or
// rate-limited, the network) rather than an answer to record as a check.
func retryable(err error) bool { return github.Retryable(err) }

// RepoAccess is check 1 on its own, for the backend's synchronous check at
// POST /api/v1/submissions with the submitter's token (userToken): the same
// codes and sentences the background resolve writes. The repository comes
// back whenever GitHub showed it (also when the check failed), so the caller
// compares repo.Owner.ID with the publisher's bound owner (owner_mismatch)
// and records repo.ID. err is only for a failure worth retrying.
func (r *Resolver) RepoAccess(ctx context.Context, repo, userToken string) (*github.Repo, domain.CheckResult, error) {
	x := &run{r: r, ctx: ctx, req: domain.ResolveRequest{Repo: repo, UserToken: userToken}, res: &domain.Resolution{}}
	if _, err := x.repoAccess(); err != nil {
		return nil, domain.CheckResult{}, err
	}
	return x.repo, x.checks[0], nil
}

// 1. repo_access
func (x *run) repoAccess() (bool, error) {
	return x.check(domain.CheckRepoAccess, func() (domain.CheckStatus, string, string, any, error) {
		owner, name, err := github.ParseRepo(x.req.Repo)
		if err != nil {
			return failed(CodeRepoAddress, x.req.Repo+" is not a GitHub repository address", nil)
		}
		x.owner, x.name = owner, name
		repo, err := x.r.gh.Repository(x.ctx, x.req.UserToken, owner, name)
		switch {
		case errors.Is(err, github.ErrNotFound):
			return failed(CodeRepoPrivate, owner+"/"+name+" is not a public repository GitHub can show (it does not exist, or it is private)", nil)
		// GitHub refused the token, not the address: the repository may well
		// be public, so saying "private or does not exist" here sent a
		// publisher of a public repository looking for a fault that was not
		// theirs (BRF-Tech/filex#118).
		case errors.Is(err, github.ErrBadCredentials) && x.req.UserToken != "":
			return failed(CodeGitHubTokenInvalid, "GitHub no longer accepts your sign-in, so your role on "+owner+"/"+name+" cannot be read: "+err.Error(), nil)
		case errors.Is(err, github.ErrBadCredentials):
			return failed(CodeGitHubUnavailable, "GitHub did not accept the store's own token reading "+owner+"/"+name+": "+err.Error(), nil)
		case err != nil && retryable(err):
			return "", "", "", nil, err
		case err != nil:
			return failed(CodeGitHubUnavailable, "GitHub did not answer about "+owner+"/"+name+": "+err.Error(), nil)
		}
		x.repo = repo
		x.res.RepoOwner, x.res.RepoName = repo.Owner.Login, repo.Name
		x.res.RepoID, x.res.OwnerID, x.res.OwnerType = repo.ID, repo.Owner.ID, repo.Owner.Type
		if h := cleanHomepage(repo.Homepage); h != "" {
			x.res.Homepage = h
		}
		data := map[string]any{"repo_id": repo.ID, "owner_id": repo.Owner.ID, "owner_type": repo.Owner.Type}
		if repo.Private || (repo.Visibility != "" && repo.Visibility != "public") {
			return failed(CodeRepoPrivate, owner+"/"+name+" is not public", data)
		}
		if repo.Archived || repo.Disabled {
			return failed(CodeRepoArchived, owner+"/"+name+" is archived on GitHub: it no longer takes changes, so it cannot carry an app the store keeps current", data)
		}
		if x.req.UserToken != "" {
			if repo.Permissions == nil {
				return failed(CodeRepoPermission, "GitHub did not say which role you have on "+owner+"/"+name, data)
			}
			if !repo.Permissions.CanList() {
				return failed(CodeRepoPermission, "listing "+owner+"/"+name+" needs the admin or maintain role on it", data)
			}
			data["permission"] = map[bool]string{true: "admin", false: "maintain"}[repo.Permissions.Admin]
		} else if x.req.Operator != nil {
			data["permission"] = "not asked: " + OperatorConsent
		} else {
			data["permission"] = "checked at submission"
		}
		return domain.CheckPassed, "", "", data, nil
	})
}

// 2. tag
func (x *run) tag() (bool, error) {
	return x.check(domain.CheckTag, func() (domain.CheckStatus, string, string, any, error) {
		t, err := x.r.gh.ResolveTag(x.ctx, x.req.UserToken, x.owner, x.name, x.req.Ref)
		switch {
		case errors.Is(err, github.ErrNotATag):
			return failed(CodeNotATag, x.req.Ref+" is a branch; submit a tag (a branch moves, a listed version must not)", nil)
		case errors.Is(err, github.ErrNotFound):
			return failed(CodeTagNotFound, "no tag "+x.req.Ref+" in "+x.owner+"/"+x.name, nil)
		case err != nil && retryable(err):
			return "", "", "", nil, err
		case err != nil:
			return failed(CodeTagNotFound, err.Error(), nil)
		}
		x.res.Ref, x.res.CommitSHA, x.res.TagVerified = t.Name, t.Commit, t.Verified
		// The license AT the commit: the default branch's may have changed
		// since the tag. No license file there is an empty License (the
		// backend's policy check says license_missing), not a failed tag.
		data := map[string]any{"name": t.Name, "commit": t.Commit, "annotated": t.Annotated,
			"verified": t.Verified, "verification_reason": t.Reason}
		l, err := x.r.gh.LicenseAt(x.ctx, x.req.UserToken, x.owner, x.name, t.Commit)
		switch {
		case err != nil && retryable(err):
			return "", "", "", nil, err
		case err != nil && !errors.Is(err, github.ErrNotFound):
			data["license_error"] = err.Error()
		case err == nil:
			x.res.License = l.SPDX()
			data["license_path"] = l.Path
			if l.Carried {
				data["license_note"] = "license carried from the default branch: identical blob"
			}
		}
		data["license"] = x.res.License
		return domain.CheckPassed, "", "", data, nil
	})
}

// fileAt reads a file at the commit and sorts its errors into a code, a
// retry, or nil.
func (x *run) fileAt(path string, max int64) (*github.File, error) {
	return x.r.gh.FileAt(x.ctx, x.owner, x.name, x.res.CommitSHA, path, max)
}

// 3. manifest, then 4. consent (and the description it names).
func (x *run) manifestAndConsent() (bool, error) {
	var p *parsed
	ok, err := x.check(domain.CheckManifest, func() (domain.CheckStatus, string, string, any, error) {
		f, err := x.fileAt(ManifestFile, x.r.lim.Manifest)
		switch {
		case errors.Is(err, github.ErrNotFound):
			return failed(CodeManifestMissing, "no "+ManifestFile+" at the root of "+x.owner+"/"+x.name+" at "+x.req.Ref, nil)
		case errors.Is(err, safehttp.ErrTooLarge):
			return failed(CodeTooLarge, fmt.Sprintf("%s is larger than %d MiB", ManifestFile, x.r.lim.Manifest>>20), nil)
		case err != nil && retryable(err):
			return "", "", "", nil, err
		case err != nil:
			return failed(CodeManifestBad, err.Error(), nil)
		}
		x.res.ManifestURL, x.res.ManifestSHA256 = f.URL, f.SHA256
		x.res.Manifest = json.RawMessage(f.Bytes)
		if !json.Valid(f.Bytes) {
			x.res.Manifest = nil
		}
		if p, err = parseManifest(f.Bytes); err != nil {
			return failed(CodeManifestBad, ManifestFile+": "+err.Error(), map[string]any{"sha256": f.SHA256})
		}
		x.res.Name, x.res.Version, x.res.Kind = p.m.Name, strings.TrimSpace(p.m.Version), p.kind
		x.res.FilexRange = p.rng.Canonical()
		x.res.Permissions, x.res.Languages = p.permissions, p.languages
		if h := cleanHomepage(p.m.Homepage); h != "" {
			x.res.Homepage = h
		}
		return domain.CheckPassed, "", "", map[string]any{"sha256": f.SHA256, "bytes": len(f.Bytes), "kind": p.kind, "filex": p.rng.String()}, nil
	})
	if !ok || err != nil {
		return ok, err
	}
	x.parsed = p
	return x.consent()
}

// consent is check 4: the operator's listing for an operator import, the
// repository's .filex-apps.json for every other submission.
func (x *run) consent() (bool, error) {
	if x.req.Operator != nil {
		return x.check(domain.CheckConsent, x.operatorConsent)
	}
	return x.check(domain.CheckConsent, x.repoConsent)
}

// repoConsent reads .filex-apps.json at the commit (and the description it
// names) and holds it to the publisher and to what was resolved before it.
func (x *run) repoConsent() (domain.CheckStatus, string, string, any, error) {
	f, err := x.fileAt(consent.FileName, consent.MaxBytes)
	switch {
	case errors.Is(err, github.ErrNotFound):
		return failed(CodeConsentMissing, "no "+consent.FileName+" at the root of "+x.owner+"/"+x.name+" at "+x.req.Ref+": the repository's owner consents to the listing with this file", nil)
	case errors.Is(err, safehttp.ErrTooLarge):
		return failed(CodeConsentInvalid, fmt.Sprintf("%s is larger than %d KiB", consent.FileName, consent.MaxBytes>>10), nil)
	case err != nil && retryable(err):
		return "", "", "", nil, err
	case err != nil:
		return failed(CodeConsentInvalid, err.Error(), nil)
	}
	c, err := consent.Parse(f.Bytes)
	if err != nil {
		var ce *consent.Error
		if errors.As(err, &ce) {
			return failed(CodeConsentInvalid, err.Error(), map[string]any{"problems": ce.Problems})
		}
		return failed(CodeConsentInvalid, err.Error(), nil)
	}
	x.res.Consent = *c
	if c.Publisher != x.req.Publisher {
		return failed(CodeConsentPub, fmt.Sprintf("%s names the publisher %q, not %q", consent.FileName, c.Publisher, x.req.Publisher), map[string]any{"publisher": c.Publisher})
	}
	if problems := x.conversionNeeds(c.Conversions); len(problems) > 0 {
		return failed(CodeConsentInvalid, consent.FileName+": "+strings.Join(problems, "; "), map[string]any{"problems": problems})
	}
	if c.Description != "" {
		d, err := x.fileAt(c.Description, MaxDescriptionBytes)
		switch {
		case errors.Is(err, github.ErrNotFound):
			return failed(CodeConsentInvalid, "the description "+c.Description+" is not in the repository at "+x.req.Ref, nil)
		case errors.Is(err, safehttp.ErrTooLarge):
			return failed(CodeConsentInvalid, fmt.Sprintf("the description %s is larger than %d KiB", c.Description, MaxDescriptionBytes>>10), nil)
		case err != nil && retryable(err):
			return "", "", "", nil, err
		case err != nil:
			return failed(CodeConsentInvalid, err.Error(), nil)
		}
		if !utf8.Valid(d.Bytes) {
			return failed(CodeConsentInvalid, "the description "+c.Description+" is not UTF-8 text", nil)
		}
		x.res.Description = string(d.Bytes)
	}
	return domain.CheckPassed, "", "", x.withConversions(map[string]any{"sha256": f.SHA256, "publisher": c.Publisher}, c.Conversions), nil
}

// conversionNeeds holds the listing's conversions to the manifest just read:
// every engine one needs must be one the manifest asks for.
func (x *run) conversionNeeds(convs []filetypes.Conversion) []string {
	if x.req.Kind == domain.SubmitStorage && len(convs) > 0 {
		// A storage plugin is a backend, not an action on a file: a listing
		// that says it converts something says something filex never does
		// with it.
		return []string{"a storage plugin converts no files: its listing declares no conversions"}
	}
	return consent.CheckNeeds(convs, x.res.Permissions, pipeline.ManifestEngines(x.res.Manifest))
}

// withConversions adds what the reviewer reads first about the declared
// conversions to the consent check's data: how many entries and pairs.
func (x *run) withConversions(data map[string]any, convs []filetypes.Conversion) map[string]any {
	if len(convs) > 0 {
		data["conversions"] = map[string]int{"entries": len(convs), "pairs": consent.Pairs(convs)}
	}
	return data
}

// operatorConsent is check 4 for an operator import (docs/ARCHITECTURE.md
// "Operator import (official publisher)"): the repository's .filex-apps.json
// is not read; the operator's listing stands in for it, and only when the
// publisher is official and verified (as the database says now) and the
// repository's owner measured in THIS resolution (its numeric id) is the
// publisher's bound GitHub owner. Every other check runs as for any
// submission.
func (x *run) operatorConsent() (domain.CheckStatus, string, string, any, error) {
	op := x.req.Operator
	c := op.Listing
	data := map[string]any{"source": "operator", "listing": op.Source, "publisher": c.Publisher,
		"official": op.PublisherOfficial, "verified": op.PublisherVerified,
		"owner_id": x.res.OwnerID, "publisher_owner_id": op.PublisherOwnerID}
	if c.Publisher != x.req.Publisher {
		return failed(CodeConsentPub, fmt.Sprintf("the operator's listing names the publisher %q, not %q", c.Publisher, x.req.Publisher), data)
	}
	if !op.PublisherOfficial || !op.PublisherVerified {
		return failed(CodePublisherNotOfficial, fmt.Sprintf("an operator import stands in for %s only for an official, verified publisher; %q is official=%v, verified=%v",
			consent.FileName, x.req.Publisher, op.PublisherOfficial, op.PublisherVerified), data)
	}
	if op.PublisherOwnerID == 0 || x.res.OwnerID != op.PublisherOwnerID {
		return failed(CodeOwnerMismatch, fmt.Sprintf("%s/%s belongs to the GitHub owner id %d, the publisher %q is bound to %d: consent by ownership does not hold",
			x.owner, x.name, x.res.OwnerID, x.req.Publisher, op.PublisherOwnerID), data)
	}
	if len(op.Description) > MaxDescriptionBytes {
		return failed(CodeConsentInvalid, fmt.Sprintf("the operator's description is larger than %d KiB", MaxDescriptionBytes>>10), data)
	}
	if !utf8.ValidString(op.Description) {
		return failed(CodeConsentInvalid, "the operator's description is not UTF-8 text", data)
	}
	if c.Description != "" && op.Description == "" {
		return failed(CodeConsentInvalid, "the operator's listing names the description "+c.Description+" but carries no text for it", data)
	}
	if problems := x.conversionNeeds(c.Conversions); len(problems) > 0 {
		data["problems"] = problems
		return failed(CodeConsentInvalid, "the operator's listing: "+strings.Join(problems, "; "), data)
	}
	x.res.Consent = c
	x.res.Description = op.Description
	return domain.CheckPassed, "", OperatorConsent, x.withConversions(data, c.Conversions), nil
}

// cleanHomepage keeps an http(s) address and drops anything else, as filex
// does with a manifest's homepage: it is drawn as a link, and a
// "javascript:" one must never reach a page.
func cleanHomepage(s string) string {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if s == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	return s
}

// expand turns a manifest address into the one filex downloads
// (wasmplugin.FetchGitHub): `{tag}` is the tag NAME, and an address without
// a scheme is a file of the repository at the tag.
func (x *run) expand(u string) string {
	u = strings.ReplaceAll(strings.TrimSpace(u), "{tag}", x.res.Ref)
	if !strings.Contains(u, "://") {
		u = x.r.gh.RawBase() + "/" + x.owner + "/" + x.name + "/" + url.PathEscape(x.res.Ref) + "/" + strings.TrimPrefix(u, "/")
	}
	return u
}

// 6. artifacts
func (x *run) artifacts() (bool, error) {
	p := x.parsed
	return x.check(domain.CheckArtifacts, func() (domain.CheckStatus, string, string, any, error) {
		if p.kind == domain.KindLanguagePack {
			return domain.CheckSkipped, "", "a language pack has no module and no interface bundle: its manifest is the whole download", nil, nil
		}
		hasWasm := p.m.Wasm != nil && strings.TrimSpace(p.m.Wasm.URL) != ""
		if !hasWasm && p.m.UI == nil {
			return failed(CodeManifestBad, "the manifest names no module (wasm.url) and no interface (ui.bundle); a repository install needs one of them", nil)
		}
		data := map[string]any{}
		fetch := func(what, addr, want string, max int64) (*domain.Artifact, string, string, error) {
			if strings.TrimSpace(addr) == "" {
				return nil, CodeManifestBad, "the manifest names no address for the " + what, nil
			}
			if strings.TrimSpace(want) == "" {
				return nil, CodeSHA256Required, "the manifest pins no sha256 for the " + what + "; a download must be pinned by hash", nil
			}
			return x.fetchPinned(what, x.expand(addr), want, "the manifest", max, data, nil)
		}
		if hasWasm {
			a, code, msg, err := fetch("module", p.m.Wasm.URL, p.m.Wasm.SHA256, x.r.lim.Wasm)
			if err != nil {
				return "", "", "", nil, err
			}
			if code != "" {
				return failed(code, msg, data)
			}
			x.res.Wasm = a
		}
		if p.m.UI != nil {
			a, code, msg, err := fetch("interface bundle", p.m.UI.Bundle.URL, p.m.UI.Bundle.SHA256, x.r.lim.UI)
			if err != nil {
				return "", "", "", nil, err
			}
			if code != "" {
				return failed(code, msg, data)
			}
			x.res.UI = a
		}
		return domain.CheckPassed, "", "", data, nil
	})
}

// 7. media
func (x *run) media() (bool, error) {
	c := x.res.Consent
	return x.check(domain.CheckMedia, func() (domain.CheckStatus, string, string, any, error) {
		type item struct {
			role, path string
			caption    domain.Text
		}
		var items []item
		if c.Icon != "" {
			items = append(items, item{roleIcon, c.Icon, nil})
		}
		for _, s := range c.Screenshots {
			items = append(items, item{roleScreenshot, s.Path, s.Caption})
		}
		if len(items) == 0 {
			return domain.CheckSkipped, "", "the consent file names no icon and no screenshots", nil, nil
		}
		var refs []domain.MediaRef
		for _, it := range items {
			var f *github.File
			var err error
			if x.req.Operator != nil {
				f, err = x.operatorFile(it.path)
			} else {
				f, err = x.fileAt(it.path, x.r.lim.Media)
			}
			switch {
			case errors.Is(err, github.ErrNotFound) && x.req.Operator != nil:
				return failed(CodeMediaInvalid, "the "+it.role+" "+it.path+" is not in the operator's listing", nil)
			case errors.Is(err, github.ErrNotFound):
				return failed(CodeMediaInvalid, "the "+it.role+" "+it.path+" is not in the repository at "+x.req.Ref, nil)
			case errors.Is(err, safehttp.ErrTooLarge):
				return failed(CodeTooLarge, fmt.Sprintf("the %s %s is larger than %d MiB", it.role, it.path, x.r.lim.Media>>20), nil)
			case err != nil && retryable(err):
				return "", "", "", nil, err
			case err != nil:
				return failed(CodeMediaInvalid, err.Error(), nil)
			}
			img, err := checkImage(it.role, f.Bytes)
			if err != nil {
				return failed(CodeMediaInvalid, it.role+" "+it.path+": "+err.Error(), nil)
			}
			ref := domain.MediaRef{Role: it.role, SHA256: f.SHA256, Type: img.typ.mime, Width: img.width, Height: img.height, Caption: it.caption}
			if x.r.ms != nil {
				if err := x.r.ms.Put(x.ctx, ref, f.URL, f.Bytes); err != nil {
					return "", "", "", nil, fmt.Errorf("store %s %s: %w", it.role, it.path, err)
				}
			}
			refs = append(refs, ref)
		}
		x.res.Media = refs
		data := map[string]any{"images": len(refs)}
		if x.req.Operator != nil {
			data["source"] = "operator"
		}
		return domain.CheckPassed, "", "", data, nil
	})
}

// operatorFile is an image of an operator's listing, under the same size cap
// as a file read from the repository; its address is recorded as
// "operator:<path>".
func (x *run) operatorFile(path string) (*github.File, error) {
	b, ok := x.req.Operator.Files[path]
	if !ok {
		return nil, fmt.Errorf("%w: %s", github.ErrNotFound, path)
	}
	if int64(len(b)) > x.r.lim.Media {
		return nil, safehttp.ErrTooLarge
	}
	return &github.File{URL: "operator:" + path, Bytes: b, SHA256: sum256(b)}, nil
}
