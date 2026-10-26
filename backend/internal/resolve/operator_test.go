package resolve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// The BRF-Tech organization's numeric id, as GitHub showed it for
// BRF-Tech/filex-sign (../github/testdata/recorded/repo-filex-sign-anon.json).
const brfTechOwnerID = 159972728

// operatorFixture is filex-sign at v0.3.0 WITHOUT a .filex-apps.json: an
// official publisher's tag cut before the store existed.
func operatorFixture(t *testing.T) *fake {
	t.Helper()
	f := newFake(t)
	f.signFixture(t, nil, "")
	f.mu.Lock()
	delete(f.files, signRepo+"/"+signCommit+"/.filex-apps.json")
	f.mu.Unlock()
	return f
}

func operatorListing(t *testing.T) *domain.OperatorImport {
	t.Helper()
	return &domain.OperatorImport{
		Listing: domain.Consent{Publisher: "brf-tech", Categories: []string{"documents"}, Summary: domain.Text{"en": "Sign PDFs."},
			Description: "description.md", Icon: "icon.png",
			Screenshots: []domain.ConsentShot{{Path: "shots/place.png", Caption: domain.Text{"en": "Placing the boxes"}}}},
		Description:       "# e-Signature\n\nSign PDFs in filex.\n",
		Files:             map[string][]byte{"icon.png": pngOf(t, 256, 256), "shots/place.png": pngOf(t, 1440, 900)},
		Source:            "seed/official/filex-sign/listing.json sha256:00",
		PublisherOfficial: true, PublisherVerified: true, PublisherOwnerID: brfTechOwnerID,
	}
}

func operatorReq(op *domain.OperatorImport) domain.ResolveRequest {
	r := req("")
	r.Operator = op
	return r
}

func consentCheck(checks []domain.CheckResult) domain.CheckResult {
	for _, c := range checks {
		if c.Kind == domain.CheckConsent {
			return c
		}
	}
	return domain.CheckResult{}
}

// The operator's listing stands in for the missing .filex-apps.json; every
// other step runs exactly as for a web submission (the tag, the manifest at
// the commit, the module downloaded and pinned) and the images go through the
// same checks and the same media sink.
func TestResolve_AnOperatorImportStandsInForTheConsentFile(t *testing.T) {
	f := operatorFixture(t)
	ms := &sink{}
	op := operatorListing(t)
	res, checks, err := f.resolver(t, ms, Limits{}).Resolve(context.Background(), operatorReq(op))
	if err != nil {
		t.Fatal(err)
	}
	want := "repo_access=passed tag=passed manifest=passed consent=passed artifacts=passed media=passed"
	if got := statuses(checks); got != want {
		t.Fatalf("checks: %s\nwant:   %s\n%s", got, want, last(checks).Message)
	}
	c := consentCheck(checks)
	if c.Message != OperatorConsent {
		t.Errorf("consent message %q", c.Message)
	}
	var data map[string]any
	_ = json.Unmarshal(c.Data, &data)
	if data["source"] != "operator" || data["listing"] != op.Source || data["owner_id"] != float64(brfTechOwnerID) {
		t.Errorf("consent data %s", c.Data)
	}
	if res.Consent.Publisher != "brf-tech" || res.Consent.Summary["en"] != "Sign PDFs." || res.Description != op.Description {
		t.Errorf("listing %+v / %q", res.Consent, res.Description)
	}
	if res.Wasm == nil || res.Wasm.SHA256 != sum(wasmBytes) || res.CommitSHA != signCommit || res.License != "MIT" {
		t.Errorf("the rest of the resolution %+v", res)
	}
	if len(res.Media) != 2 || len(ms.puts) != 2 || res.Media[0].Role != "icon" || res.Media[1].Caption["en"] != "Placing the boxes" {
		t.Fatalf("media %+v, stored %d", res.Media, len(ms.puts))
	}

	// A web submission of the same tag still needs the repository's own file.
	_, checks, err = f.resolver(t, nil, Limits{}).Resolve(context.Background(), req(""))
	if err != nil || last(checks).Code != CodeConsentMissing {
		t.Errorf("without an operator import: %v %s", err, statuses(checks))
	}
}

