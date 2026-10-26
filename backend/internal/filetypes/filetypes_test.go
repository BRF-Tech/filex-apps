package filetypes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testdata/manifests holds the official apps' real manifests at the commits
// the demo approved (raw.githubusercontent.com/BRF-Tech/<repo>/<commit>/filex-app.json):
// filex-convert v0.2.0 a11dd275, filex-drawio v31.5.3 bf7a43c5, filextext-app
// v0.1.1 f4e4be90, filex-retro v0.1.0 c49e0772, filex-sign v0.3.0 ac3fa19e;
// filex-lang-es v0.1.8 fb9f34bf with its ui_locales cut to three strings.
func load(t *testing.T, name string) Set {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "manifests", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Extract(b)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if s.Extractor != Extractor {
		t.Fatalf("%s: extractor %d", name, s.Extractor)
	}
	return s
}

// brief is a rule in one line: role id [exts] (mimes) any engine output>ext.
func brief(r Rule) string {
	var b strings.Builder
	b.WriteString(string(r.Role))
	if r.ID != "" {
		b.WriteString(" " + r.ID)
	}
	if len(r.Exts) > 0 {
		b.WriteString(" [" + strings.Join(r.Exts, ",") + "]")
	}
	if len(r.Mimes) > 0 {
		b.WriteString(" (" + strings.Join(r.Mimes, ",") + ")")
	}
	if r.AnyFile {
		b.WriteString(" any-file")
	}
	if r.Engine != "" {
		b.WriteString(" engine=" + r.Engine)
	}
	if r.Output != "" {
		b.WriteString(" " + r.Output)
		if r.OutputExt != "" {
			b.WriteString(">" + r.OutputExt)
		}
	}
	return b.String()
}

func briefs(s Set) []string {
	out := []string{}
	for _, r := range s.Rules {
		out = append(out, brief(r))
	}
	return out
}

