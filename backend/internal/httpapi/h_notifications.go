package httpapi

// Notifications (docs/ARCHITECTURE.md "Notifications"; internal/notify): a
// signed-in person's webhooks and browsers, and which events each hears.
// Everything here is the caller's own; the administrators' events are
// offered to an administrator only (and asked of GitHub again when they go
// out).

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// notifier: the notification service, or nil (503: not wired).
func (h *handler) notifier(w http.ResponseWriter) *notify.Service {
	if h.Notify == nil {
		writeError(w, http.StatusServiceUnavailable, "data_key_missing", notify.ErrDataKeyMissing.(*notify.Refusal).Message)
		return nil
	}
	return h.Notify
}

// failNotify answers a notify refusal with its status, else as fail does.
func (h *handler) failNotify(w http.ResponseWriter, r *http.Request, err error) {
	var ref *notify.Refusal
	if errors.As(err, &ref) {
		status := map[string]int{"invalid": http.StatusBadRequest, "not_found": http.StatusNotFound,
			"conflict": http.StatusConflict, "unavailable": http.StatusServiceUnavailable}[ref.Kind]
		if status == 0 {
			status = http.StatusBadRequest
		}
		writeError(w, status, ref.Code, ref.Message)
		return
	}
	h.fail(w, r, err)
}

func notifyActor(r *http.Request, p *session.Principal) notify.Actor {
	a := actor(r, p)
	return notify.Actor{AccountID: p.AccountID, Admin: p.Has(domain.RoleAdmin), Role: a.Role, IP: a.IP}
}

type channelResultView struct {
	Status     string `json:"status"`
	HTTPStatus *int   `json:"http_status,omitempty"`
	Error      string `json:"error,omitempty"`
	At         string `json:"at"`
}

func channelResult(c store.ChannelResult) *channelResultView {
	if c.Status == "" || c.At == nil {
		return nil
	}
	return &channelResultView{Status: c.Status, HTTPStatus: c.HTTPStatus, Error: c.Error, At: rfc(*c.At)}
}

type webhookView struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Format     string             `json:"format"`
	URLHint    string             `json:"url_hint"`
	HeaderName string             `json:"header_name,omitempty"`
	Signed     bool               `json:"signed"`
	Events     []string           `json:"events"`
	CreatedAt  string             `json:"created_at"`
	Last       *channelResultView `json:"last,omitempty"`
}

func webhookViewOf(v notify.WebhookView) webhookView {
	ev := v.Events
	if ev == nil {
		ev = []string{}
	}
	return webhookView{ID: v.ID, Name: v.Name, Format: v.Format, URLHint: v.URLHint, HeaderName: v.HeaderName, Signed: v.Signed,
		Events: ev, CreatedAt: rfc(v.CreatedAt), Last: channelResult(v.Last)}
}

type pushView struct {
	ID           string             `json:"id"`
	EndpointHost string             `json:"endpoint_host"`
	UserAgent    string             `json:"user_agent"`
	CreatedAt    string             `json:"created_at"`
	Last         *channelResultView `json:"last,omitempty"`
}

func pushViewOf(v notify.PushView) pushView {
	return pushView{ID: v.ID, EndpointHost: v.EndpointHost, UserAgent: v.UserAgent, CreatedAt: rfc(v.CreatedAt), Last: channelResult(v.Last)}
}

type deliveryResultView struct {
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Error      string `json:"error,omitempty"`
}

func deliveryResult(r notify.Result) deliveryResultView {
	return deliveryResultView{Status: r.Status, HTTPStatus: r.HTTPStatus, Error: r.Error}
}

