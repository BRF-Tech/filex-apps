// Package appsetup reads what a version's manifest says about setting the
// app up, the blocks filex 0.60.0 added (docs/ARCHITECTURE.md "Setting an
// app up"):
//
//   - `routes`: addresses the app answers for visitors with no account (a
//     link shortener's l.example.com/<code>). A route has no address until
//     an administrator gives it one, at setup.
//   - `setup`: an administrator sets the app up once, after installing it
//     and before it runs, starting with the app's own step (`setup.view`, a
//     view with the `setup` placement); `setup.reset.warning` is what the
//     app says before setting it up again, or removing it, erases its data.
//   - `link_shortener`: filex offers "Get short link" beside its own links
//     and the app makes the short address, on one of its routes.
//
// and `user_permissions` (filex 0.49.0): what an administrator grants per
// role and per person inside the app, which the setup's last step shows.
// The store shows them on the app's page; the resolver refuses a manifest
// whose blocks filex would refuse.
//
// # One rule, filex's
//
// The checks are filex's own: pkg/pluginkit/plugintest.InspectManifest, the
// rules an app's author meets at `go test`, from filex's public Go module,
// pinned in go.mod to the release the latest validator runs. Only the
// blocks this package reads are held to them: InspectManifest runs on a
// manifest made of those blocks (and the permissions and views they lean
// on), so its closed sets - the permissions, the placements - never refuse
// what a newer filex adds elsewhere in a manifest. The rest of a manifest
// stays the validators' to judge (internal/validator), as before; so do the
// rules only filex's installer holds (a route's id and prefix shape, every
// declared language in every label, `requires` naming a user permission).
//
// plugintest registers an `-update` flag on flag.CommandLine when it is
// loaded (its golden screens); the server's commands parse flag sets of
// their own and never read it.
package appsetup

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/brf-tech/filex/backend/pkg/pluginkit/plugintest"
	"github.com/brf-tech/filex/backend/pkg/pluginkit/wire"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// The permissions the blocks need (filex wasmplugin PermData,
// PermPublicRoutes, PermLinkShortener). `data` is the app's own key-value
// store; it needs no block, but an app that is set up keeps its setup there.
const (
	PermData          = "data"
	PermPublicRoutes  = "public_routes"
	PermLinkShortener = "link_shortener"
)

// Route is one family of public addresses the app answers.
type Route struct {
	ID    string      `json:"id"`
	Label domain.Text `json:"label"`
	// DefaultPrefix is the path the setup suggests on the filex's own
	// address ("l": files.example.com/l/<code>); the administrator decides.
	DefaultPrefix string `json:"default_prefix,omitempty"`
}

// LinkShortener is the link-shortener role.
type LinkShortener struct {
	// Route is the route the short addresses are on.
	Route string `json:"route"`
	// Requires is the user permission a person needs for "Get short link";
	// empty: whoever may make the link may shorten it.
	Requires string `json:"requires,omitempty"`
}

// UserPermission is one thing an administrator grants per role and per
// person inside the app.
type UserPermission struct {
	ID          string      `json:"id"`
	Label       domain.Text `json:"label"`
	Description domain.Text `json:"description,omitempty"`
	// Default is who holds it until the administrator decides: `viewer`
	// (every account), `user` (accounts that can change files) or `admin`
	// (administrators only).
	Default string `json:"default"`
}

// Setup is what a version's manifest says about setting it up.
type Setup struct {
	// NeedsSetup: an administrator sets the app up once before it runs. It
	// says so (`setup`), or it has routes, which have no address until it
	// is given one (filex wasmplugin Manifest.NeedsSetup).
	NeedsSetup bool `json:"needs_setup"`
	// Wizard: the setup starts with the app's own step (`setup.view`).
	Wizard bool `json:"wizard"`
	// Routes are the app's own public addresses, in the manifest's order.
	Routes []Route `json:"routes"`
	// LinkShortener is set when the app takes the link-shortener role.
	LinkShortener *LinkShortener `json:"link_shortener,omitempty"`
	// ResetWarning is the app's own warning before setting it up again (or
	// removing it) erases its data.
	ResetWarning domain.Text `json:"reset_warning,omitempty"`
	// UserPermissions are what an administrator grants inside the app.
	UserPermissions []UserPermission `json:"user_permissions"`
}