// Consent by ownership holds only for an official AND verified publisher.
func TestResolve_AnOperatorImportNeedsAnOfficialVerifiedPublisher(t *testing.T) {
	f := operatorFixture(t)
	r := f.resolver(t, &sink{}, Limits{})
	for name, mutate := range map[string]func(*domain.OperatorImport){
		"not official": func(o *domain.OperatorImport) { o.PublisherOfficial = false },
		"not verified": func(o *domain.OperatorImport) { o.PublisherVerified = false },
		"neither":      func(o *domain.OperatorImport) { o.PublisherOfficial, o.PublisherVerified = false, false },
	} {
		op := operatorListing(t)
		mutate(op)
		res, checks, err := r.Resolve(context.Background(), operatorReq(op))
		if err != nil || last(checks).Kind != domain.CheckConsent || last(checks).Code != CodePublisherNotOfficial {
			t.Errorf("%s: %v %s", name, err, statuses(checks))
		}
		if len(res.Media) != 0 {
			t.Errorf("%s: media mirrored after a failed consent", name)
		}
	}
}

// The repository's owner measured in this resolution must be the
// publisher's bound owner.
func TestResolve_AnOperatorImportNeedsTheOwnersPublisher(t *testing.T) {
	f := operatorFixture(t)
	r := f.resolver(t, &sink{}, Limits{})
	for _, owner := range []int64{42, 0} {
		op := operatorListing(t)
		op.PublisherOwnerID = owner
		_, checks, err := r.Resolve(context.Background(), operatorReq(op))
		if err != nil || last(checks).Code != CodeOwnerMismatch || !strings.Contains(last(checks).Message, "consent by ownership does not hold") {
			t.Errorf("owner %d: %v %s %s", owner, err, statuses(checks), last(checks).Message)
		}
	}
	op := operatorListing(t)
	op.Listing.Publisher = "someone-else"
	if _, checks, _ := r.Resolve(context.Background(), operatorReq(op)); last(checks).Code != CodeConsentPub {
		t.Errorf("listing for another publisher: %s", statuses(checks))
	}
	op = operatorListing(t)
	op.Description = ""
	if _, checks, _ := r.Resolve(context.Background(), operatorReq(op)); last(checks).Code != CodeConsentInvalid {
		t.Errorf("a description named but not carried: %s", statuses(checks))
	}
}

// The operator's images are held to the same rules as a repository's.
func TestResolve_AnOperatorImportsImagesAreCheckedLikeAnyOther(t *testing.T) {
	f := operatorFixture(t)
	svg := []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")
	for name, tc := range map[string]struct {
		mutate func(*domain.OperatorImport)
		lim    Limits
		code   string
	}{
		"jpeg icon":       {func(o *domain.OperatorImport) { o.Files["icon.png"] = jpegOf(t, 256, 256) }, Limits{}, CodeMediaInvalid},
		"not square":      {func(o *domain.OperatorImport) { o.Files["icon.png"] = pngOf(t, 256, 200) }, Limits{}, CodeMediaInvalid},
		"svg":             {func(o *domain.OperatorImport) { o.Files["icon.png"] = svg }, Limits{}, CodeMediaInvalid},
		"missing file":    {func(o *domain.OperatorImport) { delete(o.Files, "shots/place.png") }, Limits{}, CodeMediaInvalid},
		"tiny screenshot": {func(o *domain.OperatorImport) { o.Files["shots/place.png"] = pngOf(t, 100, 100) }, Limits{}, CodeMediaInvalid},
		"over the cap":    {func(o *domain.OperatorImport) {}, Limits{Media: 512}, CodeTooLarge},
	} {
		op := operatorListing(t)
		tc.mutate(op)
		ms := &sink{}
		_, checks, err := f.resolver(t, ms, tc.lim).Resolve(context.Background(), operatorReq(op))
		if err != nil || last(checks).Kind != domain.CheckMedia || last(checks).Code != tc.code {
			t.Errorf("%s: %v %s %s", name, err, statuses(checks), last(checks).Message)
		}
	}
}
