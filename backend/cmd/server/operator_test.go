package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/media"
	"github.com/brf-tech/filex-apps/backend/internal/resolve"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A listing is a .filex-apps.json-shaped file; its paths are files next to
// it, read once each.
func TestLoadListing_ReadsTheListingAndItsFiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"listing.json":   `{"publisher":"brf-tech","categories":["documents"],"summary":{"en":"Sign PDFs."},"description":"description.md","icon":"icon.png","screenshots":[{"path":"shots/a.png","caption":{"en":"A"}},{"path":"shots/a.png"}]}`,
		"description.md": "# Sign\n",
		"icon.png":       "icon-bytes",
		"shots/a.png":    "shot-bytes",
	})
	op, err := loadListing(filepath.Join(dir, "listing.json"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if op.Listing.Publisher != "brf-tech" || op.Description != "# Sign\n" || len(op.Listing.Screenshots) != 2 {
		t.Errorf("listing %+v", op)
	}
	if len(op.Files) != 2 || !bytes.Equal(op.Files["icon.png"], []byte("icon-bytes")) || !bytes.Equal(op.Files["shots/a.png"], []byte("shot-bytes")) {
		t.Errorf("files %v", op.Files)
	}
	if !strings.Contains(op.Source, "listing.json sha256:") || len(op.Source) < 64 {
		t.Errorf("source %q", op.Source)
	}
}

// The listing is held to the schema (the consent parser), its paths stay
// inside its directory, and sizes are capped.
func TestLoadListing_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		max   int64
		want  string
	}{
		"unknown field":      {map[string]string{"listing.json": `{"publisher":"brf-tech","extra":1}`}, 1 << 20, "unknown field"},
		"no publisher":       {map[string]string{"listing.json": `{"summary":{"en":"x"}}`}, 1 << 20, "publisher: required"},
		"escapes the folder": {map[string]string{"listing.json": `{"publisher":"brf-tech","icon":"../icon.png"}`}, 1 << 20, "not a path"},
		"absolute path":      {map[string]string{"listing.json": `{"publisher":"brf-tech","icon":"/etc/passwd"}`}, 1 << 20, "not a path"},
		"missing image":      {map[string]string{"listing.json": `{"publisher":"brf-tech","icon":"icon.png"}`}, 1 << 20, "image"},
		"image over the cap": {map[string]string{"listing.json": `{"publisher":"brf-tech","icon":"icon.png"}`, "icon.png": strings.Repeat("x", 100)}, 10, "at most 10"},
		"empty description":  {map[string]string{"listing.json": `{"publisher":"brf-tech","description":"d.md"}`, "d.md": "  \n"}, 1 << 20, "empty"},
		"not json":           {map[string]string{"listing.json": `publisher: brf-tech`}, 1 << 20, "not a JSON object"},
	} {
		dir := t.TempDir()
		writeFiles(t, dir, tc.files)
		_, err := loadListing(filepath.Join(dir, "listing.json"), tc.max)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

// Missing flags are refused before anything is opened (exit 2).
func TestOperator_UsageErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	for _, args := range [][]string{
		{"publisher", "-id", "brf-tech"},
		{"import", "-publisher", "brf-tech", "-repo", "BRF-Tech/filex-sign"},
		{"import", "extra-argument"},
		{"conversions", "-app", "convert", "-version", "0.2.0"},
		{"conversions", "-app", "convert", "-listing", "x.json"},
		{"publish-index", "extra-argument"},
		{"publish-index", "-serial", "40"},
		{"notify-webhook", "-login", "alice", "-name", "Notify"},
		{"notify-test"},
	} {
		errOut.Reset()
		if code := operator(args[0], args[1:], &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage:") {
			t.Errorf("%v: exit %d, %q", args, code, errOut.String())
		}
	}
	if code := operator("nonsense", nil, &out, &errOut); code != 2 {
		t.Errorf("unknown command: exit %d", code)
	}
}

