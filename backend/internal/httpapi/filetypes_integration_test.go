//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// typePage is the catalog's answer as a file-type search reads it.
type typePage struct {
	Total int `json:"total"`
	Items []struct {
		Name    string          `json:"name"`
		Latest  string          `json:"latest"`
		Matched []filetypes.Use `json:"matched_extensions"`
	} `json:"items"`
	FileTypes *struct {
		Extensions []string `json:"extensions"`
		Named      int      `json:"named"`
		Filex      string   `json:"filex"`
		Builtin    []struct {
			Ext string `json:"ext"`
			Can []struct {
				Do    string `json:"do"`
				Needs string `json:"needs"`
			} `json:"can"`
		} `json:"builtin"`
		Engines []struct{ ID, Label, Setting string } `json:"engines"`
		Near    []struct{ Name string }               `json:"near"`
	} `json:"file_types"`
}

// lines is each item in a line: name:role(id)+role(id), the roles on every
// file as every-<role>, kinds apart by a slash.
func (p typePage) lines() []string {
	out := []string{}
	for _, it := range p.Items {
		var parts []string
		for _, u := range it.Matched {
			var rs []string
			for _, r := range u.Roles {
				s := string(r.Role)
				if r.ID != "" {
					s += "(" + r.ID + ")"
				}
				if u.AnyFile {
					s = "every-" + s
				}
				rs = append(rs, s)
			}
			parts = append(parts, strings.Join(rs, "+"))
		}
		out = append(out, it.Name+":"+strings.Join(parts, "/"))
	}
	return out
}

func (c *client) types(t *testing.T, query string) typePage {
	t.Helper()
	var p typePage
	r := c.get("/api/v1/apps?" + query)
	if r.status != 200 {
		t.Fatalf("%s: %d %s", query, r.status, r.body)
	}
	r.json(t, &p)
	return p
}

// typeSearch is a file-type search's items in one line each.
func typeSearch(t *testing.T, c *client, q string) []string {
	t.Helper()
	p := c.types(t, "q="+url.QueryEscape(q))
	if p.FileTypes == nil {
		t.Fatalf("%q was not read as a file-type search", q)
	}
	return p.lines()
}

