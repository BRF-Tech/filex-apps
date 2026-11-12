//go:build integration

package httpapi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/appsetup"
)

// TestVersionDetailSaysWhatSettingItUpAsks: a short-link app's version page
// carries what its manifest says about setting it up (filex 0.60.0: its own
// addresses, the link-shortener role, its setup step and the warning before
// its data is erased, and its user permissions), read from the manifest it
// was approved with; an app that declares none has no `setup`.
func TestVersionDetailSaysWhatSettingItUpAsks(t *testing.T) {
	s := newStack(t)
	b, err := os.ReadFile(filepath.Join("..", "appsetup", "testdata", "shortlink.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.seedTyped(t, "brf-tech", "shortlink", "app", "Short links.", map[string]string{"0.1.0": string(b)})
	s.seedTyped(t, "acme", "docs", "app", "Word files.", map[string]string{"1.0.0": docxTool})
	anon := s.newClient()

	var vd struct {
		Setup *appsetup.Setup `json:"setup"`
	}
	anon.get("/api/v1/apps/shortlink/versions/0.1.0").json(t, &vd)
	st := vd.Setup
	if st == nil || !st.NeedsSetup || !st.Wizard || len(st.Routes) != 1 || st.Routes[0].DefaultPrefix != "l" ||
		st.LinkShortener == nil || st.LinkShortener.Requires != "use" || st.ResetWarning["en"] == "" || len(st.UserPermissions) != 4 {
		t.Fatalf("shortlink's version page: %+v", st)
	}
	if r := anon.get("/api/v1/apps/docs/versions/1.0.0"); strings.Contains(string(r.body), `"setup"`) {
		t.Fatalf("an app that declares none: %s", r.body)
	}
}