// getNotifications: GET /api/v1/me/notifications.
func (h *handler) getNotifications(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.notifier(w)
	if svc == nil {
		return
	}
	st, err := svc.Settings(r.Context(), p.AccountID, p.Has(domain.RoleAdmin))
	if err != nil {
		h.failNotify(w, r, err)
		return
	}
	browsers := []pushView{}
	for _, b := range st.Pushes {
		browsers = append(browsers, pushViewOf(b))
	}
	hooks := []webhookView{}
	for _, wh := range st.Webhooks {
		hooks = append(hooks, webhookViewOf(wh))
	}
	push := map[string]any{"available": st.PushAvailable, "events": nonNilStrs(st.PushEvents), "browsers": browsers}
	if st.PushPublicKey != "" {
		push["public_key"] = st.PushPublicKey
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": st.Events, "formats": notify.Formats, "push": push, "webhooks": hooks})
}

// setPushEvents: PUT /api/v1/me/notifications/push.
func (h *handler) setPushEvents(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.notifier(w)
	if svc == nil {
		return
	}
	var in struct {
		Events []string `json:"events"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	if in.Events == nil {
		h.fail(w, r, invalid("events is required (an empty list turns every event off)."))
		return
	}
	ev, err := svc.SetPushEvents(r.Context(), p.AccountID, p.Has(domain.RoleAdmin), in.Events)
	if err != nil {
		h.failNotify(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": nonNilStrs(ev)})
}

// subscribePush: POST /api/v1/me/push-subscriptions (PushSubscription.toJSON()).
func (h *handler) subscribePush(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.notifier(w)
	if svc == nil {
		return
	}
	if !h.limit(w, r, ratelimit.NotifyChangePerAccount, accountKey(p)) {
		return
	}
	var in struct {
		Endpoint       string          `json:"endpoint"`
		ExpirationTime json.RawMessage `json:"expirationTime"`
		Keys           struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	var t notify.PushTarget
	t.Endpoint, t.Keys.P256dh, t.Keys.Auth = in.Endpoint, in.Keys.P256dh, in.Keys.Auth
	v, created, err := svc.SubscribePush(r.Context(), p.AccountID, p.Has(domain.RoleAdmin), t, r.UserAgent())
	if err != nil {
		h.failNotify(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, pushViewOf(v))
}

// unsubscribePush: DELETE /api/v1/me/push-subscriptions/{id}.
func (h *handler) unsubscribePush(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.notifier(w)
	if svc == nil {
		return
	}
	if err := svc.UnsubscribePush(r.Context(), p.AccountID, r.PathValue("id")); err != nil {
		h.failNotify(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testPush: POST /api/v1/me/push-subscriptions/{id}/test.
func (h *handler) testPush(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.notifier(w)
	if svc == nil {
		return
	}
	if !h.limit(w, r, ratelimit.NotifyTestPerAccount, accountKey(p)) {
		return
	}
	res, err := svc.TestPush(r.Context(), p.AccountID, r.PathValue("id"))
	if err != nil {
		h.failNotify(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deliveryResult(res))
}

// createWebhook: POST /api/v1/me/webhooks.
func (h *handler) createWebhook(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.notifier(w)
	if svc == nil {
		return
	}
	if !h.limit(w, r, ratelimit.NotifyChangePerAccount, accountKey(p)) {
		return
	}
	var in struct {
		Name        string   `json:"name"`
		Format      string   `json:"format"`
		URL         string   `json:"url"`
		HeaderName  string   `json:"header_name"`
		HeaderValue string   `json:"header_value"`
		Events      []string `json:"events"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := svc.CreateWebhook(r.Context(), notifyActor(r, p), notify.WebhookCreate{Name: in.Name, Format: in.Format, URL: in.URL,
		HeaderName: in.HeaderName, HeaderValue: in.HeaderValue, Events: in.Events})
	if err != nil {
		h.failNotify(w, r, err)
		return
	}
	body := map[string]any{"webhook": webhookViewOf(out.Webhook)}
	if out.Secret != "" {
		body["secret"] = out.Secret
	}
	writeJSON(w, http.StatusCreated, body)
}

// updateWebhook: PATCH /api/v1/me/webhooks/{id}.
func (h *handler) updateWebhook(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.notifier(w)
	if svc == nil {
		return
	}
	var in struct {
		Name   *string   `json:"name"`
		Events *[]string `json:"events"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	patch := notify.WebhookPatch{Name: in.Name}
	if in.Events != nil {
		patch.Events, patch.EventsSet = *in.Events, true
	}
	v, err := svc.UpdateWebhook(r.Context(), p.AccountID, p.Has(domain.RoleAdmin), r.PathValue("id"), patch)
	if err != nil {
		h.failNotify(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, webhookViewOf(v))
}

// deleteWebhook: DELETE /api/v1/me/webhooks/{id}.
func (h *handler) deleteWebhook(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.notifier(w)
	if svc == nil {
		return
	}
	if err := svc.DeleteWebhook(r.Context(), notifyActor(r, p), r.PathValue("id")); err != nil {
		h.failNotify(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testWebhook: POST /api/v1/me/webhooks/{id}/test.
func (h *handler) testWebhook(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.notifier(w)
	if svc == nil {
		return
	}
	if !h.limit(w, r, ratelimit.NotifyTestPerAccount, accountKey(p)) {
		return
	}
	res, err := svc.TestWebhook(r.Context(), p.AccountID, r.PathValue("id"))
	if err != nil {
		h.failNotify(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deliveryResult(res))
}

func nonNilStrs(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
