// Package filetypes says which kinds of file an app works with and what it
// does with each, read from its manifest (filex-app.json) the way filex reads
// the same fields, and what filex itself does with a kind before any app is
// installed (filex-builtin.json, generated from a filex tag by
// scripts/gen-filex-builtin.mjs).
//
// It is the one place that normalizes an extension, reads a catalog search
// as a file-type search and matches an extension against an app's rules: the
// HTTP API answers with its results, and the web interface only draws them.
package filetypes

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Extractor is the version of what Extract writes. The start-up backfill
// (store.BackfillFileTypes) extracts again every stored set an older
// extractor made, so a change here reaches the versions approved before it.
const Extractor = 1

// MaxQueryExts is the most extensions one search names (the catalog's text
// search reads at most as many words).
const MaxQueryExts = 8

// Role is what an app does with a kind of file. Every role is a place in the
// manifest that names kinds of file.
type Role string

const (
	// RoleOpen: a `viewer` view - the app's interface opens the file in
	// place of filex's own preview.
	RoleOpen Role = "open"
	// RoleCreate: a `new_documents` row - filex's New menu makes an empty (or
	// templated) file of the kind, opened in the app.
	RoleCreate Role = "create"
	// RoleThumbnail: the `thumbnails` block - the app draws the file's
	// thumbnail.
	RoleThumbnail Role = "thumbnail"
	// RoleAction: an action of the file menu (not a hidden one, which no
	// menu offers).
	RoleAction Role = "action"
	// RoleInspect: an `inspector` view - a section of the details panel.
	RoleInspect Role = "inspect"
)

// Rule is one place in a manifest that names kinds of file, with the kinds
// as filex reads them (validateApplies: trimmed, one leading dot dropped,
// lower-case).
type Rule struct {
	Role Role `json:"role"`
	// ID is the view's or the action's id (a new document: its view).
	ID    string            `json:"id,omitempty"`
	Label map[string]string `json:"label,omitempty"`
	Exts  []string          `json:"exts,omitempty"`
	// Mimes are media types, exact or a family ("image/*").
	Mimes []string `json:"mimes,omitempty"`
	// AnyFile: the rule names no kind, so filex offers it on every file.
	AnyFile bool `json:"any_file,omitempty"`
	// Engine: offered on Exts only while filex has this engine
	// (applies.engine_ext; "office" is the ONLYOFFICE Document Server
	// connected to filex).
	Engine string `json:"engine,omitempty"`
	// Output (actions): where the result goes, sibling | version | none.
	Output string `json:"output,omitempty"`
	// OutputExt (actions writing a new file): the extension the manifest's
	// output name fixes ("{stem}.pdf" -> "pdf"); empty when the new file
	// keeps its source's kind or the name does not say.
	OutputExt string `json:"output_ext,omitempty"`
}

// Set is everything one version works with: the rules read from its
// manifest (Extract) and the conversions its listing declares (a record of
// their own, app_version_conversions, migration 0008; never read from the
// manifest, so an extractor change never touches them).
type Set struct {
	Extractor   int          `json:"extractor"`
	Rules       []Rule       `json:"rules"`
	Conversions []Conversion `json:"conversions,omitempty"`
}

// Exts is every extension the set's rules name, sorted, once each (the
// manifest's kinds; ConvertsFrom is the conversions').
func (s Set) Exts() []string {
	return distinct(s.Rules, func(r Rule) []string { return r.Exts })
}

// Mimes is every media type the set names, sorted, once each.
func (s Set) Mimes() []string {
	return distinct(s.Rules, func(r Rule) []string { return r.Mimes })
}

// AnyFile reports whether one of the rules is offered on every file.
func (s Set) AnyFile() bool {
	for _, r := range s.Rules {
		if r.AnyFile {
			return true
		}
	}
	return false
}

