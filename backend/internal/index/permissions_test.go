package index

// What the index says a version asks for: filex's own review list,
// the rows the app's page shows - not the manifest's declared list alone,
// which leaves out every permission filex derives from the manifest's `ui`,
// `encrypted_folders` and `co_edit` blocks.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// officeEditorRows are the review rows of an office editor version, as a
// validator's dry run answers them: two declared, the rest derived.
func officeEditorRows(t *testing.T) json.RawMessage {
	t.Helper()
	ids := []string{
		"files:read", "files:write",
		"ui", "ui:eval", "ui:wasm-eval", "ui:package-fetch", "ui:download", "ui:frame-package", "ui:connect-blob", "ui:print",
		"ui-viewer:.docx", "ui-viewer:.xlsx", "ui-viewer:.pptx", "ui-new:.docx",
		"files:e2e-plaintext", "files:co-edit",
		// A row the dry run repeated, and one spelled with an upper-case host:
		// written once, as filex writes them.
		"files:read", " http:API.Example.com ",
	}
	rows := make([]domain.PermissionRow, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, domain.PermissionRow{ID: id, Label: "label of " + id})
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuild_AVersionListsFilexsReviewNotTheDeclaredListAlone(t *testing.T) {
	v := store.Version{ID: 30, Version: "0.3.1", Ref: "v0.3.1", Commit: strings.Repeat("1", 40), FilexRange: ">=0.56.0",
		ManifestURL: "https://example/office", ManifestSHA256: strings.Repeat("2", 64), ManifestSig: "so",
		PublishedAt: ts("2026-10-11T00:00:00Z"), Permissions: []string{"files:read", "files:write"},
		PermissionRows: officeEditorRows(t), Engines: []string{}}
	got := buildVersion(v).Permissions
	want := []string{
		"files:read", "files:write",
		"ui", "ui:eval", "ui:wasm-eval", "ui:package-fetch", "ui:download", "ui:frame-package", "ui:connect-blob", "ui:print",
		"ui-viewer:.docx", "ui-viewer:.xlsx", "ui-viewer:.pptx", "ui-new:.docx",
		"files:e2e-plaintext", "files:co-edit", "http:api.example.com",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the index's permissions:\n %v\nwant filex's review list, in its order, each once:\n %v", got, want)
	}

	// And in the signed bytes a filex reads.
	s := snapshot()
	s.Apps[1].Versions = append(s.Apps[1].Versions, v)
	body, err := Canonical(Build(s, "https://fapps.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, must := range []string{`"ui:eval"`, `"files:e2e-plaintext"`, `"files:co-edit"`, `"ui-viewer:.docx"`} {
		if !bytes.Contains(body, []byte(must)) {
			t.Errorf("the canonical index lacks %s", must)
		}
	}
}

func TestBuild_AVersionWithoutReviewRowsListsItsDeclaredPermissions(t *testing.T) {
	for name, rows := range map[string]json.RawMessage{
		"no rows":    nil,
		"empty rows": json.RawMessage(`[]`),
		"unreadable": json.RawMessage(`{"not":"rows"}`),
		"blank ids":  json.RawMessage(`[{"id":" ","label":""}]`),
	} {
		v := store.Version{Version: "1.0.0", Permissions: []string{"files:read", "engines:ffmpeg"}, PermissionRows: rows}
		if got := buildVersion(v).Permissions; strings.Join(got, ",") != "files:read,engines:ffmpeg" {
			t.Errorf("%s: permissions = %v, want the declared list", name, got)
		}
	}
	if got := buildVersion(store.Version{Version: "1.0.0"}).Permissions; got == nil || len(got) != 0 {
		t.Errorf("a version with nothing declared: %#v, want [] (never null)", got)
	}
}

func TestBuild_AStoragePluginKeepsAnEmptyPermissionList(t *testing.T) {
	v := store.Version{Version: "1.3.0", Permissions: []string{}, PermissionRows: json.RawMessage(`[{"id":"files:read","label":"x"}]`),
		Binaries: map[string]store.Artifact{"linux/amd64": {URL: "https://example/amd", SHA256: strings.Repeat("7", 64)}}}
	if got := buildVersion(v).Permissions; got == nil || len(got) != 0 {
		t.Fatalf("a storage plugin's permissions = %#v, want []", got)
	}
}
