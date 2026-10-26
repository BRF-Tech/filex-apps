package filetypes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// convertListing is filex-convert's seed listing's conversions (written by
// scripts/gen-convert-conversions.sh from filex-convert v0.2.0's planner).
func convertListing(t *testing.T) []Conversion {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "seed", "official", "filex-convert", "listing.json"))
	if err != nil {
		t.Fatal(err)
	}
	var l struct {
		Conversions []Conversion `json:"conversions"`
	}
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Conversions) == 0 {
		t.Fatal("the seed listing declares no conversions")
	}
	return l.Conversions
}

// convertSet is filex-convert 0.2.0 as the store holds it once its
// conversions are declared.
func convertSet(t *testing.T) Set {
	s := load(t, "convert")
	s.Conversions = convertListing(t)
	return s
}

// short is a convert role in one line: "-> a,b" or "-> a,b needs x|y+z".
func short(r RoleUse) string {
	s := string(r.Role)
	if r.ID != "" {
		s += " " + r.ID
	}
	if len(r.To) > 0 {
		s += " -> " + strings.Join(r.To, ",")
	}
	if len(r.NeedsAny) > 0 {
		s += " needs " + needsKey(r.NeedsAny)
	}
	return s
}

func shorts(rs []RoleUse) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, short(r))
	}
	return out
}

// A search for .docx finds filex-convert by what its listing says it
// converts .docx into, apart from its "Convert…" action on every file; the
// conversions that need nothing come first, then each condition with its
// targets, a target an engine also makes listed only where nothing is
// needed.
func TestMatchFindsWhatAListingConvertsAKindInto(t *testing.T) {
	named, every := Match(convertSet(t), "docx", nil)
	want := []string{
		"convert -> pdf,epub,zip,tar,tar.gz,tar.zst,tar.xz,txt,md,html",
		"convert -> png,jpg,gif,bmp,tiff,webp,ico,svg,qoi,ppm,tga needs ghostscript|imagemagick|poppler",
		"convert -> avif,psd,dds,exr needs imagemagick",
		"convert -> odt,rtf needs office",
		"convert -> ps,eps needs ghostscript|poppler",
	}
	if got := shorts(named.Roles); !reflect.DeepEqual(got, want) {
		t.Errorf("convert on docx:\n got %q\nwant %q", got, want)
	}
	if named.Ext != "docx" || named.AnyFile {
		t.Errorf("the named use: %+v", named)
	}
	if got := shorts(every.Roles); !reflect.DeepEqual(got, []string{"action convert"}) {
		t.Errorf("the action on every file stays apart: %v", got)
	}
	// .doc needs ONLYOFFICE for everything but packing it into an archive.
	named, _ = Match(convertSet(t), "doc", nil)
	if got := shorts(named.Roles); len(got) == 0 || got[0] != "convert -> zip,tar,tar.gz,tar.zst,tar.xz" ||
		!contains(got, "convert -> pdf,docx,odt,rtf,epub,txt,md,html needs office") {
		t.Errorf("convert on doc: %q", got)
	}
	// A kind the listing does not name stays a kind the app only offers its
	// every-file action on.
	if named, _ := Match(convertSet(t), "xyz", nil); len(named.Roles) != 0 {
		t.Errorf("convert on xyz: %v", shorts(named.Roles))
	}
	// Without a declaration, filex-convert names no kind (the 0.51-era store).
	if named, _ := Match(load(t, "convert"), "docx", nil); len(named.Roles) != 0 {
		t.Errorf("no declaration: %v", shorts(named.Roles))
	}
}

