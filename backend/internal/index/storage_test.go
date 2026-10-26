package index

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

func storageSnapshot() store.IndexSnapshot {
	s := snapshot()
	s.Apps = append(s.Apps, store.IndexApp{
		App: store.App{ID: 3, Name: "myfs", Kind: "storage", PublisherID: "zeta", RepoOwner: "zeta-org", RepoName: "filex-myfs", Status: "listed"},
		Versions: []store.Version{{ID: 20, Version: "1.3.0", Ref: "v1.3.0", Commit: strings.Repeat("4", 40), FilexRange: ">=0.55.0",
			ManifestURL: "https://github.com/zeta-org/filex-myfs/releases/download/v1.3.0/filex-storage.json", ManifestSHA256: strings.Repeat("5", 64), ManifestSig: "sf",
			PublishedAt: ts("2026-10-08T00:00:00Z"), Permissions: []string{}, Engines: []string{},
			Binaries: map[string]store.Artifact{
				"linux/arm64": {URL: "https://example/arm", SHA256: strings.Repeat("6", 64), Size: 7, Sig: "sa"},
				"linux/amd64": {URL: "https://example/amd", SHA256: strings.Repeat("7", 64), Size: 8, Sig: "sb"},
			},
			Conformance: &domain.Conformance{Platform: "linux/amd64", Filex: "0.55.0", Verified: true, Passed: 9, Skipped: 7,
				Driver: "myfs", Capabilities: []string{"delete", "write"}},
		}},
	})
	return s
}

func TestBuild_AStoragePluginCarriesItsSignedBuildsAndItsRun(t *testing.T) {
	doc := Build(storageSnapshot(), "https://fapps.example")
	var fs *App
	for i := range doc.Apps {
		if doc.Apps[i].Name == "myfs" {
			fs = &doc.Apps[i]
		}
	}
	if fs == nil || fs.Kind != "storage" || fs.Latest != "1.3.0" {
		t.Fatalf("storage entry %+v", fs)
	}
	v := fs.Versions[0]
	if len(v.Binaries) != 2 || v.Binaries["linux/amd64"].Sig != "sb" || v.Binaries["linux/arm64"].SHA256 != strings.Repeat("6", 64) {
		t.Fatalf("binaries %+v", v.Binaries)
	}
	if v.Wasm != nil || v.UI != nil || v.Conformance == nil || !v.Conformance.Verified || v.Conformance.Passed != 9 {
		t.Fatalf("version %+v", v)
	}
	body, err := Canonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	var back Document
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"linux/amd64": {`)) || !bytes.Contains(body, []byte(`"conformance": {`)) {
		t.Fatal("the canonical bytes carry neither the builds nor the run")
	}
}

func TestBuild_AnAppsEntryIsByteForByteWhatItWas(t *testing.T) {
	// The fields a storage plugin adds are absent from every other entry:
	// the index of a catalog without storage plugins does not change.
	body, err := Canonical(Build(snapshot(), "https://fapps.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, never := range []string{`"binaries"`, `"conformance"`} {
		if bytes.Contains(body, []byte(never)) {
			t.Errorf("an app's entry carries %s", never)
		}
	}
}
