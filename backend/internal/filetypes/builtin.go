package filetypes

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// builtinJSON is what filex itself does with each kind of file before any
// app is installed, at one filex tag. It is generated from that tag's source
// by scripts/gen-filex-builtin.mjs (never written by hand) and checked by
// the same script's --check; docs/DEPLOY.md, "filex's own file types", says
// when and how to make it again.
//
//go:embed filex-builtin.json
var builtinJSON []byte

// Capability is one thing filex does with a kind of file.
type Capability struct {
	// Do: preview (shows it in the browser), edit (changes it in place),
	// play (audio and video), thumbnail (draws its picture in the listing),
	// browse (lists and extracts an archive's files), create (its New menu
	// makes one).
	Do string `json:"do"`
	// Needs names an engine (Data.Engines) without which filex does not do
	// it, e.g. "office": the ONLYOFFICE Document Server an administrator
	// connects. Empty: a stock filex does it.
	Needs string `json:"needs,omitempty"`
	// Via is the part of filex that does it ("image viewer", "ONLYOFFICE").
	Via string `json:"via,omitempty"`
}

// Type is what filex does with one extension.
type Type struct {
	Label string       `json:"label,omitempty"`
	Can   []Capability `json:"can"`
}

// Engine is an outside service or a program filex uses for some kinds.
type Engine struct {
	Label string `json:"label"`
	// Kind: "service" (an administrator connects it: ONLYOFFICE, draw.io)
	// or "tool" (a program on the filex server: ffmpeg, 7-Zip).
	Kind string `json:"kind"`
	// Setting is where an administrator turns it on or points filex at it.
	Setting string `json:"setting,omitempty"`
	// InImage: a tool filex's own default Docker image carries.
	InImage bool `json:"in_image,omitempty"`
}

// Source is a file of filex the data was read from.
type Source struct {
	What  string `json:"what"`
	Path  string `json:"path"`
	Lines string `json:"lines,omitempty"`
}

// Data is filex-builtin.json.
type Data struct {
	// Filex is the version the data describes ("0.57.3"), Tag and Commit
	// where it was read.
	Filex   string            `json:"filex"`
	Tag     string            `json:"tag"`
	Commit  string            `json:"commit"`
	Engines map[string]Engine `json:"engines"`
	Types   map[string]Type   `json:"types"`
	// Mimes is filex's media type of each extension it knows, also of kinds
	// it does nothing with: an app's rule may name a media type (image/*),
	// and filex gives a file its type by its extension.
	Mimes   map[string]string `json:"mimes"`
	Sources []Source          `json:"sources"`
}

var (
	builtinOnce sync.Once
	builtin     *Data
	builtinErr  error
)

// Builtin is the embedded filex-builtin.json. A file that does not parse
// is a build mistake: the tests read it, so it never ships.
func Builtin() *Data {
	builtinOnce.Do(func() {
		builtin, builtinErr = ParseBuiltin(builtinJSON)
	})
	if builtinErr != nil {
		panic(builtinErr)
	}
	return builtin
}

// ParseBuiltin reads and checks a filex-builtin.json: every extension in
// filex's grammar, every capability one of the known ones, every engine it
// needs described.
func ParseBuiltin(b []byte) (*Data, error) {
	var d Data
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("filex-builtin.json: %w", err)
	}
	if d.Filex == "" || d.Commit == "" || len(d.Types) == 0 {
		return nil, fmt.Errorf("filex-builtin.json: filex, commit and types are required")
	}
	for ext, t := range d.Types {
		if !ExtOK(ext) {
			return nil, fmt.Errorf("filex-builtin.json: %q is not an extension", ext)
		}
		if len(t.Can) == 0 {
			return nil, fmt.Errorf("filex-builtin.json: %s does nothing", ext)
		}
		for _, c := range t.Can {
			switch c.Do {
			case "preview", "edit", "play", "thumbnail", "browse", "create":
			default:
				return nil, fmt.Errorf("filex-builtin.json: %s: unknown capability %q", ext, c.Do)
			}
			if c.Needs != "" {
				if _, ok := d.Engines[c.Needs]; !ok {
					return nil, fmt.Errorf("filex-builtin.json: %s needs %q, which engines does not describe", ext, c.Needs)
				}
			}
		}
	}
	for id, e := range d.Engines {
		if e.Label == "" || (e.Kind != "service" && e.Kind != "tool") {
			return nil, fmt.Errorf("filex-builtin.json: engine %q needs a label and a kind (service or tool)", id)
		}
	}
	for ext, m := range d.Mimes {
		if !ExtOK(ext) || !mimeRe.MatchString(m) {
			return nil, fmt.Errorf("filex-builtin.json: mimes: %q -> %q", ext, m)
		}
	}
	return &d, nil
}

// Lookup is what filex does with ext by itself.
func (d *Data) Lookup(ext string) (Type, bool) {
	t, ok := d.Types[ext]
	return t, ok
}

// Known reports whether filex knows ext (does something with it, or gives
// it a media type).
func (d *Data) Known(ext string) bool {
	if _, ok := d.Types[ext]; ok {
		return true
	}
	_, ok := d.Mimes[ext]
	return ok
}

// MimeOf is filex's media type of ext ("" when it has none).
func (d *Data) MimeOf(ext string) string { return d.Mimes[ext] }

// EnginesOf are the engines the capabilities of these types need, by id.
func (d *Data) EnginesOf(types []Type) map[string]Engine {
	out := map[string]Engine{}
	for _, t := range types {
		for _, c := range t.Can {
			if c.Needs != "" {
				out[c.Needs] = d.Engines[c.Needs]
			}
		}
	}
	return out
}

// Exts is every extension filex does something with, sorted.
func (d *Data) Exts() []string {
	out := make([]string, 0, len(d.Types))
	for e := range d.Types {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}