func TestConvertUsesSaysEachConditionOnce(t *testing.T) {
	convs := []Conversion{
		{From: []string{"a"}, To: []string{"b", "c"}, Needs: []string{"x"}},
		{From: []string{"a", "z"}, To: []string{"b", "d"}},
		{From: []string{"a"}, To: []string{"c", "e"}, Needs: []string{"y"}},
		{From: []string{"a"}, To: []string{"c", "e"}, Needs: []string{"y", "x"}},
		{From: []string{"a"}, To: []string{"f"}, Needs: []string{"x", "y"}},
		{From: []string{"q"}, To: []string{"a"}},
	}
	got := shorts(convertUses(convs, "a"))
	want := []string{
		// b: nothing needed (x is not needed after all); d likewise.
		"convert -> b,d",
		// c: x or y; e: y (x+y holds y, so it is not a condition of its own).
		"convert -> c needs x|y",
		"convert -> e needs y",
		"convert -> f needs x+y",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	// What needs nothing leads even when the listing names a conditional
	// target first.
	later := []Conversion{{From: []string{"k"}, To: []string{"m"}, Needs: []string{"x"}}, {From: []string{"k"}, To: []string{"n"}}}
	if got := shorts(convertUses(later, "k")); !reflect.DeepEqual(got, []string{"convert -> n", "convert -> m needs x"}) {
		t.Errorf("k: %q", got)
	}
	if got := convertUses(convs, "b"); len(got) != 0 {
		t.Errorf("b is only a target: %v", shorts(got))
	}
	if got := shorts(convertUses(convs, "z")); !reflect.DeepEqual(got, []string{"convert -> b,d"}) {
		t.Errorf("z: %v", got)
	}
	if got := (Set{Conversions: convs}).ConvertsFrom(); !reflect.DeepEqual(got, []string{"a", "q", "z"}) {
		t.Errorf("converts from %v", got)
	}
}

// An app page's table: one row per kind, the kinds that convert alike (an
// alias like jpeg) on one row, and the same roles a search for the kind
// gives.
func TestConversionTableIsTheSearchKindByKind(t *testing.T) {
	s := convertSet(t)
	rows := ConversionTable(s)
	if len(rows) < 50 {
		t.Fatalf("%d rows", len(rows))
	}
	seen := map[string]bool{}
	for _, r := range rows {
		for _, e := range r.From {
			if seen[e] {
				t.Errorf("%s is on two rows", e)
			}
			seen[e] = true
			named, _ := Match(s, e, nil)
			if !reflect.DeepEqual(named.Roles, r.Roles) {
				t.Errorf("%s: the table and the search disagree", e)
			}
		}
	}
	for _, e := range s.ConvertsFrom() {
		if !seen[e] {
			t.Errorf("%s converts but has no row", e)
		}
	}
	var jpg ConversionRow
	for _, r := range rows {
		if contains(r.From, "jpg") {
			jpg = r
		}
	}
	if !reflect.DeepEqual(jpg.From, []string{"jpg", "jpeg"}) {
		t.Errorf("jpg's row: %v", jpg.From)
	}
	if got := ConversionTable(load(t, "drawio")); got == nil || len(got) != 0 {
		t.Errorf("no declaration: %#v (want an empty, non-nil list)", got)
	}
	// The "Works with" list keeps the manifest's kinds only.
	if g := Group(s); len(g) != 1 || !g[0].AnyFile {
		t.Errorf("Group lists conversions: %+v", g)
	}
}

// Every extension the seed listing names is in filex's grammar, every
// engine it needs is one the manifest asks for, and no entry converts a kind
// into itself: what the store's consent check holds an import to.
func TestTheConvertSeedFitsItsManifest(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "manifests", "convert.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Permissions []string `json:"permissions"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	may := map[string]bool{}
	for _, p := range m.Permissions {
		if strings.HasPrefix(p, "engines:") {
			may[strings.TrimPrefix(p, "engines:")] = true
		}
	}
	for i, c := range convertListing(t) {
		for _, e := range append(append([]string{}, c.From...), c.To...) {
			if !ExtOK(e) {
				t.Errorf("entry %d: %q is not an extension", i, e)
			}
		}
		for _, e := range c.Needs {
			if !may[e] {
				t.Errorf("entry %d needs %q, the manifest has no engines:%s", i, e, e)
			}
		}
		for _, f := range c.From {
			if contains(c.To, f) {
				t.Errorf("entry %d converts %s into itself", i, f)
			}
		}
	}
}

// The web interface's development mock (web/mock/convert-conversions.json)
// draws filex-convert's conversion table as this code answers it; when the
// seed listing or the grouping changes, the mock is captured again.
func TestTheMockConversionsAreTheServers(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "mock", "convert-conversions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mock []ConversionRow
	if err := json.Unmarshal(b, &mock); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(ConversionTable(convertSet(t)))
	got, _ := json.Marshal(mock)
	if string(got) != string(want) {
		t.Errorf("web/mock/convert-conversions.json is not what the server answers for filex-convert's seed listing; capture it again")
	}
}
