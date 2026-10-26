package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/licensing"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// licensing: the service, or nil (the feature is not wired: 503).
func (h *handler) lic(w http.ResponseWriter) *licensing.Service {
	if h.Licensing == nil {
		writeError(w, http.StatusServiceUnavailable, "data_key_missing", licensing.ErrDataKeyMissing.Message)
		return nil
	}
	return h.Licensing
}

type licenseView struct {
	ID              string `json:"id"`
	App             string `json:"app"`
	KeyPrefix       string `json:"key_prefix"`
	LicenseeName    string `json:"licensee_name"`
	LicenseeContact string `json:"licensee_contact,omitempty"`
	Seats           int    `json:"seats"`
	SeatsUsed       int    `json:"seats_used"`
	ValidUntil      string `json:"valid_until,omitempty"`
	UpdatesUntil    string `json:"updates_until,omitempty"`
	Status          string `json:"status"`
	Note            string `json:"note,omitempty"`
	CreatedBy       string `json:"created_by,omitempty"`
	CreatedAt       string `json:"created_at"`
	RevokedAt       string `json:"revoked_at,omitempty"`
}

func licenseViewOf(l store.License) licenseView {
	return licenseView{ID: l.ID, App: l.AppName, KeyPrefix: l.KeyPrefix, LicenseeName: l.LicenseeName,
		LicenseeContact: l.LicenseeContact, Seats: l.Seats, SeatsUsed: l.SeatsUsed, ValidUntil: l.ValidUntil,
		UpdatesUntil: l.UpdatesUntil, Status: l.Status, Note: l.Note, CreatedBy: l.CreatedByLogin,
		CreatedAt: rfc(l.CreatedAt), RevokedAt: rfcPtr(l.RevokedAt)}
}

type activationView struct {
	InstanceID   string `json:"instance_id"`
	FirstSeen    string `json:"first_seen"`
	LastSeen     string `json:"last_seen"`
	ReleasedAt   string `json:"released_at,omitempty"`
	Active       bool   `json:"active"`
	FilexVersion string `json:"filex_version,omitempty"`
}

func activationViewOf(a store.Activation) activationView {
	return activationView{InstanceID: a.InstanceID, FirstSeen: rfc(a.FirstSeen), LastSeen: rfc(a.LastSeen),
		ReleasedAt: rfcPtr(a.ReleasedAt), Active: a.ReleasedAt == nil, FilexVersion: a.FilexVersion}
}