// Empty reports whether the manifest declares none of the blocks.
func (s Setup) Empty() bool {
	return !s.NeedsSetup && len(s.Routes) == 0 && s.LinkShortener == nil && len(s.ResetWarning) == 0 && len(s.UserPermissions) == 0
}

// blocks is the part of a manifest this package reads, in filex's own types
// (pkg/pluginkit/wire); every other field is left alone.
type blocks struct {
	Permissions     []string                `json:"permissions"`
	UserPermissions []wire.UserPermission   `json:"user_permissions"`
	Views           []wire.View             `json:"views"`
	Routes          []wire.Route            `json:"routes"`
	Setup           *wire.SetupSpec         `json:"setup"`
	LinkShortener   *wire.LinkShortenerSpec `json:"link_shortener"`
}

// Read reads the blocks of a manifest's bytes and holds them to filex's
// rules. A manifest without them answers an empty Setup; an error is a
// sentence for the publisher, filex's own, naming where.
func Read(manifest []byte) (Setup, error) {
	var b blocks
	if err := json.Unmarshal(manifest, &b); err != nil {
		return Setup{}, fmt.Errorf("the manifest's setup blocks do not read: %v", err)
	}
	if err := inspect(b); err != nil {
		return Setup{}, err
	}
	out := Setup{
		NeedsSetup:      b.Setup != nil || len(b.Routes) > 0,
		Wizard:          b.Setup != nil && b.Setup.View != "",
		Routes:          []Route{},
		UserPermissions: []UserPermission{},
	}
	for _, r := range b.Routes {
		out.Routes = append(out.Routes, Route{ID: r.ID, Label: text(r.Label), DefaultPrefix: r.DefaultPrefix})
	}
	if ls := b.LinkShortener; ls != nil {
		out.LinkShortener = &LinkShortener{Route: ls.Route, Requires: ls.Requires}
	}
	if b.Setup != nil && b.Setup.Reset != nil {
		out.ResetWarning = text(b.Setup.Reset.Warning)
	}
	for _, p := range b.UserPermissions {
		out.UserPermissions = append(out.UserPermissions, UserPermission{ID: p.ID, Label: text(p.Label), Description: text(p.Description), Default: p.Default})
	}
	return out, nil
}

// inspect runs filex's InspectManifest on a manifest of the blocks alone:
// the routes, the setup and the link-shortener role, the permissions they
// need, the user permissions, and the views the setup leans on (those with
// the setup placement and the one setup.view names). Every error it finds
// there is one of these blocks'. The name, version and label are
// placeholders: the resolver holds the real ones to its own rules, and the
// validators to filex's.
func inspect(b blocks) error {
	m := wire.Manifest{
		Name:            "setup-check",
		Version:         "0",
		Label:           wire.Text{"en": "setup check"},
		UserPermissions: b.UserPermissions,
		Routes:          b.Routes,
		Setup:           b.Setup,
		LinkShortener:   b.LinkShortener,
	}
	for _, p := range b.Permissions {
		switch p = strings.TrimSpace(p); p {
		case PermData, PermPublicRoutes, PermLinkShortener:
			m.Permissions = append(m.Permissions, p)
		}
	}
	named := ""
	if b.Setup != nil {
		named = b.Setup.View
	}
	// at[i] is the index in the real manifest of the i-th view kept, so a
	// finding names the view where the publisher wrote it.
	var at []int
	for i, v := range b.Views {
		if v.Placement == wire.PlacementSetup || (named != "" && v.ID == named) {
			m.Views = append(m.Views, v)
			at = append(at, i)
		}
	}
	errs := plugintest.InspectManifest(m).Errors()
	if len(errs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(errs))
	for _, f := range errs {
		where := f.Where
		for i := range at {
			if p := fmt.Sprintf("manifest.views[%d] ", i); strings.HasPrefix(where, p) {
				where = fmt.Sprintf("manifest.views[%d] ", at[i]) + strings.TrimPrefix(where, p)
				break
			}
		}
		parts = append(parts, strings.TrimPrefix(where, "manifest.")+": "+f.Message)
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// text copies a filex Text into the store's.
func text(t wire.Text) domain.Text {
	if len(t) == 0 {
		return nil
	}
	out := make(domain.Text, len(t))
	for k, v := range t {
		out[k] = v
	}
	return out
}
