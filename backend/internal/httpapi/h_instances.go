package httpapi

// Instance connections (docs/ARCHITECTURE.md "Instance connections";
// internal/install/connect.go): a person connects a saved instance to its
// filex with a one-time code, and that filex's server then asks for install
// intents in its own name, with requests signed by its own key.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/brf-tech/filex-apps/backend/internal/install"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// maxInstanceBody bounds a connected filex's request body.
const maxInstanceBody = 16 << 10

// accountKey is an account's rate-limit subject.
func accountKey(p *session.Principal) string { return strconv.FormatInt(p.AccountID, 10) }

// actorAt is an actor known only by its address (a filex server).
func actorAt(r *http.Request) pipeline.Actor { return pipeline.Actor{IP: clientIP(r)} }

// createConnectCode: POST /api/v1/me/instances/{id}/connect-code.
func (h *handler) createConnectCode(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.inst(w)
	if svc == nil {
		return
	}
	if !h.limit(w, r, ratelimit.ConnectCodePerAccount, accountKey(p)) {
		return
	}
	c, err := svc.NewConnectCode(r.Context(), r.PathValue("id"), actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"code": c.Code, "expires_at": rfc(c.ExpiresAt)})
}

// disconnectInstance: DELETE /api/v1/me/instances/{id}/connection.
func (h *handler) disconnectInstance(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	svc := h.inst(w)
	if svc == nil {
		return
	}
	if err := svc.Disconnect(r.Context(), r.PathValue("id"), actor(r, p), false); err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// connectInstance: POST /v1/instances/connect - a filex server spends a code
// with its public key. Public (the code is the secret), rate limited.
func (h *handler) connectInstance(w http.ResponseWriter, r *http.Request) {
	svc := h.inst(w)
	if svc == nil {
		return
	}
	if !h.limit(w, r, ratelimit.InstanceConnectPerIP, clientIP(r)) {
		return
	}
	var in struct {
		Code            string `json:"code"`
		PublicKey       string `json:"public_key"`
		FilexOrigin     string `json:"filex_origin"`
		FilexInstanceID string `json:"filex_instance_id"`
	}
	if err := decodeLenient(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	env, err := svc.Connect(r.Context(), install.ConnectRequest{Code: strings.TrimSpace(in.Code), PublicKey: in.PublicKey,
		FilexOrigin: in.FilexOrigin, FilexInstanceID: in.FilexInstanceID}, clientIP(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

// signedInstance checks a connected filex's signed request: the per-address
// limit first (before any database work), then the signature, the timestamp
// and the nonce. Answers the instance and the body it read, or false after
// answering.
func (h *handler) signedInstance(w http.ResponseWriter, r *http.Request) (store.SavedInstance, []byte, bool) {
	svc := h.inst(w)
	if svc == nil {
		return store.SavedInstance{}, nil, false
	}
	if !h.limit(w, r, ratelimit.InstanceRequestPerIP, clientIP(r)) {
		return store.SavedInstance{}, nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInstanceBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "The body is too large or unreadable.")
		return store.SavedInstance{}, nil, false
	}
	inst, err := svc.VerifyRequest(r.Context(), install.SignedRequest{
		Method: r.Method, Path: r.URL.EscapedPath(),
		InstanceID: r.Header.Get(install.HeaderInstance), Key: r.Header.Get(install.HeaderKey),
		Timestamp: r.Header.Get(install.HeaderTimestamp), Nonce: r.Header.Get(install.HeaderNonce),
		Signature: r.Header.Get(install.HeaderSignature), Body: body,
	}, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return store.SavedInstance{}, nil, false
	}
	return inst, body, true
}

// instanceSelf: GET /v1/instances/{id}/connection - what a connected filex
// reads about its own connection.
func (h *handler) instanceSelf(w http.ResponseWriter, r *http.Request) {
	inst, _, ok := h.signedInstance(w, r)
	if !ok {
		return
	}
	v, err := h.Install.Self(inst)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instance_id": v.ID, "filex_origin": v.FilexOrigin,
		"key_fingerprint": v.KeyFingerprint, "connected_at": rfc(v.ConnectedAt)})
}

// instanceDisconnect: DELETE /v1/instances/{id}/connection - the connected
// filex lets go of its key.
func (h *handler) instanceDisconnect(w http.ResponseWriter, r *http.Request) {
	inst, _, ok := h.signedInstance(w, r)
	if !ok {
		return
	}
	if err := h.Install.Disconnect(r.Context(), inst.ID, actorAt(r), true); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// instanceIntent: POST /v1/instances/{id}/intents - a connected filex asks
// for an install intent for its own address.
func (h *handler) instanceIntent(w http.ResponseWriter, r *http.Request) {
	inst, body, ok := h.signedInstance(w, r)
	if !ok {
		return
	}
	if !h.limit(w, r, ratelimit.InstanceIntentPerInstance, inst.ID) {
		return
	}
	var in struct {
		App        string `json:"app"`
		Version    string `json:"version"`
		LicenseKey string `json:"license_key"`
	}
	if err := decodeBytes(body, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	c, err := h.Install.CreateInstanceIntent(r.Context(), inst, install.InstanceIntentRequest{App: in.App, Version: in.Version,
		LicenseKey: in.LicenseKey}, clientIP(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": c.Token, "token_id": c.TokenID, "expires_at": rfc(c.ExpiresAt),
		"app": c.App, "version": c.Version})
}

// decodeBytes reads one JSON object from a body already read, the rules of
// decode (no unknown field, no trailing data, no NUL, valid UTF-8).
func decodeBytes(body []byte, v any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	if !utf8.Valid(body) || bytes.Contains(body, []byte(`\u0000`)) || bytes.IndexByte(body, 0) >= 0 {
		return &badRequest{code: "bad_request", msg: "The body holds invalid text."}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &badRequest{code: "bad_request", msg: "The body is not the expected JSON: " + err.Error()}
	}
	if dec.More() {
		return &badRequest{code: "bad_request", msg: "The body holds more than one JSON value."}
	}
	return nil
}