func TestTheOfficialAppsNameTheKindsTheirManifestsDeclare(t *testing.T) {
	cases := map[string][]string{
		// One action on every file: what it makes of a file is the module's
		// answer when it runs, not a list in the manifest.
		"convert":   {"action convert any-file sibling"},
		"drawio":    {"open editor [drawio,dio]", "create editor [drawio]"},
		"filextext": {"open editor [fxtxt]", "create editor [fxtxt]"},
		"retro":     {"open doom [wad,fxdoom]", "open dos [jsdos]"},
		// "apply" is hidden (a public page's second half), so it is not here.
		"sign": {"inspect status [pdf]", "action sign [pdf] sibling", "action request [pdf] none",
			"action fill [pdf] version", "action verify [pdf] none"},
		"lang-es": {},
	}
	for name, want := range cases {
		got := briefs(load(t, name))
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
	if got := load(t, "drawio").Exts(); !reflect.DeepEqual(got, []string{"dio", "drawio"}) {
		t.Errorf("drawio exts = %v", got)
	}
	if !load(t, "convert").AnyFile() || load(t, "sign").AnyFile() {
		t.Error("AnyFile: convert yes, sign no")
	}
	if l := load(t, "sign").Rules[1].Label["en"]; l != "Sign…" {
		t.Errorf("an action keeps its own label: %q", l)
	}
}

func TestExtractReadsTheKindsAsFilexNormalizesThem(t *testing.T) {
	raw := `{
	  "views": [
	    {"id": "v", "placement": "viewer", "label": {"en": "Viewer"}, "applies": {"ext": [" .MD ", "markdown", "Bad Ext!"], "mime": ["Text/Markdown"]}},
	    {"id": "folder", "placement": "inspector", "label": {"en": "Folder"}, "applies": {"kind": "dir"}},
	    {"id": "every", "placement": "inspector", "label": "Details"},
	    {"id": "wizard", "placement": "page", "label": {"en": "W"}, "applies": {"ext": ["pdf"]}}
	  ],
	  "thumbnails": {"applies": {"mime": ["image/*"]}},
	  "new_documents": [{"ext": ".Note", "label": {"en": "Note"}, "view": "v"}],
	  "actions": [
	    {"id": "to-pdf", "label": {"en": "To PDF"}, "applies": {"ext": ["pdf"], "engine_ext": {"office": ["docx", ".ODT"]}}, "output": {"mode": "sibling", "name": "{stem}.pdf"}},
	    {"id": "zip", "label": {"en": "Zip"}, "applies": {"kind": "dir"}},
	    {"id": "touch", "label": {"en": "Touch"}, "applies": {"kind": "any"}, "output": {"mode": "version"}},
	    {"id": "secret", "label": {"en": "S"}, "applies": {"ext": ["txt"]}, "hidden": true}
	  ]
	}`
	s, err := Extract([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"open v [md,markdown] (text/markdown)",
		"inspect every any-file",
		"create v [note]",
		"thumbnail (image/*)",
		"action to-pdf [pdf] sibling>pdf",
		"action to-pdf [docx,odt] engine=office sibling>pdf",
		"action touch any-file version",
	}
	if got := briefs(s); !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	if s.Rules[1].Label["en"] != "Details" {
		t.Errorf("a plain-string label reads as English: %v", s.Rules[1].Label)
	}
	if got := s.Mimes(); !reflect.DeepEqual(got, []string{"image/*", "text/markdown"}) {
		t.Errorf("mimes = %v", got)
	}
}

func TestExtractRefusesWhatIsNotJSON(t *testing.T) {
	if _, err := Extract([]byte(`{"views": [`)); err == nil {
		t.Fatal("malformed JSON extracted")
	}
	s, err := Extract([]byte(`{}`))
	if err != nil || len(s.Rules) != 0 || s.Rules == nil {
		t.Fatalf("an empty manifest: %+v %v (want an empty, non-nil list)", s, err)
	}
}

func TestOutputExtIsWhatTheNameFixes(t *testing.T) {
	for name, want := range map[string]string{
		"{stem}.pdf":         "pdf",
		"{stem}-small.PNG":   "png",
		"{stem}.tar.gz":      "gz",
		"{stem}-signed{ext}": "",
		"{name}":             "",
		"{stem}":             "",
		"{stem}.{ext}":       "",
		"out":                "",
	} {
		if got := outputExt(name); got != want {
			t.Errorf("outputExt(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestNormalizeReadsAnExtensionAsAPersonWritesIt(t *testing.T) {
	for in, want := range map[string][3]any{
		"docx":     {"docx", false, true},
		".docx":    {"docx", true, true},
		"DOCX":     {"docx", false, true},
		"*.docx":   {"docx", true, true},
		" .Tar.GZ": {"tar.gz", true, true},
		".":        {"", true, false},
		"*":        {"*", false, false},
		"*docx":    {"*docx", false, false},
		".a b":     {"a b", true, false},
		"..docx":   {".docx", true, false},
	} {
		e, explicit, ok := Normalize(in)
		if e != want[0] || explicit != want[1] || ok != want[2] {
			t.Errorf("Normalize(%q) = %q %v %v, want %v", in, e, explicit, ok, want)
		}
	}
}

func TestParseQueryKnowsAFileTypeSearch(t *testing.T) {
	known := func(e string) bool { return e == "docx" || e == "pdf" || e == "drawio" }
	type want struct {
		exts []string
		ok   bool
	}
	for q, w := range map[string]want{
		"docx":              {[]string{"docx"}, true},
		".DOCX":             {[]string{"docx"}, true},
		"*.docx":            {[]string{"docx"}, true},
		"pdf docx":          {[]string{"pdf", "docx"}, true},
		"pdf, .docx;PDF":    {[]string{"pdf", "docx"}, true},
		".xyz":              {[]string{"xyz"}, true}, // written as an extension: a search for that kind
		"xyz":               {nil, false},            // a bare unknown word stays a word
		"sign":              {nil, false},
		"sign pdf":          {nil, false},
		"":                  {nil, false},
		"   ":               {nil, false},
		".":                 {nil, false},
		"a b c d e f g h i": {nil, false},
	} {
		got, ok := ParseQuery(q, known)
		if ok != w.ok || !reflect.DeepEqual(got, w.exts) {
			t.Errorf("ParseQuery(%q) = %v %v, want %v %v", q, got, ok, w.exts, w.ok)
		}
	}
	if _, ok := ParseQuery("docx", nil); ok {
		t.Error("without a known list a bare word is not an extension")
	}
	if !MightBeQuery("pdf docx") || MightBeQuery("filex sign!") || MightBeQuery("") {
		t.Error("MightBeQuery")
	}
}

func roles(u Use) []string {
	out := []string{}
	for _, r := range u.Roles {
		s := string(r.Role) + " " + r.ID
		if r.Engine != "" {
			s += " engine=" + r.Engine
		}
		if r.ViaMime != "" {
			s += " via " + r.ViaMime
		}
		out = append(out, s)
	}
	return out
}

func TestMatchSaysWhatAnAppDoesWithAKind(t *testing.T) {
	sign := load(t, "sign")
	named, any := Match(sign, "pdf", nil)
	if got := roles(named); !reflect.DeepEqual(got, []string{"inspect status", "action sign", "action request", "action fill", "action verify"}) {
		t.Errorf("sign on pdf: %v", got)
	}
	if len(any.Roles) != 0 {
		t.Errorf("sign has nothing on every file: %v", roles(any))
	}
	if named, _ := Match(sign, "docx", nil); len(named.Roles) != 0 {
		t.Errorf("sign 0.3.0 names only pdf: %v", roles(named))
	}

	named, any = Match(load(t, "convert"), "docx", nil)
	if len(named.Roles) != 0 || !reflect.DeepEqual(roles(any), []string{"action convert"}) || !any.AnyFile || any.Ext != "docx" {
		t.Errorf("convert on docx: named %v, any-file %+v", roles(named), any)
	}

	if named, _ := Match(load(t, "drawio"), "dio", nil); !reflect.DeepEqual(roles(named), []string{"open editor"}) {
		t.Errorf("drawio on dio: %v", roles(named))
	}
	if named, _ := Match(load(t, "drawio"), "drawio", nil); !reflect.DeepEqual(roles(named), []string{"open editor", "create editor"}) {
		t.Errorf("drawio on drawio: %v", roles(named))
	}

	s, _ := Extract([]byte(`{"thumbnails": {"applies": {"mime": ["image/*"]}},
	  "actions": [{"id": "to-pdf", "label": {"en": "To PDF"}, "applies": {"ext": ["pdf"], "engine_ext": {"office": ["docx"]}}, "output": {"mode": "sibling", "name": "{stem}.pdf"}}]}`))
	mimeOf := func(e string) string {
		return map[string]string{"png": "image/png", "docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"}[e]
	}
	if named, _ := Match(s, "png", mimeOf); !reflect.DeepEqual(roles(named), []string{"thumbnail  via image/*"}) {
		t.Errorf("a media type family: %v", roles(named))
	}
	named, _ = Match(s, "docx", mimeOf)
	if !reflect.DeepEqual(roles(named), []string{"action to-pdf engine=office"}) || named.Roles[0].OutputExt != "pdf" {
		t.Errorf("an engine's extension: %+v", named)
	}
	if named, _ := Match(s, "pdf", mimeOf); !reflect.DeepEqual(roles(named), []string{"action to-pdf"}) {
		t.Errorf("the action's own kind: %v", roles(named))
	}
	if named, _ := Match(s, "gif", nil); len(named.Roles) != 0 {
		t.Errorf("without filex's media type a family matches nothing: %v", roles(named))
	}
}

func TestGroupListsAnAppsKindsInTheManifestsOrder(t *testing.T) {
	got := Group(load(t, "drawio"))
	b, _ := json.Marshal(got)
	want := `[{"ext":"drawio","roles":[{"role":"open","id":"editor","label":{"en":"draw.io","tr":"draw.io"}},{"role":"create","id":"editor","label":{"en":"draw.io diagram","tr":"draw.io diyagramı"}}]},` +
		`{"ext":"dio","roles":[{"role":"open","id":"editor","label":{"en":"draw.io","tr":"draw.io"}}]}]`
	if string(b) != want {
		t.Errorf("\n got %s\nwant %s", b, want)
	}
	conv := Group(load(t, "convert"))
	if len(conv) != 1 || !conv[0].AnyFile || conv[0].Roles[0].ID != "convert" {
		t.Errorf("convert: %+v", conv)
	}
	if g := Group(load(t, "lang-es")); g == nil || len(g) != 0 {
		t.Errorf("a language pack: %#v (want an empty, non-nil list)", g)
	}
}
