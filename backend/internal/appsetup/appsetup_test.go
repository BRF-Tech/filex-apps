package appsetup

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The two short-link apps filex 0.60.0 was made for: BRF-Tech/filex-shortlink
// (its manifest at 54f2ba1) and filex's own example
// (backend/examples/app-shortlinks at v0.60.0). Both are what the store must
// list and show.
func TestRead_TheShortLinkApps(t *testing.T) {
	s, err := Read(read(t, "shortlink.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.NeedsSetup || !s.Wizard {
		t.Errorf("needs setup %v, wizard %v", s.NeedsSetup, s.Wizard)
	}
	if len(s.Routes) != 1 || s.Routes[0].ID != "go" || s.Routes[0].DefaultPrefix != "l" || s.Routes[0].Label["en"] != "Short links" || s.Routes[0].Label["tr"] != "Kısa bağlantılar" {
		t.Errorf("routes %+v", s.Routes)
	}
	if s.LinkShortener == nil || s.LinkShortener.Route != "go" || s.LinkShortener.Requires != "use" {
		t.Errorf("link shortener %+v", s.LinkShortener)
	}
	if !strings.HasPrefix(s.ResetWarning["en"], "Every short link this app made stops working") || s.ResetWarning["tr"] == "" {
		t.Errorf("reset warning %v", s.ResetWarning)
	}
	var ids, defaults []string
	for _, p := range s.UserPermissions {
		ids, defaults = append(ids, p.ID), append(defaults, p.Default)
		if p.Label["en"] == "" || p.Description["en"] == "" {
			t.Errorf("user permission %+v", p)
		}
	}
	if strings.Join(ids, ",") != "use,outside,aliases,manage" || strings.Join(defaults, ",") != "user,admin,admin,admin" {
		t.Errorf("user permissions %v %v", ids, defaults)
	}

	s, err = Read(read(t, "app-shortlinks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !s.NeedsSetup || !s.Wizard || len(s.Routes) != 1 || s.LinkShortener == nil || len(s.UserPermissions) != 1 || s.ResetWarning["en"] == "" {
		t.Errorf("the example: %+v", s)
	}
	if b, _ := json.Marshal(s); !strings.Contains(string(b), `"routes":[{"id":"go"`) || !strings.Contains(string(b), `"link_shortener":{"route":"go","requires":"use"}`) {
		t.Errorf("JSON %s", b)
	}
}

// An app without the blocks reads as empty, with its arrays present.
func TestRead_AnAppWithoutTheBlocks(t *testing.T) {
	s, err := Read([]byte(`{"name":"sign","version":"0.3.0","permissions":["files:read","files:write"],"views":[{"id":"sign","placement":"modal"}]}`))
	if err != nil || !s.Empty() {
		t.Fatalf("%v %+v", err, s)
	}
	if b, _ := json.Marshal(s); string(b) != `{"needs_setup":false,"wizard":false,"routes":[],"user_permissions":[]}` {
		t.Errorf("JSON %s", b)
	}
	// Routes alone are a setup: they have no address until given one.
	s, err = Read([]byte(`{"permissions":["public_routes"],"routes":[{"id":"go","label":{"en":"Go"}}]}`))
	if err != nil || !s.NeedsSetup || s.Wizard {
		t.Errorf("routes alone: %v %+v", err, s)
	}
	// User permissions alone (filex 0.49) are shown, and need no setup.
	s, err = Read([]byte(`{"user_permissions":[{"id":"use","label":{"en":"Use it"},"default":"user"}]}`))
	if err != nil || s.NeedsSetup || s.Empty() || len(s.UserPermissions) != 1 {
		t.Errorf("user permissions alone: %v %+v", err, s)
	}
}

// What filex's InspectManifest refuses in the blocks, the store refuses, in
// filex's words and naming the place.
func TestRead_FilexsRulesForTheBlocks(t *testing.T) {
	const view = `"views":[{"id":"links","placement":"home"},{"id":"setup","placement":"setup"}]`
	const route = `"routes":[{"id":"go","label":{"en":"Short links"}}]`
	for name, c := range map[string]struct{ manifest, want string }{
		"routes without the permission": {
			`{"permissions":["data"],` + route + `}`,
			"routes: the manifest declares routes but never asks for the public_routes permission"},
		"the permission without routes": {
			`{"permissions":["public_routes"]}`,
			"permissions: public_routes without routes"},
		"a route without an English label": {
			`{"permissions":["public_routes"],"routes":[{"id":"go","label":{"tr":"Git"}}]}`,
			"routes[0] (go): label.en is required"},
		"a route without an id": {
			`{"permissions":["public_routes"],"routes":[{"label":{"en":"Go"}}]}`,
			"a route with no id"},
		"two routes, one id": {
			`{"permissions":["public_routes"],"routes":[{"id":"go","label":{"en":"Go"}},{"id":"go","label":{"en":"Again"}}]}`,
			`two routes share the id "go"`},
		"the shortener without its permission": {
			`{"permissions":["public_routes"],` + route + `,"link_shortener":{"route":"go"}}`,
			"link_shortener: the link_shortener block needs the link_shortener permission"},
		"the shortener on an undeclared route": {
			`{"permissions":["public_routes","link_shortener"],` + route + `,"link_shortener":{"route":"elsewhere"}}`,
			`link_shortener: route "elsewhere" is not declared in routes`},
		"the shortener's permission without the block": {
			`{"permissions":["public_routes","link_shortener"],` + route + `}`,
			"permissions: link_shortener without a link_shortener block"},
		"the setup names no view there is": {
			`{"setup":{"view":"wizard"}}`,
			`setup: view "wizard" is not declared in views`},
		"the setup names a view of another placement": {
			`{"views":[{"id":"links","placement":"home"}],"setup":{"view":"links"}}`,
			`setup: view "links" has placement "home", not setup`},
		"a setup view the setup does not name": {
			`{` + view + `}`,
			"views[1] (setup): a setup view is the one setup.view names"},
		"a setup view that requires a permission": {
			`{"views":[{"id":"links","placement":"home"},{"id":"other","placement":"page"},{"id":"setup","placement":"setup","requires":"use"}],"setup":{"view":"setup"}}`,
			"views[2] (setup): a setup view is an administrator's - leave requires out"},
		"a block that does not read": {
			`{"routes":{"id":"go"}}`,
			"the manifest's setup blocks do not read"},
	} {
		_, err := Read([]byte(c.manifest))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// The rest of a manifest is not this package's to judge: what a newer filex
// adds (a permission, a placement) is the validators' to accept or refuse,
// and so is everything filex 0.60's InspectManifest says about the other
// blocks.
func TestRead_OnlyTheBlocksAreJudged(t *testing.T) {
	for name, m := range map[string]string{
		"a permission of a newer filex":         `{"permissions":["files:read","calendar:write","data","public_routes"],"routes":[{"id":"go","label":{"en":"Go"}}]}`,
		"a placement of a newer filex":          `{"views":[{"id":"side","placement":"sidebar"},{"id":"setup","placement":"setup"}],"setup":{"view":"setup"}}`,
		"an inspector without applies":          `{"views":[{"id":"info","placement":"inspector"}]}`,
		"an action writing without files:write": `{"actions":[{"id":"x","output":{"mode":"sibling"}}]}`,
		"no label, no name":                     `{"routes":[]}`,
	} {
		if _, err := Read([]byte(m)); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}
