package httpapi

import (
	"context"
	"net/http"
	"sort"

	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// The catalog's file-type search (`GET /api/v1/apps?q=.docx`) and the kinds
// of file a version works with. Reading a search as file types, matching an
// extension against an app's rules and what filex does by itself all live in
// internal/filetypes; this file only asks the database and shapes answers.

// maxNear is how many other results (apps whose text names the extension)
// a file-type search adds.
const maxNear = 12

type builtinCapabilityView struct {
	Do    string `json:"do"`
	Needs string `json:"needs,omitempty"`
	Via   string `json:"via,omitempty"`
}

type builtinTypeView struct {
	Ext   string                  `json:"ext"`
	Label string                  `json:"label,omitempty"`
	Mime  string                  `json:"mime,omitempty"`
	Can   []builtinCapabilityView `json:"can"`
}

type engineView struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Setting string `json:"setting,omitempty"`
	InImage bool   `json:"in_image,omitempty"`
}

type fileTypeSearchView struct {
	// Extensions are the kinds searched for, normalized, in the search's
	// order.
	Extensions []string `json:"extensions"`
	// Named: how many of the answer's items name one of the kinds
	// themselves (the rest offer something on every file, and come after).
	Named int `json:"named"`
	// Filex is the filex version Builtin describes.
	Filex string `json:"filex"`
	// Builtin: what filex does by itself with each searched kind it handles
	// (a kind it does nothing with is absent).
	Builtin []builtinTypeView `json:"builtin"`
	// Engines describes every engine a Builtin capability needs.
	Engines []engineView `json:"engines"`
	// Near: other listed apps whose name, label or summary holds one of the
	// extensions, not among the items.
	Near []appSummaryView `json:"near"`
}

// versionDetailOf is a version's page, with the kinds of file it works with.
func (h *handler) versionDetailOf(ctx context.Context, app store.App, v store.Version) (versionDetailView, error) {
	sets, err := store.FileTypesOf(ctx, h.Pool, []int64{v.ID})
	if err != nil {
		return versionDetailView{}, err
	}
	return versionDetail(app, v, sets[v.ID]), nil
}