// setPricing: PUT /api/v1/apps/{name}/pricing.
func (h *handler) setPricing(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.lic(w)
	if svc == nil {
		return
	}
	var in domain.Pricing
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := svc.SetPricing(r.Context(), r.PathValue("name"), in, actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type licensePageView struct {
	Items   []licenseView `json:"items"`
	Total   int           `json:"total"`
	Page    int           `json:"page"`
	PerPage int           `json:"per_page"`
}

// listLicenses: GET /api/v1/apps/{name}/licenses.
func (h *handler) listLicenses(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.lic(w)
	if svc == nil {
		return
	}
	pg, pp, err := page(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rows, total, err := svc.List(r.Context(), r.PathValue("name"), store.Page{Page: pg, PerPage: pp}, actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := licensePageView{Items: []licenseView{}, Total: total, Page: pg, PerPage: pp}
	for _, l := range rows {
		out.Items = append(out.Items, licenseViewOf(l))
	}
	writeJSON(w, http.StatusOK, out)
}

// createLicense: POST /api/v1/apps/{name}/licenses. The key is in this
// answer and nowhere else.
func (h *handler) createLicense(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.lic(w)
	if svc == nil {
		return
	}
	var in struct {
		LicenseeName    string `json:"licensee_name"`
		LicenseeContact string `json:"licensee_contact"`
		Seats           *int   `json:"seats"`
		ValidUntil      string `json:"valid_until"`
		UpdatesUntil    string `json:"updates_until"`
		Note            string `json:"note"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	ci := licensing.CreateInput{LicenseeName: in.LicenseeName, LicenseeContact: in.LicenseeContact,
		ValidUntil: in.ValidUntil, UpdatesUntil: in.UpdatesUntil, Note: in.Note, Seats: 1}
	if in.Seats != nil {
		ci.Seats = *in.Seats
		if ci.Seats == 0 {
			ci.Seats = -1
		}
	}
	lic, key, err := svc.Create(r.Context(), r.PathValue("name"), ci, actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"license": licenseViewOf(lic), "key": key})
}

// revokeLicense: POST /api/v1/licenses/{id}/revoke.
func (h *handler) revokeLicense(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.lic(w)
	if svc == nil {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	lic, err := svc.Revoke(r.Context(), r.PathValue("id"), in.Reason, actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, licenseViewOf(lic))
}

// listActivations: GET /api/v1/licenses/{id}/activations.
func (h *handler) listActivations(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.lic(w)
	if svc == nil {
		return
	}
	acts, err := svc.Activations(r.Context(), r.PathValue("id"), actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := []activationView{}
	for _, a := range acts {
		items = append(items, activationViewOf(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// releaseActivation: POST /api/v1/licenses/{id}/activations/{instance_id}/release.
func (h *handler) releaseActivation(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.lic(w)
	if svc == nil {
		return
	}
	act, err := svc.Release(r.Context(), r.PathValue("id"), r.PathValue("instance_id"), actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, activationViewOf(act))
}

// verifyLicense: POST /v1/licenses/verify, what a filex server calls. No
// session, no CSRF; limited per address and per key.
func (h *handler) verifyLicense(w http.ResponseWriter, r *http.Request) {
	svc := h.lic(w)
	if svc == nil {
		return
	}
	if !h.limit(w, r, ratelimit.LicenseVerifyPerIP, clientIP(r)) {
		return
	}
	var in struct {
		Key          string `json:"key"`
		App          string `json:"app"`
		InstanceID   string `json:"instance_id"`
		FilexVersion string `json:"filex_version"`
	}
	if err := decodeLenient(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.limit(w, r, ratelimit.LicenseVerifyPerKey, svc.KeySubject(in.Key)) {
		return
	}
	env, _, err := svc.Verify(r.Context(), licensing.VerifyRequest{Key: in.Key, App: in.App, InstanceID: in.InstanceID,
		FilexVersion: in.FilexVersion}, clientIP(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

// myApps: GET /api/v1/me/apps, the apps of the caller's publishers in every
// status.
func (h *handler) myApps(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	apps, err := store.CatalogApps(r.Context(), h.Pool, store.CatalogFilter{
		Member: p.AccountID, Statuses: []string{"listed", "unlisted", "revoked"}, WithVersion: true})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]adminAppView, 0, len(apps))
	for _, a := range apps {
		v := adminAppView{appSummaryView: h.appSummary(a, briefsToVersions(a.Versions)), Status: a.Status}
		if a.Status == "revoked" && a.RevokedAt != nil {
			v.RevokedAt = rfc(*a.RevokedAt)
		}
		items = append(items, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// decodeLenient reads a /v1/ request body (what a filex sends): one JSON
// object, unknown fields ignored (a later filex may send more), the same
// size and text rules as decode.
func decodeLenient(w http.ResponseWriter, r *http.Request, v any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return &badRequest{code: "bad_request", msg: "The body must be application/json."}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		return &badRequest{code: "bad_request", msg: "The body is too large or unreadable."}
	}
	if !json.Valid(body) || bytes.IndexByte(body, 0) >= 0 || bytes.Contains(body, []byte(`\u0000`)) {
		return &badRequest{code: "bad_request", msg: "The body is not JSON."}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(v); err != nil {
		return &badRequest{code: "bad_request", msg: "The body is not the expected JSON: " + err.Error()}
	}
	return nil
}