// manifestOf is an official app's real manifest (internal/filetypes/testdata).
func manifestOf(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "filetypes", "testdata", "manifests", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// seedTyped makes a listed app with these versions (version -> manifest),
// as the approvals before migration 0007 left them: no file types yet.
func (s *stack) seedTyped(t *testing.T, publisher, name, kind, summary string, versions map[string]string) store.App {
	t.Helper()
	ctx := context.Background()
	var owner int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM accounts WHERE display_name = 'seed'`).Scan(&owner); err != nil {
		if err := s.pool.QueryRow(ctx, `INSERT INTO accounts (display_name) VALUES ('seed') RETURNING id`).Scan(&owner); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status)
		VALUES ($1, $1, $1, (SELECT coalesce(max(github_owner_id), 500) + 1 FROM publishers), 'Organization', 'active')
		ON CONFLICT (id) DO NOTHING`, publisher); err != nil {
		t.Fatal(err)
	}
	var repoID int64
	_ = s.pool.QueryRow(ctx, `SELECT coalesce(max(repo_id), 7000) + 1 FROM apps`).Scan(&repoID)
	id, err := store.InsertApp(ctx, s.pool, store.App{Name: name, Kind: kind, PublisherID: publisher, RepoOwner: publisher,
		RepoName: "filex-" + name, RepoID: repoID, Label: map[string]string{"en": "App " + name}, Summary: map[string]string{"en": summary}})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(versions))
	for v := range versions {
		keys = append(keys, v)
	}
	sort.Strings(keys)
	for _, v := range keys {
		sub, err := store.InsertSubmission(ctx, s.pool, publisher, owner, publisher+"/filex-"+name, "v"+v, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE submissions SET status = 'approved', app_id = $2 WHERE id = $1`, sub, id); err != nil {
			t.Fatal(err)
		}
		if _, err := store.InsertVersion(ctx, s.pool, store.Version{AppID: id, SubmissionID: sub, Version: v, Ref: "v" + v,
			Commit: strings.Repeat("c", 40), FilexRange: ">=0.50.0", ManifestURL: "https://raw.githubusercontent.com/m.json",
			ManifestSHA256: sha([]byte("manifest " + name + v)), ManifestSig: "00", Manifest: json.RawMessage(versions[v]),
			ArtifactKeyID: s.keys.Artifact.ID}); err != nil {
			t.Fatal(err)
		}
	}
	app, err := store.GetAppByID(ctx, s.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

const docxTool = `{"views": [{"id": "edit", "placement": "viewer", "label": {"en": "Doc editor"}, "applies": {"ext": ["docx"]}}],
  "actions": [{"id": "to-pdf", "label": {"en": "To PDF…"}, "applies": {"ext": ["docx"]}, "output": {"mode": "sibling", "name": "{stem}.pdf"}}]}`

// TestFileTypeSearch: the versions approved before migration 0007 get their
// kinds from the start-up backfill, and a search made of extensions finds
// the apps by what their newest version that is not yanked works with.
func TestFileTypeSearch(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	s.seedTyped(t, "brf-tech", "sign", "app", "Sign PDFs.", map[string]string{"0.3.0": manifestOf(t, "sign")})
	s.seedTyped(t, "brf-tech", "drawio", "app", "Diagrams.", map[string]string{"31.5.3": manifestOf(t, "drawio")})
	s.seedTyped(t, "brf-tech", "convert", "app", "Convert anything.", map[string]string{"0.2.0": manifestOf(t, "convert")})
	s.seedTyped(t, "brf-tech", "retro", "app", "DOOM and DOS.", map[string]string{"0.1.0": manifestOf(t, "retro")})
	s.seedTyped(t, "brf-tech", "lang-es", "language_pack", "Español.", map[string]string{"0.1.8": manifestOf(t, "lang-es")})
	// docs: 0.9.0 opened .odt, 1.0.0 opens .docx, 2.0.0 (yanked) opens .xlsx.
	s.seedTyped(t, "acme", "docs", "app", "Word files.", map[string]string{
		"0.9.0": `{"views": [{"id": "v", "placement": "viewer", "label": {"en": "V"}, "applies": {"ext": ["odt"]}}]}`,
		"1.0.0": docxTool,
		"2.0.0": `{"views": [{"id": "v", "placement": "viewer", "label": {"en": "V"}, "applies": {"ext": ["xlsx"]}}]}`,
	})
	docs, _ := store.GetAppByName(ctx, s.pool, "docs")
	if _, err := s.pool.Exec(ctx, `UPDATE app_versions SET yanked_at = now(), yanked_reason = 'broken' WHERE app_id = $1 AND version = '2.0.0'`, docs.ID); err != nil {
		t.Fatal(err)
	}
	// notes: a summary that names .docx, a manifest that does not.
	s.seedTyped(t, "acme", "notes", "app", "Keeps notes next to your docx files.", map[string]string{"1.0.0": `{}`})

	anon := s.newClient()
	// Before the backfill nothing is known about the kinds: only filex's own.
	if got := typeSearch(t, anon, ".drawio"); len(got) != 0 {
		t.Fatalf("before the backfill: %v", got)
	}
	res, err := store.BackfillFileTypes(ctx, s.pool)
	if err != nil || res.Extracted != 9 || len(res.Failed) != 0 {
		t.Fatalf("backfill: %+v %v (want all 9 versions)", res, err)
	}
	if again, err := store.BackfillFileTypes(ctx, s.pool); err != nil || again.Extracted != 0 {
		t.Fatalf("a second backfill: %+v %v (want nothing to do)", again, err)
	}

	// docx however it is written: the app that names it, then the one that
	// offers its action on every file; filex's own support; the near result.
	want := []string{"docs:open(edit)+action(to-pdf)", "convert:every-action(convert)"}
	for _, q := range []string{"docx", ".docx", ".DOCX", "*.docx", "DOCX", " *.Docx "} {
		p := anon.types(t, "q="+url.QueryEscape(q))
		if p.FileTypes == nil || !reflect.DeepEqual(p.FileTypes.Extensions, []string{"docx"}) {
			t.Fatalf("%q: not a file-type search: %+v", q, p.FileTypes)
		}
		if got := p.lines(); !reflect.DeepEqual(got, want) || p.Total != 2 || p.FileTypes.Named != 1 {
			t.Fatalf("%q: %v (total %d, named %d), want %v (named 1)", q, got, p.Total, p.FileTypes.Named, want)
		}
		if len(p.FileTypes.Builtin) != 1 || p.FileTypes.Builtin[0].Ext != "docx" || p.FileTypes.Filex != filetypes.Builtin().Filex {
			t.Fatalf("%q: filex's own support for docx: %+v", q, p.FileTypes)
		}
		if len(p.FileTypes.Near) != 1 || p.FileTypes.Near[0].Name != "notes" {
			t.Fatalf("%q: near = %+v, want notes", q, p.FileTypes.Near)
		}
	}
	// What the action writes, and filex's engines said with the support.
	p := anon.types(t, "q=.docx")
	if r := p.Items[0].Matched[0].Roles[1]; r.OutputExt != "pdf" || r.Output != "sibling" || r.Label["en"] != "To PDF…" {
		t.Fatalf("the action's output: %+v", r)
	}
	needs := map[string]bool{}
	for _, c := range p.FileTypes.Builtin[0].Can {
		if c.Needs != "" {
			needs[c.Needs] = true
		}
	}
	for _, e := range p.FileTypes.Engines {
		if !needs[e.ID] || e.Label == "" {
			t.Fatalf("an engine nothing needs, or without a name: %+v", p.FileTypes.Engines)
		}
		delete(needs, e.ID)
	}
	if len(needs) != 0 {
		t.Fatalf("engines not described: %v", needs)
	}

	// Several kinds: every app that names one, the more the earlier.
	if got := typeSearch(t, anon, "pdf docx"); !reflect.DeepEqual(got, []string{
		"docs:open(edit)+action(to-pdf)",
		"sign:inspect(status)+action(sign)+action(request)+action(fill)+action(verify)",
		"convert:every-action(convert)",
	}) {
		t.Fatalf("pdf docx: %v", got)
	}
	if got := typeSearch(t, anon, ".drawio"); !reflect.DeepEqual(got, []string{"drawio:open(editor)+create(editor)", "convert:every-action(convert)"}) {
		t.Fatalf(".drawio: %v", got)
	}
	if got := typeSearch(t, anon, "dio"); !reflect.DeepEqual(got, []string{"drawio:open(editor)", "convert:every-action(convert)"}) {
		t.Fatalf("dio (bare, named by a listed app): %v", got)
	}
	if got := typeSearch(t, anon, ".wad"); !reflect.DeepEqual(got, []string{"retro:open(doom)", "convert:every-action(convert)"}) {
		t.Fatalf(".wad: %v", got)
	}

	// Only the newest version that is not yanked speaks for the app.
	if got := typeSearch(t, anon, ".xlsx"); !reflect.DeepEqual(got, []string{"convert:every-action(convert)"}) {
		t.Fatalf(".xlsx (named only by a yanked version): %v", got)
	}
	if got := typeSearch(t, anon, ".odt"); !reflect.DeepEqual(got, []string{"convert:every-action(convert)"}) {
		t.Fatalf(".odt (named only by an older version): %v", got)
	}

	// A kind nobody knows: no app names it, filex does nothing with it; the
	// app that offers something on every file is still said apart.
	p = anon.types(t, "q=.xyz")
	if p.FileTypes == nil || len(p.FileTypes.Builtin) != 0 || len(p.FileTypes.Near) != 0 || p.FileTypes.Named != 0 ||
		!reflect.DeepEqual(p.lines(), []string{"convert:every-action(convert)"}) {
		t.Fatalf(".xyz: %+v %v", p.FileTypes, p.lines())
	}
	// Written bare, an unknown word is a word: the ordinary search.
	if p := anon.types(t, "q=xyz"); p.FileTypes != nil || p.Total != 0 {
		t.Fatalf("xyz: %+v", p)
	}
	// Words stay words: the ordinary search, unchanged.
	if p := anon.types(t, "q=sign"); p.FileTypes != nil || !reflect.DeepEqual(p.lines(), []string{"sign:"}) {
		t.Fatalf("sign: %+v", p)
	}
	if p := anon.types(t, "q=word+files"); p.FileTypes != nil || !reflect.DeepEqual(p.lines(), []string{"docs:"}) {
		t.Fatalf("word files: %+v", p)
	}
	if p := anon.types(t, "q=sign+pdf"); p.FileTypes != nil || !reflect.DeepEqual(p.lines(), []string{"sign:"}) {
		t.Fatalf("a word and an extension stay a text search (sign's summary says PDFs): %+v", p)
	}
	if p := anon.types(t, ""); p.FileTypes != nil || p.Total != 7 {
		t.Fatalf("no search: %d apps, file_types %+v", p.Total, p.FileTypes)
	}
	// The other filters still hold.
	if p := anon.types(t, "q=.docx&kind=language_pack"); p.FileTypes == nil || p.Total != 0 {
		t.Fatalf("a language pack never works with .docx: %+v", p)
	}
	if p := anon.types(t, "q=.docx&publisher=acme"); !reflect.DeepEqual(p.lines(), []string{"docs:open(edit)+action(to-pdf)"}) {
		t.Fatalf(".docx of acme: %v", p.lines())
	}

	// An app's version page lists its kinds.
	var vd struct {
		FileTypes []filetypes.Use `json:"file_types"`
	}
	anon.get("/api/v1/apps/drawio/versions/31.5.3").json(t, &vd)
	if len(vd.FileTypes) != 2 || vd.FileTypes[0].Ext != "drawio" || len(vd.FileTypes[0].Roles) != 2 || vd.FileTypes[1].Ext != "dio" {
		t.Fatalf("drawio's version page: %+v", vd.FileTypes)
	}
	r := anon.get("/api/v1/apps/lang-es/versions/0.1.8")
	if !strings.Contains(string(r.body), `"file_types":[]`) {
		t.Fatalf("a language pack names no kind: %s", r.body)
	}
}

// seedConversions is filex-convert's seed listing's conversions.
func seedConversions(t *testing.T) []filetypes.Conversion {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "seed", "official", "filex-convert", "listing.json"))
	if err != nil {
		t.Fatal(err)
	}
	var l struct {
		Conversions []filetypes.Conversion `json:"conversions"`
	}
	if err := json.Unmarshal(b, &l); err != nil || len(l.Conversions) == 0 {
		t.Fatalf("seed conversions: %v", err)
	}
	return l.Conversions
}

// TestFileTypeSearchFindsDeclaredConversions: once filex-convert's listing
// declares what it converts, a search for one of those kinds names it (it
// counts in `named` and comes with the apps that name the kind), with what
// it converts the kind into; a kind it does not convert keeps it among the
// apps that only offer something on every file. Its version page carries
// the table.
func TestFileTypeSearchFindsDeclaredConversions(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	conv := s.seedTyped(t, "brf-tech", "convert", "app", "Convert anything.", map[string]string{"0.2.0": manifestOf(t, "convert")})
	s.seedTyped(t, "acme", "docs", "app", "Word files.", map[string]string{"1.0.0": docxTool})
	if _, err := store.BackfillFileTypes(ctx, s.pool); err != nil {
		t.Fatal(err)
	}
	anon := s.newClient()
	p := anon.types(t, "q=.docx")
	if got := p.lines(); !reflect.DeepEqual(got, []string{"docs:open(edit)+action(to-pdf)", "convert:every-action(convert)"}) || p.FileTypes.Named != 1 {
		t.Fatalf("before the declaration: %v (named %d)", got, p.FileTypes.Named)
	}
	if p := anon.types(t, "q=xcf"); p.FileTypes != nil {
		t.Fatalf("before the declaration a bare xcf is a word: %+v", p.FileTypes)
	}

	v, err := store.GetVersion(ctx, s.pool, conv.ID, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutConversions(ctx, s.pool, v.ID, seedConversions(t), store.ConversionsFromOperator, "seed"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"docx", ".docx", "*.DOCX"} {
		p = anon.types(t, "q="+url.QueryEscape(q))
		want := []string{"convert:convert+convert+convert+convert+convert/every-action(convert)", "docs:open(edit)+action(to-pdf)"}
		if got := p.lines(); !reflect.DeepEqual(got, want) || p.Total != 2 || p.FileTypes.Named != 2 {
			t.Fatalf("%q: %v (total %d, named %d), want %v (named 2)", q, got, p.Total, p.FileTypes.Named, want)
		}
	}
	u := p.Items[0].Matched[0]
	if u.Ext != "docx" || u.AnyFile {
		t.Fatalf("convert's docx use: %+v", u)
	}
	free, office := u.Roles[0], u.Roles[3]
	if !reflect.DeepEqual(free.To, []string{"pdf", "epub", "zip", "tar", "tar.gz", "tar.zst", "tar.xz", "txt", "md", "html"}) || free.NeedsAny != nil {
		t.Errorf("what needs nothing: %+v", free)
	}
	if !reflect.DeepEqual(office.To, []string{"odt", "rtf"}) || !reflect.DeepEqual(office.NeedsAny, [][]string{{"office"}}) {
		t.Errorf("what needs ONLYOFFICE: %+v", office)
	}
	if u := p.Items[0].Matched[1]; !u.AnyFile || u.Roles[0].ID != "convert" {
		t.Errorf("the action on every file stays apart: %+v", u)
	}

	// A kind only its conversions name is a file-type search, bare.
	if got := typeSearch(t, anon, "xcf"); len(got) != 1 || !strings.HasPrefix(got[0], "convert:convert") {
		t.Fatalf("xcf: %v", got)
	}
	// A kind it does not convert: only its action on every file, named 0.
	p = anon.types(t, "q=.xyz")
	if p.FileTypes.Named != 0 || !reflect.DeepEqual(p.lines(), []string{"convert:every-action(convert)"}) {
		t.Fatalf(".xyz: %v (named %d)", p.lines(), p.FileTypes.Named)
	}
	// Two kinds, one converted, one opened by docs: both named.
	if p := anon.types(t, "q=odt+docx"); p.FileTypes.Named != 2 || p.Total != 2 {
		t.Fatalf("odt docx: %v (named %d)", p.lines(), p.FileTypes.Named)
	}

	// The version page: the table, and the "Works with" list without it.
	var vd struct {
		FileTypes   []filetypes.Use           `json:"file_types"`
		Conversions []filetypes.ConversionRow `json:"conversions"`
	}
	anon.get("/api/v1/apps/convert/versions/0.2.0").json(t, &vd)
	if len(vd.FileTypes) != 1 || !vd.FileTypes[0].AnyFile || len(vd.Conversions) < 50 {
		t.Fatalf("convert's version page: %d kinds, %d conversion rows", len(vd.FileTypes), len(vd.Conversions))
	}
	if r := vd.Conversions[1]; !reflect.DeepEqual(r.From, []string{"jpg", "jpeg"}) || r.Roles[0].Role != filetypes.RoleConvert {
		t.Errorf("jpg's row: %+v", r)
	}
	r := anon.get("/api/v1/apps/docs/versions/1.0.0")
	if !strings.Contains(string(r.body), `"conversions":[]`) {
		t.Fatalf("an app that declares none: %s", r.body)
	}
}