// Every operator command is routed to operator(); anything else starts the
// server (a missing name would open a second store in the container).
func TestIsOperatorCommand(t *testing.T) {
	for _, cmd := range []string{"publisher", "import", "conversions", "publish-index", "notify-webhook", "notify-test", "keygen"} {
		if !isOperatorCommand(cmd) {
			t.Errorf("%s is not routed to the operator commands", cmd)
		}
		var out, errOut bytes.Buffer
		if code := operator(cmd, []string{"extra-argument"}, &out, &errOut); code != 2 || strings.Contains(errOut.String(), "unknown command") {
			t.Errorf("%s: operator() does not know it: exit %d, %q", cmd, code, errOut.String())
		}
	}
	for _, cmd := range []string{"", "help", "serve", "healthcheck", "publish"} {
		if isOperatorCommand(cmd) {
			t.Errorf("%q is routed to the operator commands", cmd)
		}
	}
}

// The conversions command reads the listing file alone (the files it names
// are not needed), held to the same schema.
func TestLoadConversions_ReadsTheListingAlone(t *testing.T) {
	p := filepath.Join("..", "..", "..", "seed", "official", "filex-convert", "listing.json")
	l, err := loadConversions(p)
	if err != nil {
		t.Fatal(err)
	}
	if l.Listing.Publisher != "brf-tech" || len(l.Listing.Conversions) == 0 || len(l.Files) != 0 || !strings.Contains(l.Source, "listing.json sha256:") {
		t.Errorf("got %d conversions, %d files, source %q", len(l.Listing.Conversions), len(l.Files), l.Source)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "listing.json")
	if err := os.WriteFile(bad, []byte(`{"publisher":"brf-tech","conversions":[{"from":["pdf"],"to":["pdf"]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConversions(bad); err == nil || !strings.Contains(err.Error(), "is in both from and to") {
		t.Errorf("a listing the schema refuses: %v", err)
	}
}

// Every listing under seed/official passes what an import will check
// first: the schema, the files it names, and the image rules (an icon is a
// square PNG or WebP of 128 to 1024 px, a screenshot within the store's
// bounds). The pipeline checks them again, this only fails early.
func TestSeedListingsAreValid(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "seed", "official", "*", "listing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 8 {
		t.Fatalf("found %d seed listings: %v", len(paths), paths)
	}
	for _, p := range paths {
		op, err := loadListing(p, 5<<20)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if op.Listing.Publisher != "brf-tech" || op.Listing.Summary["en"] == "" || op.Listing.Icon == "" || op.Description == "" {
			t.Errorf("%s: publisher, an English summary, an icon and a description are expected", p)
		}
		for path, b := range op.Files {
			typ, w, h, err := media.Inspect(b)
			if err != nil {
				t.Errorf("%s %s: %v", p, path, err)
				continue
			}
			if path == op.Listing.Icon {
				if typ == media.JPEG || w != h || w < resolve.IconMin || w > resolve.IconMax {
					t.Errorf("%s %s: icon %s %dx%d", p, path, typ, w, h)
				}
				continue
			}
			if w < resolve.ShotMinSide || h < resolve.ShotMinSide || w > resolve.ShotMaxSide || h > resolve.ShotMaxSide || w*h > resolve.ShotMaxPixels {
				t.Errorf("%s %s: screenshot %dx%d", p, path, w, h)
			}
		}
	}
}

// A secret reaches notify-webhook on standard input, its first line.
func TestNotifyWebhookHelpers(t *testing.T) {
	if v, err := readSecretLine(strings.NewReader("  Bearer bn_abc  \nsecond line\n")); err != nil || v != "Bearer bn_abc" {
		t.Fatalf("first line: %q %v", v, err)
	}
	if _, err := readSecretLine(strings.NewReader("\n")); err == nil {
		t.Fatal("an empty line was taken")
	}
	if got := eventList("all"); len(got) != 7 {
		t.Fatalf("all: %v", got)
	}
	if got := eventList(" filex.released, submission.approved ,"); len(got) != 2 || got[0] != "filex.released" || got[1] != "submission.approved" {
		t.Fatalf("list: %v", got)
	}
}
