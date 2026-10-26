//go:build integration

package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/db/dbtest"
	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
)

// versionWith writes an approved version of app "a" with this manifest, as
// an approval before migration 0007 left it (no file types).
func versionWith(t *testing.T, q Q, version, manifest string) int64 {
	t.Helper()
	ctx := context.Background()
	var acc int64
	if err := q.QueryRow(ctx, `SELECT id FROM accounts LIMIT 1`).Scan(&acc); err != nil {
		if err := q.QueryRow(ctx, `INSERT INTO accounts (display_name) VALUES ('t') RETURNING id`).Scan(&acc); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Exec(ctx, `INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status) VALUES ('pub', 'P', 'p', 1, 'User', 'active')`); err != nil {
			t.Fatal(err)
		}
		if err := InsertActiveKey(ctx, q, SigningKey{ID: "art", Use: "artifact", PublicHex: strings.Repeat("a", 64)}); err != nil {
			t.Fatal(err)
		}
		if _, err := InsertApp(ctx, q, App{Name: "a", Kind: "app", PublisherID: "pub", RepoOwner: "p", RepoName: "a", RepoID: 1, Label: map[string]string{"en": "A"}}); err != nil {
			t.Fatal(err)
		}
	}
	app, err := GetAppByName(ctx, q, "a")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := InsertSubmission(ctx, q, "pub", acc, "p/a", "v"+version, "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := InsertVersion(ctx, q, Version{AppID: app.ID, SubmissionID: sub, Version: version, Ref: "v" + version,
		Commit: strings.Repeat("c", 40), ManifestURL: "https://m", ManifestSHA256: strings.Repeat("f", 64), ManifestSig: "s",
		Manifest: json.RawMessage(manifest), ArtifactKeyID: "art"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBackfillExtractsWhatIsMissingOrOld(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	viewer := versionWith(t, pool, "1.0.0", `{"views": [{"id": "v", "placement": "viewer", "label": {"en": "V"}, "applies": {"ext": ["drawio"]}}]}`)
	pack := versionWith(t, pool, "1.1.0", `{"ui_locales": {"es": {"a": "b"}}}`)
	broken := versionWith(t, pool, "1.2.0", `{"views": 5}`)

	res, err := BackfillFileTypes(ctx, pool)
	if err != nil || res.Extracted != 2 || !reflect.DeepEqual(res.Failed, []int64{broken}) {
		t.Fatalf("backfill: %+v %v (want 2 extracted, %d failed)", res, err, broken)
	}
	sets, err := FileTypesOf(ctx, pool, []int64{viewer, pack, broken})
	if err != nil {
		t.Fatal(err)
	}
	if got := sets[viewer]; got.Extractor != filetypes.Extractor || len(got.Rules) != 1 || got.Rules[0].Exts[0] != "drawio" {
		t.Fatalf("viewer: %+v", got)
	}
	if got, ok := sets[pack]; !ok || len(got.Rules) != 0 {
		t.Fatalf("a language pack has a row with no rules (so it is not read again): %+v %v", got, ok)
	}
	if _, ok := sets[broken]; ok {
		t.Fatal("an unreadable manifest got a row; it should be tried again at the next start")
	}
	var exts []string
	var anyFile bool
	if err := pool.QueryRow(ctx, `SELECT exts, any_file FROM app_version_file_types WHERE version_id = $1`, viewer).Scan(&exts, &anyFile); err != nil ||
		!reflect.DeepEqual(exts, []string{"drawio"}) || anyFile {
		t.Fatalf("the searchable columns: %v %v %v", exts, anyFile, err)
	}

	// Nothing new: a second run does nothing (the broken one is tried again).
	if again, err := BackfillFileTypes(ctx, pool); err != nil || again.Extracted != 0 || len(again.Failed) != 1 {
		t.Fatalf("second run: %+v %v", again, err)
	}

	// A row an older extractor wrote is extracted again.
	if _, err := pool.Exec(ctx, `UPDATE app_version_file_types SET rules = '[]', exts = '{}' WHERE version_id = $1`, viewer); err != nil {
		t.Fatal(err)
	}
	if res, err := backfillFileTypes(ctx, pool, filetypes.Extractor+1); err != nil || res.Extracted != 2 {
		t.Fatalf("a newer extractor: %+v %v (want both rows again)", res, err)
	}
	sets, _ = FileTypesOf(ctx, pool, []int64{viewer})
	if len(sets[viewer].Rules) != 1 {
		t.Fatalf("the row of an older extractor was not replaced: %+v", sets[viewer])
	}

	// The approved version itself is untouched: its row stays immutable.
	if _, err := pool.Exec(ctx, `UPDATE app_versions SET manifest = '{}' WHERE id = $1`, viewer); err == nil {
		t.Fatal("an approved version changed")
	}
}
