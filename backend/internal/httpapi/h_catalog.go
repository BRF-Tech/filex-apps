package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filexrange"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

const (
	reviewerRole = domain.RoleReviewer
	adminRole    = domain.RoleAdmin
)

func (h *handler) now() time.Time { return time.Now() }

var (
	appNameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	publisherIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)
	filexVerRe    = regexp.MustCompile(`^[0-9]+[.][0-9]+[.][0-9]+$`)
	githubLoginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
)

func briefsToVersions(bs []store.VersionBrief) []store.Version {
	out := make([]store.Version, 0, len(bs))
	for _, b := range bs {
		v := store.Version{ID: b.ID, Version: b.Version, FilexRange: b.FilexRange, PublishedAt: b.PublishedAt}
		if b.Yanked {
			t := b.PublishedAt
			v.YankedAt = &t
		}
		out = append(out, v)
	}
	return out
}

type appPageView struct {
	Items   []appSummaryView `json:"items"`
	Total   int              `json:"total"`
	Page    int              `json:"page"`
	PerPage int              `json:"per_page"`
	// FileTypes is set when `q` reads as a file-type search (".docx",
	// "pdf docx"): the items are then the apps that work with those kinds.
	FileTypes *fileTypeSearchView `json:"file_types,omitempty"`
}

// listApps searches the listed apps.
func (h *handler) listApps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, pp, err := page(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	text := strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(text) > 100 {
		h.fail(w, r, invalid("q is at most 100 characters."))
		return
	}
	kind := q.Get("kind")
	if kind != "" && kind != string(domain.KindApp) && kind != string(domain.KindLanguagePack) && kind != string(domain.KindStorage) {
		h.fail(w, r, invalid("kind must be app, language_pack or storage."))
		return
	}
	filex := q.Get("filex")
	var host filexrange.Version
	if filex != "" {
		var err error
		if host, err = filexrange.ParseVersion(filex); err != nil || !filexVerRe.MatchString(filex) {
			h.fail(w, r, invalid("filex must be a version like 0.50.0."))
			return
		}
	}
	verified := false
	switch q.Get("verified") {
	case "", "false":
	case "true":
		verified = true
	default:
		h.fail(w, r, invalid("verified must be true or false."))
		return
	}
	sortBy := q.Get("sort")
	switch sortBy {
	case "", "name", "updated", "published":
	default:
		h.fail(w, r, invalid("sort must be name, updated or published."))
		return
	}
	words := strings.Fields(text)
	if len(words) > 8 {
		words = words[:8]
	}
	// A search made only of extensions (".docx", "*.pdf", "pdf docx") is a
	// file-type search: the apps that work with those kinds, not the ones
	// whose text holds the words.
	exts, byType, err := h.fileTypeQuery(r.Context(), text)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	filter := store.CatalogFilter{
		Words: words, Kind: kind, Category: q.Get("category"), Publisher: q.Get("publisher"), Verified: verified, WithVersion: true,
	}
	if byType {
		filter.Words = nil
	}
	apps, err := store.CatalogApps(r.Context(), h.Pool, filter)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var rows []catalogRow
	for _, a := range apps {
		versions := briefsToVersions(a.Versions)
		view := h.appSummary(a, versions)
		if filex != "" {
			compatible := ""
			for _, v := range versions {
				if v.YankedAt != nil {
					continue
				}
				rng, err := filexrange.Parse(v.FilexRange)
				if err != nil || !rng.Admits(host) {
					continue
				}
				if compatible == "" || index.CompareVersions(v.Version, compatible) > 0 {
					compatible = v.Version
				}
			}
			if compatible == "" {
				continue
			}
			view.Compatible = compatible
		}
		var newest time.Time
		for _, v := range versions {
			if v.PublishedAt.After(newest) {
				newest = v.PublishedAt
			}
		}
		rows = append(rows, catalogRow{view: view, versions: versions, published: newest.UnixNano()})
	}
	switch sortBy {
	case "updated":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].view.UpdatedAt > rows[j].view.UpdatedAt })
	case "published":
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].published > rows[j].published })
	default:
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].view.Name < rows[j].view.Name })
	}
	out := appPageView{Items: []appSummaryView{}, Page: p, PerPage: pp}
	if byType {
		if rows, err = h.matchFileTypes(r.Context(), rows, exts); err != nil {
			h.fail(w, r, err)
			return
		}
		taken := map[string]bool{}
		for _, row := range rows {
			taken[row.view.Name] = true
		}
		nearFilter := filter
		nearFilter.Words = nil
		if out.FileTypes, err = h.fileTypeSearch(r.Context(), exts, nearFilter, taken); err != nil {
			h.fail(w, r, err)
			return
		}
		for _, row := range rows {
			if !row.anyOnly {
				out.FileTypes.Named++
			}
		}
	}
	out.Total = len(rows)
	for i := (p - 1) * pp; i < len(rows) && i < p*pp; i++ {
		out.Items = append(out.Items, rows[i].view)
	}
	writeJSON(w, http.StatusOK, out)
}

