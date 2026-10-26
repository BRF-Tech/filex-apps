package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
)

// storeInfo is how the store presents itself (GET /api/v1/store): what the
// web interface shows as its brand and in its footer. Every field but the
// name is absent when not configured.
type storeInfo struct {
	Name         string `json:"name"`
	LogoURL      string `json:"logo_url,omitempty"`
	OperatorName string `json:"operator_name,omitempty"`
	OperatorURL  string `json:"operator_url,omitempty"`
	Contact      string `json:"contact,omitempty"`
	TermsURL     string `json:"terms_url,omitempty"`
	PrivacyURL   string `json:"privacy_url,omitempty"`
}

// storeRoutes are answered from the configuration alone, so they work
// before the database is ready too.
func (h *handler) storeRoutes(mux *http.ServeMux) {
	b := h.cfg.Brand
	info := storeInfo{Name: b.Name, OperatorName: b.OperatorName, OperatorURL: b.OperatorURL, Contact: b.Contact,
		TermsURL: b.TermsURL, PrivacyURL: b.PrivacyURL}
	var etag string
	if len(b.Logo) > 0 {
		sum := sha256.Sum256(b.Logo)
		etag = hex.EncodeToString(sum[:])
		// The address changes with the image, so a browser never keeps an
		// old logo past a restart with a new one.
		info.LogoURL = "/api/v1/store/logo?v=" + etag[:16]
	}
	// Not under the per-address API limit, like the logo: the answer is a
	// fixed value made at start and every page load asks for it, so counting
	// it only spends a visitor's budget for the endpoints that do work (the
	// e2e stack's three browsers share one address and ran out of it once
	// this call joined every page load).
	route(mux, "/api/v1/store", methods{"GET": func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		writeJSON(w, http.StatusOK, info)
	}})
	route(mux, "/api/v1/store/logo", methods{"GET": func(w http.ResponseWriter, r *http.Request) {
		if len(b.Logo) == 0 {
			writeError(w, http.StatusNotFound, "not_found", "This store has no logo of its own.")
			return
		}
		hd := w.Header()
		// The operator's file, shown as an image: an SVG opened on its own
		// runs no script and loads nothing.
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		hd.Set("Cache-Control", "public, max-age=86400")
		hd.Set("ETag", `"`+etag+`"`)
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		hd.Set("Content-Type", b.LogoType)
		hd.Set("Content-Length", strconv.Itoa(len(b.Logo)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(b.Logo)
		}
	}})
}
