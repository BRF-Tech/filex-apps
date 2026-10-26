package httpapi

import (
	"encoding/json"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/licensing"
	"github.com/brf-tech/filex-apps/backend/internal/markdown"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// The JSON shapes of api/openapi.yaml.

type publisherRefView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Verified bool   `json:"verified"`
	Official bool   `json:"official"`
	// AvatarURL: the publisher's mirrored avatar, where the answer carries
	// it (an app's publisher); "" when it has none.
	AvatarURL string `json:"avatar_url,omitempty"`
}

type publisherView struct {
	publisherRefView
	GitHubOwner  string `json:"github_owner"`
	OwnerType    string `json:"owner_type"`
	Homepage     string `json:"homepage,omitempty"`
	Status       string `json:"status"`
	StatusReason string `json:"status_reason,omitempty"`
	CreatedAt    string `json:"created_at"`
	Description  string `json:"description,omitempty"`
	AvatarURL    string `json:"avatar_url,omitempty"`
}

func refView(p store.PublisherRef) publisherRefView {
	return publisherRefView{ID: p.ID, Name: p.Name, Verified: p.Verified, Official: p.Official}
}

// appPublisherView is an app's publisher with its mirrored avatar.
func (h *handler) appPublisherView(p store.PublisherRef) publisherRefView {
	v := refView(p)
	if p.AvatarSHA256 != "" && p.AvatarType != "" {
		v.AvatarURL = h.mediaURL(p.AvatarSHA256, p.AvatarType)
	}
	return v
}

func pubView(p store.Publisher) publisherView {
	return publisherView{
		publisherRefView: publisherRefView{ID: p.ID, Name: p.Name, Verified: p.Verified, Official: p.Official},
		GitHubOwner:      p.GitHubOwner, OwnerType: p.OwnerType, Homepage: p.Homepage, Status: p.Status,
		StatusReason: p.StatusReason, CreatedAt: rfc(p.CreatedAt), Description: p.Description,
	}
}

type withdrawalView struct {
	Reason string `json:"reason"`
	At     string `json:"at"`
}

type mediaView struct {
	URL     string            `json:"url"`
	SHA256  string            `json:"sha256"`
	Caption map[string]string `json:"caption,omitempty"`
}

type appSummaryView struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Label      map[string]string `json:"label"`
	Summary    map[string]string `json:"summary,omitempty"`
	Publisher  publisherRefView  `json:"publisher"`
	Categories []string          `json:"categories"`
	IconURL    string            `json:"icon_url,omitempty"`
	Latest     string            `json:"latest"`
	Compatible string            `json:"compatible,omitempty"`
	UpdatedAt  string            `json:"updated_at"`
	Pricing    *domain.Pricing   `json:"pricing,omitempty"`
	// MatchedExtensions (a file-type search only): what the app does with
	// each extension searched for, and apart what it offers on every file.
	MatchedExtensions []filetypes.Use `json:"matched_extensions,omitempty"`
}

type versionSummaryView struct {
	Version     string          `json:"version"`
	Ref         string          `json:"ref"`
	Filex       string          `json:"filex"`
	PublishedAt string          `json:"published_at"`
	Security    bool            `json:"security"`
	Yanked      *withdrawalView `json:"yanked"`
}

type appDetailView struct {
	appSummaryView
	Repo            string               `json:"repo"`
	Homepage        string               `json:"homepage,omitempty"`
	License         string               `json:"license,omitempty"`
	Status          string               `json:"status"`
	Revoked         *withdrawalView      `json:"revoked"`
	DescriptionHTML string               `json:"description_html,omitempty"`
	Screenshots     []mediaView          `json:"screenshots"`
	Versions        []versionSummaryView `json:"versions"`
}

type artifactView struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
	Sig    string `json:"sig"`
}

type languageView struct {
	Code    string `json:"code"`
	Percent int    `json:"percent"`
}

type installView struct {
	GitHubRepo string `json:"github_repo"`
	Ref        string `json:"ref"`
}

