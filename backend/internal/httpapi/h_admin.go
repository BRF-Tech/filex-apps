package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/plugincheck"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
	"github.com/brf-tech/filex-apps/backend/internal/validator"
)

type adminAppView struct {
	appSummaryView
	Status    string `json:"status"`
	RevokedAt string `json:"revoked_at,omitempty"`
}

type adminAppPageView struct {
	Items   []adminAppView `json:"items"`
	Total   int            `json:"total"`
	Page    int            `json:"page"`
	PerPage int            `json:"per_page"`
}

// adminListApps lists the apps in every status (the public list holds only
// the listed ones), for reviewers and administrators.
func (h *handler) adminListApps(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	q := r.URL.Query()
	pg, pp, err := page(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	statuses := []string{"listed", "unlisted", "revoked"}
	switch st := q.Get("status"); st {
	case "", "all":
	case "listed", "unlisted", "revoked":
		statuses = []string{st}
	default:
		h.fail(w, r, invalid("status must be listed, unlisted, revoked or all."))
		return
	}
	text := strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(text) > 100 {
		h.fail(w, r, invalid("q is at most 100 characters."))
		return
	}
	sortBy := q.Get("sort")
	switch sortBy {
	case "", "name", "updated":
	default:
		h.fail(w, r, invalid("sort must be name or updated."))
		return
	}
	words := strings.Fields(text)
	if len(words) > 8 {
		words = words[:8]
	}
	apps, err := store.CatalogApps(r.Context(), h.Pool, store.CatalogFilter{Words: words, Statuses: statuses})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rows := make([]adminAppView, 0, len(apps))
	for _, a := range apps {
		v := adminAppView{appSummaryView: h.appSummary(a, briefsToVersions(a.Versions)), Status: a.Status}
		if a.Status == "revoked" && a.RevokedAt != nil {
			v.RevokedAt = rfc(*a.RevokedAt)
		}
		rows = append(rows, v)
	}
	if sortBy == "updated" {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].UpdatedAt > rows[j].UpdatedAt })
	}
	out := adminAppPageView{Items: []adminAppView{}, Total: len(rows), Page: pg, PerPage: pp}
	for i := (pg - 1) * pp; i < len(rows) && i < pg*pp; i++ {
		out.Items = append(out.Items, rows[i])
	}
	writeJSON(w, http.StatusOK, out)
}