func distinct(rules []Rule, of func(Rule) []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range rules {
		for _, v := range of(r) {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out
}

// extRe is filex's grammar of an extension (wasmplugin viewerExtRe, the
// `ui-viewer:` and `thumbnail:` kinds): up to three dotted parts.
var extRe = regexp.MustCompile(ExtPattern)

// ExtPattern is filex's grammar of an extension, the same pattern
// schema/filex-apps.schema.json gives a conversion's extensions
// (internal/consent).
const ExtPattern = `^[a-z0-9][a-z0-9_+-]{0,31}(\.[a-z0-9][a-z0-9_+-]{0,31}){0,2}$`

// mimeRe is filex's grammar of a media type (viewerMimeRe): a family like
// image/* is one, */* is not.
var mimeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,63}/([a-z0-9][a-z0-9.+-]{0,127}|\*)$`)

// ExtOK reports whether e is an extension in filex's grammar (lower-case, no
// leading dot).
func ExtOK(e string) bool { return extRe.MatchString(e) }

// manifestExt is one entry of applies.ext the way filex stores it.
func manifestExt(s string) (string, bool) {
	e := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
	return e, ExtOK(e)
}

// Normalize reads one extension as a person writes it in a search: "docx",
// ".docx", "DOCX" or "*.docx" are all "docx". explicit reports a leading dot
// (or "*."), which makes it an extension whatever it is.
func Normalize(s string) (ext string, explicit, ok bool) {
	t := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.HasPrefix(t, "*."):
		t, explicit = t[2:], true
	case strings.HasPrefix(t, "."):
		t, explicit = t[1:], true
	}
	return t, explicit, ExtOK(t)
}

// queryFields splits a search into words: spaces, commas and semicolons.
func queryFields(q string) []string {
	return strings.FieldsFunc(q, func(r rune) bool { return unicode.IsSpace(r) || r == ',' || r == ';' })
}

// ParseQuery reads a catalog search as a file-type search. It is one when
// every word is an extension: written with a leading dot or "*." (".xyz" is
// a search for that kind even if nothing knows it), or a bare word that is a
// known extension (`known`: filex's own kinds and the ones listed apps name),
// so "docx" or "pdf docx" are file-type searches and "sign" stays a word.
// The extensions come back lower-case, without the dot, once each, in the
// search's order.
func ParseQuery(q string, known func(string) bool) ([]string, bool) {
	words := queryFields(q)
	if len(words) == 0 || len(words) > MaxQueryExts {
		return nil, false
	}
	out := []string{}
	seen := map[string]bool{}
	for _, w := range words {
		e, explicit, ok := Normalize(w)
		if !ok || (!explicit && (known == nil || !known(e))) {
			return nil, false
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, true
}

// MightBeQuery reports whether every word of q has an extension's shape, so
// whether it is worth asking which extensions are known (ParseQuery).
func MightBeQuery(q string) bool {
	words := queryFields(q)
	if len(words) == 0 || len(words) > MaxQueryExts {
		return false
	}
	for _, w := range words {
		if _, _, ok := Normalize(w); !ok {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- extract

// text is a manifest Text ({"en": "..."}); a plain string is read as English.
type text map[string]string

func (t *text) UnmarshalJSON(b []byte) error {
	var m map[string]string
	if err := json.Unmarshal(b, &m); err == nil {
		*t = m
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil && s != "" {
		*t = text{"en": s}
		return nil
	}
	*t = nil
	return nil
}

type applies struct {
	Kind      string              `json:"kind"`
	Ext       []string            `json:"ext"`
	Mime      []string            `json:"mime"`
	EngineExt map[string][]string `json:"engine_ext"`
}

// manifest is the part of filex-app.json that names kinds of file
// (filex's wire.Manifest), read leniently: the manifest was checked by the
// validators before the version was approved.
type manifest struct {
	Actions []struct {
		ID      string  `json:"id"`
		Label   text    `json:"label"`
		Applies applies `json:"applies"`
		Hidden  bool    `json:"hidden"`
		Output  struct {
			Mode string `json:"mode"`
			Name string `json:"name"`
		} `json:"output"`
	} `json:"actions"`
	Views []struct {
		ID        string  `json:"id"`
		Placement string  `json:"placement"`
		Label     text    `json:"label"`
		Applies   applies `json:"applies"`
	} `json:"views"`
	NewDocuments []struct {
		Ext   string `json:"ext"`
		Label text   `json:"label"`
		View  string `json:"view"`
	} `json:"new_documents"`
	Thumbnails *struct {
		Applies applies `json:"applies"`
	} `json:"thumbnails"`
}

// kinds is an applies rule's extensions and media types, normalized as filex
// normalizes them; what is not in filex's grammar is dropped (no search can
// name it).
func kinds(a applies) (exts, mimes []string) {
	for _, e := range a.Ext {
		if v, ok := manifestExt(e); ok && !contains(exts, v) {
			exts = append(exts, v)
		}
	}
	for _, m := range a.Mime {
		v := strings.ToLower(strings.TrimSpace(m))
		if mimeRe.MatchString(v) && !contains(mimes, v) {
			mimes = append(mimes, v)
		}
	}
	return exts, mimes
}

// onFiles reports whether a rule of this kind is offered on files (kind
// file, the default, or any); a folder's action names no file type.
func onFiles(kind string) bool { return kind == "" || kind == "file" || kind == "any" }

// Extract reads what a manifest works with. A manifest that names no kind of
// file (a language pack) gives an empty set; malformed JSON is an error.
func Extract(raw []byte) (Set, error) {
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Set{}, err
	}
	s := Set{Extractor: Extractor, Rules: []Rule{}}
	for _, v := range m.Views {
		var role Role
		switch v.Placement {
		case "viewer":
			role = RoleOpen
		case "inspector":
			role = RoleInspect
		default:
			// modal, page and home views are opened by an action or from
			// Apps; their own rule is not where filex offers them on a file.
			continue
		}
		if !onFiles(v.Applies.Kind) {
			continue
		}
		exts, mimes := kinds(v.Applies)
		r := Rule{Role: role, ID: v.ID, Label: v.Label, Exts: exts, Mimes: mimes}
		if len(exts) == 0 && len(mimes) == 0 {
			if role == RoleOpen {
				continue // filex refuses a viewer with no rule
			}
			r.AnyFile = true
		}
		s.Rules = append(s.Rules, r)
	}
	for _, d := range m.NewDocuments {
		if e, ok := manifestExt(d.Ext); ok {
			s.Rules = append(s.Rules, Rule{Role: RoleCreate, ID: d.View, Label: d.Label, Exts: []string{e}})
		}
	}
	if m.Thumbnails != nil && onFiles(m.Thumbnails.Applies.Kind) {
		if exts, mimes := kinds(m.Thumbnails.Applies); len(exts)+len(mimes) > 0 {
			s.Rules = append(s.Rules, Rule{Role: RoleThumbnail, Exts: exts, Mimes: mimes})
		}
	}
	for _, a := range m.Actions {
		if a.Hidden || !onFiles(a.Applies.Kind) {
			continue
		}
		mode := a.Output.Mode
		if mode == "" {
			mode = "none"
		}
		out := ""
		if mode == "sibling" {
			name := a.Output.Name
			if name == "" {
				name = "{stem}-" + a.ID + "{ext}" // filex's default (manifest.go)
			}
			out = outputExt(name)
		}
		exts, mimes := kinds(a.Applies)
		base := Rule{Role: RoleAction, ID: a.ID, Label: a.Label, Exts: exts, Mimes: mimes, Output: mode, OutputExt: out}
		if len(exts) == 0 && len(mimes) == 0 {
			// No kind: every file. (engine_ext only adds to a named kind.)
			base.AnyFile = true
			s.Rules = append(s.Rules, base)
			continue
		}
		s.Rules = append(s.Rules, base)
		engines := make([]string, 0, len(a.Applies.EngineExt))
		for name := range a.Applies.EngineExt {
			engines = append(engines, name)
		}
		sort.Strings(engines)
		for _, name := range engines {
			extra, _ := kinds(applies{Ext: a.Applies.EngineExt[name]})
			if len(extra) == 0 {
				continue
			}
			r := base
			r.Exts, r.Mimes, r.Engine = extra, nil, strings.ToLower(strings.TrimSpace(name))
			s.Rules = append(s.Rules, r)
		}
	}
	return s, nil
}

// outputExt is the extension an action's output name gives its new file:
// "{stem}.pdf" -> "pdf". A name ending in {ext} or {name} keeps the source's
// kind (""), and so does a name whose last part is not a literal extension.
func outputExt(name string) string {
	if strings.HasSuffix(name, "{ext}") || strings.HasSuffix(name, "{name}") {
		return ""
	}
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	e := strings.ToLower(name[i+1:])
	if strings.ContainsAny(e, "{}") || !ExtOK(e) {
		return ""
	}
	return e
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- match

// Use is what an app does with one kind of file: an extension, a media type
// its manifest names, or every file.
type Use struct {
	Ext     string    `json:"ext,omitempty"`
	Mime    string    `json:"mime,omitempty"`
	AnyFile bool      `json:"any_file,omitempty"`
	Roles   []RoleUse `json:"roles"`
}

// RoleUse is one thing an app does with a kind of file.
type RoleUse struct {
	Role      Role              `json:"role"`
	ID        string            `json:"id,omitempty"`
	Label     map[string]string `json:"label,omitempty"`
	Engine    string            `json:"engine,omitempty"`
	Output    string            `json:"output,omitempty"`
	OutputExt string            `json:"output_ext,omitempty"`
	// ViaMime: the manifest names a media type, and filex gives this
	// extension that type (filex-builtin.json).
	ViaMime string `json:"via_mime,omitempty"`
	// To (RoleConvert): the extensions the kind converts into.
	To []string `json:"to,omitempty"`
	// NeedsAny (RoleConvert): the conversion works while the filex has every
	// engine of any one of these sets; absent, it needs nothing else.
	NeedsAny [][]string `json:"needs_any,omitempty"`
}

func roleOf(r Rule) RoleUse {
	return RoleUse{Role: r.Role, ID: r.ID, Label: r.Label, Engine: r.Engine, Output: r.Output, OutputExt: r.OutputExt}
}

func addRole(roles []RoleUse, u RoleUse) []RoleUse {
	for _, x := range roles {
		if x.Role == u.Role && x.ID == u.ID && x.Engine == u.Engine {
			return roles
		}
	}
	return append(roles, u)
}

// mimeMatches is filex's rule (wasmplugin.Matches): the same type, or a
// family "image/*" holding it.
func mimeMatches(rule, mime string) bool {
	if rule == mime {
		return true
	}
	return strings.HasSuffix(rule, "/*") && strings.HasPrefix(mime, strings.TrimSuffix(rule, "*"))
}

// Match is what the set does with files of extension ext (normalized): the
// rules that name it (or a media type filex gives it, `mimeOf`) and the
// conversions that start from it (RoleConvert, after the rules), then,
// apart, the rules offered on every file. Either Use may have no roles; a
// role in `named` means the app names the kind itself.
func Match(s Set, ext string, mimeOf func(string) string) (named, anyFile Use) {
	named = Use{Ext: ext, Roles: []RoleUse{}}
	anyFile = Use{Ext: ext, AnyFile: true, Roles: []RoleUse{}}
	mime := ""
	if mimeOf != nil {
		mime = mimeOf(ext)
	}
	for _, r := range s.Rules {
		switch {
		case contains(r.Exts, ext):
			named.Roles = addRole(named.Roles, roleOf(r))
		case mime != "" && matchesAny(r.Mimes, mime) != "":
			u := roleOf(r)
			u.ViaMime = matchesAny(r.Mimes, mime)
			named.Roles = addRole(named.Roles, u)
		case r.AnyFile:
			anyFile.Roles = addRole(anyFile.Roles, roleOf(r))
		}
	}
	named.Roles = append(named.Roles, convertUses(s.Conversions, ext)...)
	return named, anyFile
}

func matchesAny(rules []string, mime string) string {
	for _, m := range rules {
		if mimeMatches(m, mime) {
			return m
		}
	}
	return ""
}

// Group is a set's uses kind by kind, for an app's page: each extension in
// the order the manifest first names it, then each media type, then what is
// offered on every file. The conversions are not here: an app's page shows
// them as a table of their own (ConversionTable).
func Group(s Set) []Use {
	out := []Use{}
	idx := map[string]int{}
	add := func(key string, u Use, r Rule) {
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			u.Roles = []RoleUse{}
			out = append(out, u)
		}
		out[i].Roles = addRole(out[i].Roles, roleOf(r))
	}
	for _, r := range s.Rules {
		for _, e := range r.Exts {
			add("ext:"+e, Use{Ext: e}, r)
		}
	}
	for _, r := range s.Rules {
		for _, m := range r.Mimes {
			add("mime:"+m, Use{Mime: m}, r)
		}
	}
	for _, r := range s.Rules {
		if r.AnyFile {
			add("any", Use{AnyFile: true}, r)
		}
	}
	return out
}