type versionDetailView struct {
	versionSummaryView
	Commit      string                 `json:"commit"`
	Manifest    artifactView           `json:"manifest"`
	Wasm        *artifactView          `json:"wasm,omitempty"`
	UI          *artifactView          `json:"ui,omitempty"`
	Permissions []domain.PermissionRow `json:"permissions"`
	Languages   []languageView         `json:"languages"`
	Engines     []string               `json:"engines"`
	NotesURL    string                 `json:"notes_url,omitempty"`
	Install     installView            `json:"install"`
	// FileTypes: the kinds of file the version works with, kind by kind
	// (its manifest, read by internal/filetypes).
	FileTypes []filetypes.Use `json:"file_types"`
	// Conversions: what the version converts, kind by kind (its listing's
	// declaration, migration 0008); empty when it declares none.
	Conversions []filetypes.ConversionRow `json:"conversions"`
	// Builds: a storage plugin's builds, one per platform in the store's
	// order (domain.StoragePlatforms), each pinned and signed; absent for an
	// app. Manifest is then the release's filex-storage.json.
	Builds []buildView `json:"builds,omitempty"`
	// Conformance: what the store's plugin validator measured of a storage
	// plugin build (absent for an app).
	Conformance *domain.Conformance `json:"conformance,omitempty"`
}

// buildView is one platform's build of a storage plugin.
type buildView struct {
	Platform string `json:"platform"`
	artifactView
}

func (h *handler) mediaURL(sha, contentType string) string {
	return index.MediaURL(h.cfg.PublicURL, sha, contentType)
}

func versionSummary(v store.Version) versionSummaryView {
	out := versionSummaryView{Version: v.Version, Ref: v.Ref, Filex: v.FilexRange, PublishedAt: rfc(v.PublishedAt), Security: v.Security}
	if v.YankedAt != nil {
		out.Yanked = &withdrawalView{Reason: v.YankedReason, At: rfc(*v.YankedAt)}
	}
	return out
}

func versionDetail(app store.App, v store.Version, types filetypes.Set) versionDetailView {
	out := versionDetailView{
		FileTypes:          filetypes.Group(types),
		Conversions:        filetypes.ConversionTable(types),
		versionSummaryView: versionSummary(v),
		Commit:             v.Commit,
		Manifest:           artifactView{URL: v.ManifestURL, SHA256: v.ManifestSHA256, Sig: v.ManifestSig},
		Permissions:        []domain.PermissionRow{},
		Languages:          []languageView{},
		Engines:            append([]string{}, v.Engines...),
		NotesURL:           v.NotesURL,
		Install:            installView{GitHubRepo: app.Repo(), Ref: v.Ref},
	}
	if v.Wasm != nil {
		out.Wasm = &artifactView{URL: v.Wasm.URL, SHA256: v.Wasm.SHA256, Size: v.Wasm.Size, Sig: v.Wasm.Sig}
	}
	if v.UI != nil {
		out.UI = &artifactView{URL: v.UI.URL, SHA256: v.UI.SHA256, Size: v.UI.Size, Sig: v.UI.Sig}
	}
	if len(v.PermissionRows) > 0 {
		_ = json.Unmarshal(v.PermissionRows, &out.Permissions)
	}
	if len(v.Languages) > 0 {
		_ = json.Unmarshal(v.Languages, &out.Languages)
	}
	if out.Permissions == nil {
		out.Permissions = []domain.PermissionRow{}
	}
	if out.Languages == nil {
		out.Languages = []languageView{}
	}
	if len(v.Binaries) > 0 {
		out.Builds = []buildView{}
		for _, plat := range domain.StoragePlatforms {
			if b, ok := v.Binaries[plat]; ok {
				out.Builds = append(out.Builds, buildView{Platform: plat, artifactView: artifactView{URL: b.URL, SHA256: b.SHA256, Size: b.Size, Sig: b.Sig}})
			}
		}
	}
	if v.Conformance != nil {
		c := *v.Conformance
		c.Capabilities = append([]string{}, c.Capabilities...)
		out.Conformance = &c
	}
	return out
}

func (h *handler) appDetail(a store.App, pub store.PublisherRef, iconType string, versions []store.Version, shots []store.AppMedia) appDetailView {
	index.SortVersions(versions)
	out := appDetailView{
		appSummaryView: h.appSummary(store.CatalogApp{App: a, Publisher: pub, IconType: iconType}, versions),
		Repo:           a.Repo(), Homepage: a.Homepage, License: a.License, Status: a.Status,
		DescriptionHTML: markdown.Render(a.DescriptionMD),
		Screenshots:     []mediaView{},
		Versions:        []versionSummaryView{},
	}
	if a.Status == "revoked" && a.RevokedAt != nil {
		out.Revoked = &withdrawalView{Reason: a.RevokedReason, At: rfc(*a.RevokedAt)}
	}
	for _, m := range shots {
		out.Screenshots = append(out.Screenshots, mediaView{URL: h.mediaURL(m.SHA256, m.ContentType), SHA256: m.SHA256, Caption: nilIfEmpty(m.Caption)})
	}
	for _, v := range versions {
		out.Versions = append(out.Versions, versionSummary(v))
	}
	return out
}