// adminUpdateApp lists or unlists an app, or corrects its categories.
func (h *handler) adminUpdateApp(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	var body struct {
		Status     *string   `json:"status"`
		Categories *[]string `json:"categories"`
	}
	if err := decode(w, r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	if body.Status != nil && *body.Status != "listed" && *body.Status != "unlisted" {
		h.fail(w, r, invalid("status must be listed or unlisted."))
		return
	}
	if body.Categories != nil && len(*body.Categories) > 3 {
		h.fail(w, r, invalid("categories holds at most 3 ids."))
		return
	}
	if _, _, err := h.Service.UpdateApp(r.Context(), r.PathValue("name"), pipeline.AppPatch{Status: body.Status, Categories: body.Categories}, actor(r, p)); err != nil {
		h.fail(w, r, err)
		return
	}
	if v, ok := h.appDetailOf(w, r, r.PathValue("name")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (h *handler) revokeApp(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	reason, ok := h.reason(w, r)
	if !ok {
		return
	}
	if _, _, err := h.Service.Revoke(r.Context(), r.PathValue("name"), reason, actor(r, p)); err != nil {
		h.fail(w, r, err)
		return
	}
	if v, ok := h.appDetailOf(w, r, r.PathValue("name")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (h *handler) unrevokeApp(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	if _, _, err := h.Service.Unrevoke(r.Context(), r.PathValue("name"), actor(r, p)); err != nil {
		h.fail(w, r, err)
		return
	}
	if v, ok := h.appDetailOf(w, r, r.PathValue("name")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (h *handler) yankVersion(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	reason, ok := h.reason(w, r)
	if !ok {
		return
	}
	app, v, _, err := h.Service.Yank(r.Context(), r.PathValue("name"), r.PathValue("version"), reason, actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeVersion(w, r, app, v)
}

func (h *handler) unyankVersion(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	app, v, _, err := h.Service.Unyank(r.Context(), r.PathValue("name"), r.PathValue("version"), actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeVersion(w, r, app, v)
}

func (h *handler) adminListPublishers(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	pg, pp, err := page(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "active", "suspended":
	default:
		h.fail(w, r, invalid("status must be pending, active or suspended."))
		return
	}
	pubs, total, err := store.ListPublishers(r.Context(), h.Pool, status, store.Page{Page: pg, PerPage: pp})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := []publisherView{}
	for _, x := range pubs {
		items = append(items, pubView(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *handler) adminUpdatePublisher(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	var body struct {
		Status       *string `json:"status"`
		StatusReason *string `json:"status_reason"`
		Verified     *bool   `json:"verified"`
		Official     *bool   `json:"official"`
	}
	if err := decode(w, r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	if body.Status != nil && *body.Status != "active" && *body.Status != "suspended" {
		h.fail(w, r, invalid("status must be active or suspended."))
		return
	}
	if body.StatusReason != nil {
		s, err := cleanText("status_reason", *body.StatusReason, 0, 500)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		body.StatusReason = &s
	}
	pub, _, err := h.Service.UpdatePublisher(r.Context(), r.PathValue("id"), store.PublisherPatch{
		Status: body.Status, StatusReason: body.StatusReason, Verified: body.Verified, Official: body.Official,
	}, actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pubView(pub))
}

func (h *handler) publishIndex(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	rel, err := h.Service.PublishNow(r.Context(), actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, releaseViewOf(*rel))
}

type signingKeyView struct {
	ID          string `json:"id"`
	Use         string `json:"use"`
	PublicHex   string `json:"public_hex"`
	Status      string `json:"status"`
	Demo        bool   `json:"demo"`
	CreatedAt   string `json:"created_at"`
	ActivatedAt string `json:"activated_at,omitempty"`
	RetiredAt   string `json:"retired_at,omitempty"`
}

func (h *handler) adminKeys(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	keys, err := store.ListKeys(r.Context(), h.Pool)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := []signingKeyView{}
	for _, k := range keys {
		items = append(items, signingKeyView{ID: k.ID, Use: k.Use, PublicHex: k.PublicHex, Status: k.Status, Demo: k.Demo,
			CreatedAt: rfc(k.CreatedAt), ActivatedAt: rfcPtr(k.ActivatedAt), RetiredAt: rfcPtr(k.RetiredAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// adminHealth: the role check, asked again now (an expired or refused
// FAPPS_ADMIN_TOKEN, or a fine-grained token the organization has not
// approved, shows here instead of looking like "nobody is an
// administrator").
func (h *handler) adminHealth(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	writeJSON(w, http.StatusOK, map[string]any{"roles": h.Roles.Probe(r.Context())})
}

// adminValidators: each configured filex's health and version.
func (h *handler) adminValidators(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	var items []validator.Status
	if h.Validators != nil {
		items = h.Validators.Statuses(r.Context())
	} else {
		for _, v := range h.cfg.Validators {
			items = append(items, validator.Status{Label: v.Label, BaseURL: v.BaseURL, Error: "no health probe on this server"})
		}
	}
	if items == nil {
		items = []validator.Status{}
	}
	// The storage plugins' validators (internal/plugincheck): their
	// heartbeat, their filex and the platform they run builds for.
	plugins := []plugincheck.Health{}
	if h.PluginValidators != nil {
		plugins = h.PluginValidators.Statuses()
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "plugin_validators": plugins})
}

type auditView struct {
	ID         int64  `json:"id"`
	At         string `json:"at"`
	Actor      string `json:"actor,omitempty"`
	ActorRole  string `json:"actor_role"`
	Action     string `json:"action"`
	TargetType string `json:"target_type,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
	Data       any    `json:"data,omitempty"`
}

// adminAudit: the audit log, newest first, and whether the whole chain
// verifies.
func (h *handler) adminAudit(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	pg, pp, err := page(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	rows, total, err := audit.List(r.Context(), h.Pool, audit.Filter{
		Action: strings.TrimSpace(q.Get("action")), TargetType: strings.TrimSpace(q.Get("target_type")),
		TargetID: strings.TrimSpace(q.Get("target_id")), Limit: pp, Offset: (pg - 1) * pp,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok, _, err := audit.Verify(r.Context(), h.Pool)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := []auditView{}
	for _, row := range rows {
		v := auditView{ID: row.ID, At: rfc(row.At), Actor: row.Actor, ActorRole: row.ActorRole, Action: row.Action,
			TargetType: row.TargetType, TargetID: row.TargetID}
		if len(row.Data) > 0 {
			v.Data = row.Data
		}
		items = append(items, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "chain_ok": ok})
}
