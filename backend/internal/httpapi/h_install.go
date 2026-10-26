package httpapi

import (
	"net/http"
	"strconv"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/install"
	"github.com/brf-tech/filex-apps/backend/internal/licensing"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/session"
)

// inst: the install service, or nil (the feature is not wired: 503).
func (h *handler) inst(w http.ResponseWriter) *install.Service {
	if h.Install == nil {
		writeError(w, http.StatusServiceUnavailable, "data_key_missing", licensing.ErrDataKeyMissing.Message)
		return nil
	}
	return h.Install
}

type instanceView struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
	// Connected: a filex server holds a key for the instance (Instance
	// connections); the rest only when it does.
	Connected      bool   `json:"connected"`
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	ConnectedAt    string `json:"connected_at,omitempty"`
	LastRequestAt  string `json:"last_request_at,omitempty"`
}

func instanceViewOf(i install.Instance) instanceView {
	return instanceView{ID: i.ID, Label: i.Label, URL: i.URL, CreatedAt: rfc(i.CreatedAt), Connected: i.Connected,
		KeyFingerprint: i.KeyFingerprint, ConnectedAt: rfcPtr(i.ConnectedAt), LastRequestAt: rfcPtr(i.LastRequestAt)}
}

// listInstances: GET /api/v1/me/instances (the owner's only).
func (h *handler) listInstances(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.inst(w)
	if svc == nil {
		return
	}
	list, err := svc.Instances(r.Context(), p.AccountID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := []instanceView{}
	for _, i := range list {
		items = append(items, instanceViewOf(i))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// saveInstance: POST /api/v1/me/instances.
func (h *handler) saveInstance(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.inst(w)
	if svc == nil {
		return
	}
	var in struct {
		URL   string `json:"url"`
		Label string `json:"label"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	i, created, err := svc.SaveInstance(r.Context(), in.URL, in.Label, actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, instanceViewOf(i))
}

// deleteInstance: DELETE /api/v1/me/instances/{id}.
func (h *handler) deleteInstance(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.inst(w)
	if svc == nil {
		return
	}
	if err := svc.DeleteInstance(r.Context(), r.PathValue("id"), actor(r, p)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// createIntent: POST /api/v1/install-intents.
func (h *handler) createIntent(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.inst(w)
	if svc == nil {
		return
	}
	var in struct {
		App           string `json:"app"`
		Version       string `json:"version"`
		FilexURL      string `json:"filex_url"`
		InstanceID    string `json:"instance_id"`
		SaveInstance  bool   `json:"save_instance"`
		InstanceLabel string `json:"instance_label"`
		LicenseKey    string `json:"license_key"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	if !h.limit(w, r, ratelimit.InstallIntentPerAccount, strconv.FormatInt(p.AccountID, 10)) {
		return
	}
	c, err := svc.CreateIntent(r.Context(), install.IntentRequest{App: in.App, Version: in.Version, FilexURL: in.FilexURL,
		InstanceID: in.InstanceID, SaveInstance: in.SaveInstance, InstanceLabel: in.InstanceLabel, LicenseKey: in.LicenseKey},
		actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := map[string]any{"token": c.Token, "magic_link": c.MagicLink, "expires_at": rfc(c.ExpiresAt),
		"app": c.App, "version": c.Version}
	if c.Instance != nil {
		out["instance"] = instanceViewOf(*c.Instance)
	}
	writeJSON(w, http.StatusCreated, out)
}

// readIntent: GET /v1/install/{token}, what a filex server reads.
func (h *handler) readIntent(w http.ResponseWriter, r *http.Request) {
	svc := h.inst(w)
	if svc == nil {
		return
	}
	if !h.limit(w, r, ratelimit.InstallReadPerIP, clientIP(r)) {
		return
	}
	env, err := svc.ReadIntent(r.Context(), r.PathValue("token"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

// completeIntent: POST /v1/install/{token}/complete; the first call spends
// the token.
func (h *handler) completeIntent(w http.ResponseWriter, r *http.Request) {
	svc := h.inst(w)
	if svc == nil {
		return
	}
	if !h.limit(w, r, ratelimit.InstallReadPerIP, clientIP(r)) {
		return
	}
	var in struct {
		InstanceID string `json:"instance_id"`
		Result     string `json:"result"`
	}
	if err := decodeLenient(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	c, err := svc.CompleteIntent(r.Context(), r.PathValue("token"), in.InstanceID, domain.InstallResult(in.Result), clientIP(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token_id": c.TokenID, "result": c.Result, "completed_at": rfc(c.CompletedAt)})
}
