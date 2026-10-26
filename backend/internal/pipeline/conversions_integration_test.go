//go:build integration

package pipeline_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

func auditCount(t *testing.T, w *world, action string) int {
	t.Helper()
	var n int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func withConversions(l *domain.OperatorImport, convs ...filetypes.Conversion) *domain.OperatorImport {
	l.Listing.Conversions = convs
	return l
}

// The conversions a listing declares are kept at approval with where they
// were read; the operator replaces them on an official publisher's
// version (audited, the index untouched) after the same checks, and a
// listing without them removes them.
func TestConversions_KeptAtApprovalAndSetByTheOperator(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.officialBRFTech(t)

	declared := []filetypes.Conversion{{From: []string{"docx", "odt"}, To: []string{"pdf"}}}
	sub, err := w.svc.Import(ctx, pipeline.ImportRequest{Actor: w.op, Publisher: "brf-tech", Repo: demoRepo, Ref: "v1.0.0",
		Listing: withConversions(listing("brf-tech"), declared...)})
	if err != nil {
		t.Fatal(err)
	}
	w.drain(t)
	sub, _ = store.GetSubmission(ctx, w.pool, sub.ID)
	checks, _ := store.ChecksOf(ctx, w.pool, sub.ID, sub.Attempt)
	for _, c := range checks {
		if c.Kind != domain.CheckConsent {
			continue
		}
		var data struct {
			Conversions map[string]int `json:"conversions"`
		}
		if err := json.Unmarshal(c.Data, &data); err != nil || !reflect.DeepEqual(data.Conversions, map[string]int{"entries": 1, "pairs": 2}) {
			t.Errorf("the consent check tells the reviewer what is declared: %s", c.Data)
		}
	}
	res, err := w.svc.Approve(ctx, sub.ID, pipeline.Decision{Actor: w.op, Note: pipeline.OperatorConsent})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ConversionsOf(ctx, w.pool, res.Version.ID)
	if err != nil || !reflect.DeepEqual(got.Conversions, declared) || got.Source != store.ConversionsFromListing ||
		got.Origin != demoRepo+"@"+demoCommit+", operator listing seed/official/demo/listing.json sha256:11" {
		t.Fatalf("kept at approval: %+v %v", got, err)
	}
	sets, _ := store.FileTypesOf(ctx, w.pool, []int64{res.Version.ID})
	if !reflect.DeepEqual(sets[res.Version.ID].Conversions, declared) {
		t.Fatalf("the file types carry them: %+v", sets[res.Version.ID])
	}
	before, err := w.pub.Latest(ctx)
	if err != nil || before == nil {
		t.Fatal(err)
	}

	set := func(l *domain.OperatorImport, app, version string) (pipeline.ConversionsResult, error) {
		return w.svc.SetConversions(ctx, pipeline.ConversionsRequest{Actor: w.op, App: app, Version: version, Listing: l})
	}
	next := []filetypes.Conversion{{From: []string{"docx"}, To: []string{"pdf", "txt"}}, {From: []string{"md"}, To: []string{"html"}}}
	l := withConversions(listing("brf-tech"), next...)
	l.Source = "seed/official/demo/listing.json sha256:22"
	r, err := set(l, "demo", "1.0.0")
	if err != nil || r.Entries != 2 || r.Pairs != 3 || r.Before == "" || r.After == "" || r.Before == r.After || r.Source != store.ConversionsFromListing {
		t.Fatalf("set: %+v %v", r, err)
	}
	got, _ = store.ConversionsOf(ctx, w.pool, res.Version.ID)
	if !reflect.DeepEqual(got.Conversions, next) || got.Source != store.ConversionsFromOperator || got.Origin != l.Source {
		t.Fatalf("after set: %+v", got)
	}
	a := auditData(t, w.pool, "version.conversions")
	if a["app"] != "demo" || a["version"] != "1.0.0" || a["entries"] != float64(2) || a["pairs"] != float64(3) || a["listing"] != l.Source || a["_role"] != audit.RoleAdmin {
		t.Errorf("audit %v", a)
	}
	after, _ := w.pub.Latest(ctx)
	if after.Serial != before.Serial || !bytes.Equal(after.Body, before.Body) {
		t.Errorf("the index changed: serial %d -> %d (conversions are not in it)", before.Serial, after.Serial)
	}

	// The same declaration again changes nothing and writes no audit row.
	n := auditCount(t, w, "version.conversions")
	if r, err := set(l, "demo", "1.0.0"); err != nil || r.Before != r.After {
		t.Fatalf("again: %+v %v", r, err)
	}
	if auditCount(t, w, "version.conversions") != n {
		t.Error("a repeat was audited")
	}

	// Refused, nothing changes.
	for name, c := range map[string]struct {
		l       *domain.OperatorImport
		app, v  string
		code    string
		message string
	}{
		"an engine the manifest lacks": {withConversions(listing("brf-tech"), filetypes.Conversion{From: []string{"doc"}, To: []string{"pdf"}, Needs: []string{"office"}}),
			"demo", "1.0.0", "consent_invalid", "no engines:office permission"},
		"another publisher's listing": {withConversions(listing("acme"), next...), "demo", "1.0.0", "consent_publisher", ""},
		"no such version":             {l, "demo", "9.9.9", "not_found", ""},
		"no such app":                 {l, "nope", "1.0.0", "not_found", ""},
	} {
		_, err := set(c.l, c.app, c.v)
		if refusalCode(err) != c.code || !strings.Contains(err.Error(), c.message) {
			t.Errorf("%s: %v (want %s)", name, err, c.code)
		}
	}
	if _, _, err := w.svc.UpdatePublisher(ctx, "brf-tech", store.PublisherPatch{Official: ptr(false)}, w.op); err != nil {
		t.Fatal(err)
	}
	if _, err := set(l, "demo", "1.0.0"); refusalCode(err) != "publisher_not_official" {
		t.Errorf("a publisher that is not official: %v", err)
	}
	if _, _, err := w.svc.UpdatePublisher(ctx, "brf-tech", store.PublisherPatch{Official: ptr(true)}, w.op); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.ConversionsOf(ctx, w.pool, res.Version.ID); !reflect.DeepEqual(got.Conversions, next) {
		t.Fatalf("a refusal changed them: %+v", got)
	}

	// A listing that declares none removes them.
	if r, err := set(listing("brf-tech"), "demo", "1.0.0"); err != nil || r.Entries != 0 || r.After != "" || r.Before == "" {
		t.Fatalf("remove: %+v %v", r, err)
	}
	if _, err := store.ConversionsOf(ctx, w.pool, res.Version.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after removal: %v", err)
	}
	if auditCount(t, w, "version.conversions") != n+1 {
		t.Error("the removal was not audited")
	}
	if ok, broken, err := audit.Verify(ctx, w.pool); err != nil || !ok {
		t.Errorf("audit chain broken at %d: %v", broken, err)
	}
}

// A listing whose conversions need an engine the manifest does not ask for
// fails the consent check: the store does not list a promise the app
// cannot keep.
func TestConversions_AnEngineTheManifestLacksFailsTheConsentCheck(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.officialBRFTech(t)
	sub, err := w.svc.Import(ctx, pipeline.ImportRequest{Actor: w.op, Publisher: "brf-tech", Repo: demoRepo, Ref: "v1.0.0",
		Listing: withConversions(listing("brf-tech"), filetypes.Conversion{From: []string{"mp4"}, To: []string{"webm"}, Needs: []string{"ffmpeg"}})})
	if err != nil {
		t.Fatal(err)
	}
	w.drain(t)
	sub, _ = store.GetSubmission(ctx, w.pool, sub.ID)
	if sub.Status != "failed" || sub.FailureCode != "consent_invalid" || !strings.Contains(sub.FailureMessage, `"ffmpeg" is not an engine the manifest asks for`) {
		t.Fatalf("submission %s: %s %s", sub.Status, sub.FailureCode, sub.FailureMessage)
	}
}