// loadApp finds an app by name with its publisher and icon type.
func (h *handler) loadApp(w http.ResponseWriter, r *http.Request, name string) (store.App, store.PublisherRef, string, bool) {
	if !appNameRe.MatchString(name) {
		writeError(w, http.StatusNotFound, "not_found", "No such app.")
		return store.App{}, store.PublisherRef{}, "", false
	}
	a, err := store.GetAppByName(r.Context(), h.Pool, name)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such app.")
		return a, store.PublisherRef{}, "", false
	}
	if err != nil {
		h.fail(w, r, err)
		return a, store.PublisherRef{}, "", false
	}
	pub, err := store.GetPublisher(r.Context(), h.Pool, a.PublisherID)
	if err != nil {
		h.fail(w, r, err)
		return a, store.PublisherRef{}, "", false
	}
	iconType := ""
	if a.IconSHA256 != "" {
		types, err := store.MediaTypes(r.Context(), h.Pool, []string{a.IconSHA256})
		if err != nil {
			h.fail(w, r, err)
			return a, store.PublisherRef{}, "", false
		}
		iconType = types[a.IconSHA256]
	}
	return a, store.PublisherRef{ID: pub.ID, Name: pub.Name, Verified: pub.Verified, Official: pub.Official,
		AvatarSHA256: pub.AvatarSHA256, AvatarType: pub.AvatarType}, iconType, true
}

func (h *handler) appDetailOf(w http.ResponseWriter, r *http.Request, name string) (appDetailView, bool) {
	a, pub, iconType, ok := h.loadApp(w, r, name)
	if !ok {
		return appDetailView{}, false
	}
	versions, err := store.VersionsOf(r.Context(), h.Pool, a.ID)
	if err != nil {
		h.fail(w, r, err)
		return appDetailView{}, false
	}
	shots, err := store.AppScreenshots(r.Context(), h.Pool, a.ID)
	if err != nil {
		h.fail(w, r, err)
		return appDetailView{}, false
	}
	return h.appDetail(a, pub, iconType, versions, shots), true
}

// getApp: listed and unlisted apps answer; a revoked one answers with
// `revoked` set.
func (h *handler) getApp(w http.ResponseWriter, r *http.Request) {
	if v, ok := h.appDetailOf(w, r, r.PathValue("name")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// getAppVersion: one version with filex's own words for its permissions.
func (h *handler) getAppVersion(w http.ResponseWriter, r *http.Request) {
	a, _, _, ok := h.loadApp(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	version := r.PathValue("version")
	if len(version) > 64 {
		writeError(w, http.StatusNotFound, "not_found", "No such version.")
		return
	}
	v, err := store.GetVersion(r.Context(), h.Pool, a.ID, version)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such version.")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeVersion(w, r, a, v)
}

// getPublisher: an active publisher (or one the index shows) and its listed
// apps. A pending publisher is not public.
func (h *handler) getPublisher(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !publisherIDRe.MatchString(id) {
		writeError(w, http.StatusNotFound, "not_found", "No such publisher.")
		return
	}
	pub, err := store.GetPublisher(r.Context(), h.Pool, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such publisher.")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if pub.Status != "active" {
		shown, err := store.PublisherInIndex(r.Context(), h.Pool, id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if !shown {
			writeError(w, http.StatusNotFound, "not_found", "No such publisher.")
			return
		}
	}
	apps, err := store.AppsOfPublisher(r.Context(), h.Pool, id, []string{"listed"})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := []appSummaryView{}
	for _, a := range apps {
		items = append(items, h.appSummary(a, briefsToVersions(a.Versions)))
	}
	view := pubView(pub)
	view.AvatarURL = h.avatarURL(pub)
	writeJSON(w, http.StatusOK, map[string]any{"publisher": view, "apps": items})
}

type categoryView struct {
	ID    string            `json:"id"`
	Label map[string]string `json:"label"`
}

func (h *handler) listCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := store.ListCategories(r.Context(), h.Pool)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := []categoryView{}
	for _, c := range cats {
		items = append(items, categoryView{ID: c.ID, Label: c.Label})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