func (h *handler) writeVersion(w http.ResponseWriter, r *http.Request, app store.App, v store.Version) {
	out, err := h.versionDetailOf(r.Context(), app, v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// shownVersion is the version an app's card shows: with a filex version
// asked for, the newest one whose range admits it (`compatible`), else the
// newest that is not yanked (`latest`). 0 when there is none.
func shownVersion(view appSummaryView, versions []store.Version) int64 {
	want := view.Latest
	if view.Compatible != "" {
		want = view.Compatible
	}
	for _, v := range versions {
		if want != "" && v.Version == want && v.YankedAt == nil {
			return v.ID
		}
	}
	return 0
}

// knownExts is what a bare word must be to read as an extension: one filex
// knows, or one a listed app's newest version names (in its manifest, or as
// a kind its listing says it converts from).
func (h *handler) knownExts(ctx context.Context) (func(string) bool, error) {
	apps, err := store.CatalogApps(ctx, h.Pool, store.CatalogFilter{WithVersion: true})
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, a := range apps {
		versions := briefsToVersions(a.Versions)
		if id := shownVersion(appSummaryView{Latest: index.Latest(versions)}, versions); id != 0 {
			ids = append(ids, id)
		}
	}
	sets, err := store.FileTypesOf(ctx, h.Pool, ids)
	if err != nil {
		return nil, err
	}
	named := map[string]bool{}
	for _, s := range sets {
		for _, e := range s.Exts() {
			named[e] = true
		}
		for _, e := range s.ConvertsFrom() {
			named[e] = true
		}
	}
	b := filetypes.Builtin()
	return func(e string) bool { return named[e] || b.Known(e) }, nil
}

// fileTypeQuery reads the search as file types: ok=false is an ordinary
// text search.
func (h *handler) fileTypeQuery(ctx context.Context, text string) ([]string, bool, error) {
	if exts, ok := filetypes.ParseQuery(text, nil); ok {
		return exts, true, nil
	}
	if !filetypes.MightBeQuery(text) {
		return nil, false, nil
	}
	known, err := h.knownExts(ctx)
	if err != nil {
		return nil, false, err
	}
	exts, ok := filetypes.ParseQuery(text, known)
	return exts, ok, nil
}

// catalogRow is one app of the catalog's answer before paging.
type catalogRow struct {
	view      appSummaryView
	versions  []store.Version
	published int64 // unix seconds of the newest version (sort=published)
	// a file-type search: how many of the extensions it names itself, and
	// whether it is here only for what it offers on every file.
	named   int
	anyOnly bool
}

// matchFileTypes keeps the rows whose shown version works with at least one
// of exts, each with what it does with them; apps that name a kind come
// first (the more kinds the earlier), then the ones that only offer
// something on every file. The order within each group is kept.
func (h *handler) matchFileTypes(ctx context.Context, rows []catalogRow, exts []string) ([]catalogRow, error) {
	ids := []int64{}
	shown := make([]int64, len(rows))
	for i, r := range rows {
		if shown[i] = shownVersion(r.view, r.versions); shown[i] != 0 {
			ids = append(ids, shown[i])
		}
	}
	sets, err := store.FileTypesOf(ctx, h.Pool, ids)
	if err != nil {
		return nil, err
	}
	mimeOf := filetypes.Builtin().MimeOf
	out := []catalogRow{}
	for i, r := range rows {
		set, ok := sets[shown[i]]
		if shown[i] == 0 || !ok {
			continue
		}
		var uses []filetypes.Use
		var every filetypes.Use
		for _, e := range exts {
			named, anyFile := filetypes.Match(set, e, mimeOf)
			if len(named.Roles) > 0 {
				uses = append(uses, named)
				r.named++
			}
			if len(every.Roles) == 0 && len(anyFile.Roles) > 0 {
				// The same rules for every kind: said once, without an extension.
				every = filetypes.Use{AnyFile: true, Roles: anyFile.Roles}
			}
		}
		if len(every.Roles) > 0 {
			uses = append(uses, every)
		}
		if len(uses) == 0 {
			continue
		}
		r.anyOnly = r.named == 0
		r.view.MatchedExtensions = uses
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].anyOnly != out[j].anyOnly {
			return !out[i].anyOnly
		}
		return out[i].named > out[j].named
	})
	return out, nil
}

// fileTypeSearch is the answer's `file_types`: what filex does by itself
// with the kinds, and the other apps whose text names them.
func (h *handler) fileTypeSearch(ctx context.Context, exts []string, f store.CatalogFilter, taken map[string]bool) (*fileTypeSearchView, error) {
	b := filetypes.Builtin()
	out := &fileTypeSearchView{Extensions: exts, Filex: b.Filex, Builtin: []builtinTypeView{}, Engines: []engineView{}, Near: []appSummaryView{}}
	var types []filetypes.Type
	for _, e := range exts {
		t, ok := b.Lookup(e)
		if !ok {
			continue
		}
		types = append(types, t)
		v := builtinTypeView{Ext: e, Label: t.Label, Mime: b.MimeOf(e), Can: []builtinCapabilityView{}}
		for _, c := range t.Can {
			v.Can = append(v.Can, builtinCapabilityView{Do: c.Do, Needs: c.Needs, Via: c.Via})
		}
		out.Builtin = append(out.Builtin, v)
	}
	engines := b.EnginesOf(types)
	ids := make([]string, 0, len(engines))
	for id := range engines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		e := engines[id]
		out.Engines = append(out.Engines, engineView{ID: id, Label: e.Label, Kind: e.Kind, Setting: e.Setting, InImage: e.InImage})
	}
	for _, e := range exts {
		if len(out.Near) >= maxNear {
			break
		}
		nf := f
		nf.Words = []string{e}
		apps, err := store.CatalogApps(ctx, h.Pool, nf)
		if err != nil {
			return nil, err
		}
		for _, a := range apps {
			if taken[a.Name] || len(out.Near) >= maxNear {
				continue
			}
			taken[a.Name] = true
			out.Near = append(out.Near, h.appSummary(a, briefsToVersions(a.Versions)))
		}
	}
	return out, nil
}
