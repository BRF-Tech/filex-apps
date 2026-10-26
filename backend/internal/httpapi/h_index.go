package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

const indexCache = "public, max-age=300"

// getIndex serves the latest release's exact bytes.
func (h *handler) getIndex(w http.ResponseWriter, r *http.Request) {
	rel, ok := h.latest(w, r)
	if !ok {
		return
	}
	h.serveRelease(w, r, rel, false, indexCache)
}

func (h *handler) getIndexSig(w http.ResponseWriter, r *http.Request) {
	rel, ok := h.latest(w, r)
	if !ok {
		return
	}
	h.serveRelease(w, r, rel, true, indexCache)
}

func (h *handler) latest(w http.ResponseWriter, r *http.Request) (*store.Release, bool) {
	var rel *store.Release
	var err error
	if h.Index != nil {
		rel, err = h.Index.Latest(r.Context())
	} else {
		rel, err = store.LatestRelease(r.Context(), h.Pool)
	}
	if err != nil {
		h.fail(w, r, err)
		return nil, false
	}
	if rel == nil {
		writeError(w, http.StatusServiceUnavailable, "index_not_ready", "No signed index has been generated yet.")
		return nil, false
	}
	return rel, true
}

// serveRelease writes a release's body (or its signature) with the serial,
// an ETag (the body's sha256) and a short public cache.
func (h *handler) serveRelease(w http.ResponseWriter, r *http.Request, rel *store.Release, sig bool, cache string) {
	hd := w.Header()
	hd.Set("X-Index-Serial", strconv.FormatInt(rel.Serial, 10))
	hd.Set("Cache-Control", cache)
	if sig {
		hd.Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(rel.Signature + "\n"))
		return
	}
	etag := `"` + rel.SHA256 + `"`
	hd.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), rel.SHA256) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	hd.Set("Content-Type", "application/json")
	hd.Set("Content-Length", strconv.Itoa(len(rel.Body)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(rel.Body)
}

// etagMatches reads If-None-Match: "*", or a list of (weak or strong) tags.
func etagMatches(header, sha string) bool {
	if header == "" {
		return false
	}
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || strings.Trim(strings.TrimPrefix(t, "W/"), `"`) == sha {
			return true
		}
	}
	return false
}

// KeyView is one public key as /v1/keys.json lists it (also the base of
// the keys document `fapps-server keygen` prints).
type KeyView struct {
	ID      string `json:"id"`
	Use     string `json:"use"`
	Ed25519 string `json:"ed25519"`
	Status  string `json:"status"`
}

// getKeys lists the published public keys (informational).
func (h *handler) getKeys(w http.ResponseWriter, r *http.Request) {
	ttl := h.cfg.Index.TTL
	keys, err := store.PublishedKeys(r.Context(), h.Pool, h.now().Add(-ttl))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := []KeyView{}
	for _, k := range keys {
		out = append(out, KeyView{ID: k.ID, Use: k.Use, Ed25519: k.PublicHex, Status: k.Status})
	}
	w.Header().Set("Cache-Control", indexCache)
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

var mediaFileRe = regexp.MustCompile(`^([0-9a-f]{64})\.(png|jpg|webp)$`)

// getMedia serves a mirrored image by its sha256; the extension must be the
// stored type's.
func (h *handler) getMedia(w http.ResponseWriter, r *http.Request) {
	mm := mediaFileRe.FindStringSubmatch(r.PathValue("file"))
	if mm == nil {
		writeError(w, http.StatusNotFound, "not_found", "No such image.")
		return
	}
	if etagMatches(r.Header.Get("If-None-Match"), mm[1]) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	med, err := store.GetMedia(r.Context(), h.Pool, mm[1])
	if errors.Is(err, store.ErrNotFound) || (err == nil && index.MediaExt(med.ContentType) != "."+mm[2]) {
		writeError(w, http.StatusNotFound, "not_found", "No such image.")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", med.ContentType)
	hd.Set("Content-Length", strconv.Itoa(len(med.Blob)))
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("ETag", `"`+med.SHA256+`"`)
	hd.Set("Content-Disposition", "inline")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(med.Blob)
}

type releaseView struct {
	Serial      int64  `json:"serial"`
	GeneratedAt string `json:"generated_at"`
	ExpiresAt   string `json:"expires_at"`
	SHA256      string `json:"sha256"`
	KeyID       string `json:"key_id"`
	Reason      string `json:"reason"`
}

func releaseViewOf(rel store.Release) releaseView {
	return releaseView{Serial: rel.Serial, GeneratedAt: rfc(rel.GeneratedAt), ExpiresAt: rfc(rel.ExpiresAt),
		SHA256: rel.SHA256, KeyID: rel.KeyID, Reason: rel.Reason}
}

// listReleases: every signed index, newest first (history for audit and
// mirrors).
func (h *handler) listReleases(w http.ResponseWriter, r *http.Request) {
	p, pp, err := page(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	rels, total, err := store.ListReleases(r.Context(), h.Pool, store.Page{Page: p, PerPage: pp})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := []releaseView{}
	for _, rel := range rels {
		items = append(items, releaseViewOf(rel))
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *handler) release(w http.ResponseWriter, r *http.Request) (*store.Release, bool) {
	serial, err := strconv.ParseInt(r.PathValue("serial"), 10, 64)
	if err != nil || serial < 1 {
		writeError(w, http.StatusNotFound, "not_found", "No such release.")
		return nil, false
	}
	rel, err := store.GetRelease(r.Context(), h.Pool, serial)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such release.")
		return nil, false
	}
	if err != nil {
		h.fail(w, r, err)
		return nil, false
	}
	return &rel, true
}

func (h *handler) getReleaseBody(w http.ResponseWriter, r *http.Request) {
	if rel, ok := h.release(w, r); ok {
		h.serveRelease(w, r, rel, false, "public, max-age=31536000, immutable")
	}
}

func (h *handler) getReleaseSig(w http.ResponseWriter, r *http.Request) {
	if rel, ok := h.release(w, r); ok {
		h.serveRelease(w, r, rel, true, "public, max-age=31536000, immutable")
	}
}
