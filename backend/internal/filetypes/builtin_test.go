package filetypes

import (
	"strings"
	"testing"
)

// capsOf is a kind's capabilities in one line: "preview edit/office ...".
func capsOf(t *testing.T, ext string) string {
	t.Helper()
	ty, ok := Builtin().Lookup(ext)
	if !ok {
		return ""
	}
	var out []string
	for _, c := range ty.Can {
		s := c.Do
		if c.Needs != "" {
			s += "/" + c.Needs
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// The embedded data is filex 0.57.3 as scripts/gen-filex-builtin.mjs read
// it; these are the claims the catalog makes most, checked against what the
// code at that tag does (the script's sources name the lines).
func TestTheBuiltinDataSaysWhatFilexDoesByItself(t *testing.T) {
	b := Builtin()
	if b.Filex != "0.57.3" || b.Tag != "v0.57.3" || len(b.Commit) != 40 {
		t.Fatalf("data from %s %s %s", b.Filex, b.Tag, b.Commit)
	}
	for ext, want := range map[string]string{
		// Office documents only with a connected ONLYOFFICE: without it filex
		// offers them as a download, so nothing here is "out of the box".
		"docx":   "preview/office edit/office thumbnail/office create/office",
		"doc":    "preview/office edit/office thumbnail/office",
		"csv":    "preview edit/office create",
		"pdf":    "preview thumbnail/ghostscript",
		"png":    "preview thumbnail",
		"svg":    "preview thumbnail",
		"heic":   "thumbnail/imagemagick",
		"mp4":    "play thumbnail/ffmpeg",
		"avi":    "thumbnail/ffmpeg",
		"md":     "preview edit thumbnail create",
		"go":     "preview edit",
		"zip":    "browse thumbnail",
		"7z":     "browse/sevenzip",
		"rar":    "browse/sevenzip-rar",
		"drawio": "preview/drawio edit/drawio create/drawio",
		"glb":    "preview",

		// Since 0.54 the server's rule (editkind): .docm is an office
		// document and .properties text save-text saves, a .tsv's table only
		// shows it, and .xps is fixed-layout, not an office document. Since
		// filex 0.56 a Mermaid diagram's Edit opens its source in the text
		// editor, which save-text saves (before, its viewer had no save).
		"docm":       "preview/office edit/office thumbnail/office",
		"properties": "preview edit",
		"mmd":        "preview edit",
		"mermaid":    "preview edit",
		"tsv":        "preview",
		"xps":        "",

		// What only an app does: nothing of filex's own.
		"stl": "", "obj": "", "wad": "", "jsdos": "", "fxdoom": "", "fxtxt": "", "dio2": "", "xyz": "", "ico": "",
	} {
		if got := capsOf(t, ext); got != want {
			t.Errorf("%s: %q, want %q", ext, got, want)
		}
	}
	if b.MimeOf("png") != "image/png" || b.MimeOf("docx") != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || b.MimeOf("xyz") != "" {
		t.Error("MimeOf")
	}
	if !b.Known("docx") || !b.Known("ico") || b.Known("xyz") || b.Known("wad") {
		t.Error("Known: filex's kinds and media types, nothing else")
	}
	for _, id := range []string{"office", "drawio", "ffmpeg", "ghostscript", "imagemagick", "sevenzip", "sevenzip-rar"} {
		if e, ok := b.Engines[id]; !ok || e.Label == "" {
			t.Errorf("engine %s: %+v", id, e)
		}
	}
	if b.Engines["sevenzip-rar"].InImage || !b.Engines["ffmpeg"].InImage || b.Engines["office"].Kind != "service" {
		t.Errorf("engines: %+v", b.Engines)
	}
	if got := b.EnginesOf([]Type{b.Types["docx"], b.Types["pdf"]}); len(got) != 2 || got["office"].Label != "ONLYOFFICE" {
		t.Errorf("EnginesOf: %+v", got)
	}
}

func TestParseBuiltinRefusesWhatItCannotStandBehind(t *testing.T) {
	ok := `{"filex": "1.0.0", "commit": "c", "engines": {"office": {"label": "ONLYOFFICE", "kind": "service"}},
	  "types": {"docx": {"can": [{"do": "edit", "needs": "office"}]}}, "mimes": {"docx": "application/x-doc"}}`
	if _, err := ParseBuiltin([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"no commit":           `{"filex": "1.0.0", "types": {"a": {"can": [{"do": "preview"}]}}}`,
		"unknown capability":  `{"filex": "1", "commit": "c", "types": {"a": {"can": [{"do": "teleport"}]}}}`,
		"undescribed engine":  `{"filex": "1", "commit": "c", "types": {"a": {"can": [{"do": "edit", "needs": "office"}]}}}`,
		"does nothing":        `{"filex": "1", "commit": "c", "types": {"a": {"can": []}}}`,
		"not an extension":    `{"filex": "1", "commit": "c", "types": {".A": {"can": [{"do": "preview"}]}}}`,
		"bad media type":      `{"filex": "1", "commit": "c", "types": {"a": {"can": [{"do": "preview"}]}}, "mimes": {"a": "*/*"}}`,
		"engine without kind": `{"filex": "1", "commit": "c", "engines": {"x": {"label": "X"}}, "types": {"a": {"can": [{"do": "edit", "needs": "x"}]}}}`,
		"not JSON":            `{`,
	} {
		if _, err := ParseBuiltin([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
