package resolve

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filexrange"
)

// ManifestFile is the manifest's name at the repository root.
const ManifestFile = "filex-app.json"

// manifest is the part of filex-app.json the store reads, LENIENTLY: unknown
// fields are ignored here (a newer filex's field is not the store's to
// refuse - the validators decide what their filex accepts). The fields and
// their meaning are filex's wire.Manifest.
type manifest struct {
	ManifestVersion int                        `json:"manifest_version"`
	Name            string                     `json:"name"`
	Version         string                     `json:"version"`
	Homepage        string                     `json:"homepage"`
	Filex           string                     `json:"filex"`
	MinFilex        string                     `json:"min_filex"`
	Permissions     []string                   `json:"permissions"`
	Languages       []string                   `json:"languages"`
	UILocales       map[string]json.RawMessage `json:"ui_locales"`
	Wasm            *struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"wasm"`
	UI *struct {
		Bundle struct {
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
		} `json:"bundle"`
	} `json:"ui"`
	Actions     []json.RawMessage `json:"actions"`
	Views       []json.RawMessage `json:"views"`
	PublicPages []json.RawMessage `json:"public_pages"`
	Settings    []json.RawMessage `json:"settings"`
	Thumbnails  any               `json:"thumbnails"`
}

// filex's name rule (wasmplugin.nameRe).
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// isLanguagePack is filex's wire.Manifest.IsLanguagePack: languages and
// nothing that runs.
func (m *manifest) isLanguagePack() bool {
	return len(m.UILocales) > 0 && m.Wasm == nil && m.UI == nil &&
		len(m.Actions) == 0 && len(m.Views) == 0 && len(m.PublicPages) == 0 &&
		len(m.Settings) == 0 && len(m.Permissions) == 0 && m.Thumbnails == nil
}

// parsed is what the resolver takes from a manifest.
type parsed struct {
	m           manifest
	kind        domain.AppKind
	rng         filexrange.Range
	permissions []string
	languages   []string
}

// parseManifest reads a manifest leniently and checks only what the store
// itself stands on: a name, a version, a range that parses, the kind.
func parseManifest(b []byte) (*parsed, error) {
	var p parsed
	if err := json.Unmarshal(b, &p.m); err != nil {
		return nil, fmt.Errorf("not a JSON manifest: %v", err)
	}
	if !nameRe.MatchString(p.m.Name) {
		return nil, fmt.Errorf("name %q must match %s", p.m.Name, nameRe)
	}
	if strings.TrimSpace(p.m.Version) == "" {
		return nil, fmt.Errorf("version is required")
	}
	rng, err := filexrange.Compat(p.m.Filex, p.m.MinFilex)
	if err != nil {
		return nil, fmt.Errorf("the filex range does not parse: %v", err)
	}
	p.rng = rng
	p.kind = domain.KindApp
	if p.m.isLanguagePack() {
		p.kind = domain.KindLanguagePack
	}
	p.permissions = NormalizePermissions(p.m.Permissions)
	if p.kind == domain.KindLanguagePack {
		for code := range p.m.UILocales {
			p.languages = append(p.languages, strings.ToLower(strings.TrimSpace(code)))
		}
		sort.Strings(p.languages)
	} else {
		seen := map[string]bool{}
		for _, code := range p.m.Languages {
			c := strings.ToLower(strings.TrimSpace(code))
			if c != "" && !seen[c] {
				seen[c] = true
				p.languages = append(p.languages, c)
			}
		}
		if len(p.languages) == 0 {
			p.languages = []string{"en"}
		}
	}
	return &p, nil
}

// NormalizePermissions writes permission ids the way filex's ParsePermission
// does before it lists them (trimmed; the host of `http:` lower-cased),
// without duplicates, in the manifest's order (domain.NormalizePermissions,
// which the index uses for a version's review rows too).
func NormalizePermissions(ids []string) []string { return domain.NormalizePermissions(ids) }

// IsDerivedPermission reports whether filex adds id itself from the
// manifest's `ui`, `thumbnails`, `encrypted_folders` or `co_edit` block
// (filex isUIPerm / isThumbPerm / isE2EPlainPerm / isCoEditPerm): it is in
// the dry run's rows but never in the manifest's `permissions`.
// Measured on 2026-10-03: filex-drawio declares files:read and files:write;
// the dry run lists ui, ui:package-fetch, ui-viewer:.drawio, ui-viewer:.dio
// and ui-new:.drawio as well. filex 0.55 adds ui:frame-package
// (`ui.frame_package`: frames of the app's own package) and ui:connect-blob
// (`ui.connect_blob`: blob: in connect-src), which the office editor app
// asks for; only a 0.55 validator lists them (an older filex refuses the
// manifest's field). 0.55 also adds ui:print (`ui.print`: filex prints a
// PDF the interface hands it - the office editor's Print). filex 0.56 adds
// files:e2e-plaintext, from `encrypted_folders: {"open": true}`: the
// browser hands the app's interface a file of an unlocked end-to-end
// encrypted folder in the clear (the office editor's 0.2.0), and
// files:co-edit, from `co_edit: {"open": true}`: the app's interface edits a
// file together with other people through filex's relay (the office
// editor's 0.3.0). Neither is an `ui:` id, so both are named here; only a
// 0.56 validator lists them (an older filex refuses the blocks). filex 0.57
// adds office-engine, from the manifest's `engines.office` block: the app
// brings an office engine filex runs on the server in a process of its own
// (the office editor's 0.4.0; filex wasmplugin PermOfficeEngine). Unknown
// here, a 0.57 validator's dry run would fail the version's pin check
// (permissions_mismatch) and its install links with it.
func IsDerivedPermission(id string) bool {
	switch id {
	case "ui", "ui:eval", "ui:wasm-eval", "ui:package-fetch", "ui:download", "ui:frame-package", "ui:connect-blob", "ui:print":
		return true
	case "files:e2e-plaintext", "files:co-edit", "office-engine":
		return true
	}
	for _, p := range []string{"ui:", "ui-net:", "ui-viewer:", "ui-new:", "thumbnail:"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}