// appSummary builds a catalog card; versions may be full rows or briefs
// turned into rows (only Version, FilexRange, YankedAt, PublishedAt are read).
func (h *handler) appSummary(c store.CatalogApp, versions []store.Version) appSummaryView {
	out := appSummaryView{
		Name: c.Name, Kind: c.Kind, Label: nonNil(c.Label), Summary: nilIfEmpty(c.Summary),
		Publisher: h.appPublisherView(c.Publisher), Categories: append([]string{}, c.Categories...),
		Latest: index.Latest(versions), UpdatedAt: rfc(c.UpdatedAt),
	}
	pricing := licensing.PricingOf(c.App)
	out.Pricing = &pricing
	if c.IconSHA256 != "" && c.IconType != "" {
		out.IconURL = h.mediaURL(c.IconSHA256, c.IconType)
	}
	for _, v := range versions {
		if v.PublishedAt.After(c.UpdatedAt) {
			out.UpdatedAt = rfc(v.PublishedAt)
		}
	}
	return out
}

type submissionView struct {
	ID        int64            `json:"id"`
	Publisher publisherRefView `json:"publisher"`
	App       string           `json:"app,omitempty"`
	Version   string           `json:"version,omitempty"`
	Repo      string           `json:"repo"`
	Ref       string           `json:"ref"`
	// Kind is what was submitted: app (an app or a language pack) or
	// storage (a storage plugin).
	Kind        string       `json:"kind"`
	Status      string       `json:"status"`
	Attempt     int          `json:"attempt"`
	Failure     *failureView `json:"failure,omitempty"`
	SubmittedBy string       `json:"submitted_by,omitempty"`
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
	DecidedAt   string       `json:"decided_at,omitempty"`
}

type failureView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (h *handler) submissionView(s store.Submission) submissionView {
	out := submissionView{
		ID: s.ID, Publisher: refView(s.Publisher), App: s.Name, Version: s.Version, Repo: s.Repo, Ref: s.Ref, Kind: s.Kind,
		Status: s.Status, Attempt: s.Attempt, SubmittedBy: s.SubmitterLogin, CreatedAt: rfc(s.CreatedAt),
		UpdatedAt: rfc(s.UpdatedAt), DecidedAt: rfcPtr(s.DecidedAt),
	}
	if s.FailureCode != "" {
		out.Failure = &failureView{Code: s.FailureCode, Message: s.FailureMessage}
	}
	return out
}

type checkView struct {
	Kind      string          `json:"kind"`
	Validator string          `json:"validator,omitempty"`
	Status    string          `json:"status"`
	Code      string          `json:"code,omitempty"`
	Message   string          `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	StartedAt string          `json:"started_at"`
	EndedAt   string          `json:"ended_at,omitempty"`
}

func checkViews(cs []domain.CheckResult) []checkView {
	out := []checkView{}
	for _, c := range cs {
		v := checkView{Kind: string(c.Kind), Validator: c.Validator, Status: string(c.Status), Code: c.Code,
			Message: c.Message, StartedAt: rfc(c.StartedAt), EndedAt: rfcPtr(c.EndedAt)}
		if len(c.Data) > 0 && c.Data[0] == '{' {
			v.Data = c.Data
		}
		out = append(out, v)
	}
	return out
}

type reviewView struct {
	ID        int64  `json:"id"`
	Reviewer  string `json:"reviewer"`
	Decision  string `json:"decision"`
	Note      string `json:"note,omitempty"`
	Security  bool   `json:"security"`
	CreatedAt string `json:"created_at"`
}

func reviewViewOf(r store.Review) reviewView {
	return reviewView{ID: r.ID, Reviewer: r.Reviewer, Decision: r.Decision, Note: r.Note, Security: r.Security, CreatedAt: rfc(r.CreatedAt)}
}

func nilIfEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// dryRunView keeps the reviewer's answer without the bulky raw body.
type dryRunView struct {
	domain.DryRunResult
	Raw json.RawMessage `json:"raw,omitempty"`
}
