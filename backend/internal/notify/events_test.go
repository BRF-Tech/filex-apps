package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/store"
)

var sampleSubmission = store.Submission{ID: 7, PublisherID: "acme", Repo: "acme/filex-hello", Ref: "v1.2.0", Kind: "app",
	Name: "hello", Version: "1.2.0", Attempt: 2, Publisher: store.PublisherRef{ID: "acme", Name: "Acme"}, SubmitterLogin: "alice"}

func data(t *testing.T, ev Event) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(ev.Data)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Submission reaching review: the administrators, once per attempt; a new
// app and a new version are two events.
func TestReviewEvent(t *testing.T) {
	ev := ReviewEvent(sampleSubmission, true)
	if ev.Kind != KindAppSubmitted || ev.Audience != AudienceAdmin || ev.DedupeKey != "review:7:2" {
		t.Fatalf("new app: %+v", ev)
	}
	if ev := ReviewEvent(sampleSubmission, false); ev.Kind != KindVersionSubmitted || ev.DedupeKey != "review:7:2" {
		t.Fatalf("new version: %+v", ev)
	}
	m := Render(ev.Kind, data(t, ev), "https://apps.example", "")
	if m.Title != "New app to review: hello 1.2.0" || m.URL != "https://apps.example/submissions/7" ||
		!strings.Contains(m.Text, "Acme submitted a new app, hello 1.2.0 (acme/filex-hello at v1.2.0") {
		t.Fatalf("message %+v", m)
	}
}

// Approval or rejection: the publisher's members, never the one who
// decided; nobody left means no event.
func TestDecisionEvent(t *testing.T) {
	ev := DecisionEvent(KindApproved, sampleSubmission, []int64{1, 2, 3}, 2, "")
	if ev.Audience != AudienceAccounts || !slices.Equal(ev.Accounts, []int64{1, 3}) || ev.DedupeKey != "decision:7" {
		t.Fatalf("approved: %+v", ev)
	}
	m := Render(ev.Kind, data(t, ev), "https://apps.example", "")
	if m.Title != "Approved: hello 1.2.0" || m.URL != "https://apps.example/apps/hello/versions/1.2.0" || m.Severity != SeveritySuccess {
		t.Fatalf("approved message %+v", m)
	}
	rej := DecisionEvent(KindRejected, sampleSubmission, []int64{1}, 9, "The permissions are not explained.")
	m = Render(rej.Kind, data(t, rej), "https://apps.example", "")
	if m.Title != "Not approved: hello 1.2.0" || !strings.HasSuffix(m.Text, ": The permissions are not explained.") || m.Severity != SeverityWarning {
		t.Fatalf("rejected message %+v", m)
	}
	if ev := DecisionEvent(KindApproved, sampleSubmission, []int64{5}, 5, ""); len(ev.Accounts) != 0 {
		t.Fatalf("the decider alone: %+v", ev)
	}
}

func TestPublisherAndPipelineAndFilexEvents(t *testing.T) {
	pe := PublisherRequestEvent(store.Publisher{ID: "acme", Name: "Acme", GitHubOwner: "acme-org", OwnerType: "Organization"}, "alice")
	m := Render(pe.Kind, data(t, pe), "https://apps.example", "")
	if pe.Audience != AudienceAdmin || m.URL != "https://apps.example/admin/publishers" ||
		m.Text != "alice asked for the publisher Acme (acme), bound to the GitHub organization acme-org. It waits for an administrator to activate it." {
		t.Fatalf("publisher: %+v %+v", pe, m)
	}
	ee := PipelineErrorEvent(sampleSubmission, "dry_run_error", "The validators could not be asked: timeout")
	m = Render(ee.Kind, data(t, ee), "https://apps.example", "")
	if ee.Audience != AudienceAdmin || ee.DedupeKey != "pipeline-error:7:2" || m.Severity != SeverityDanger ||
		!strings.Contains(m.Text, "dry_run_error: The validators could not be asked: timeout") {
		t.Fatalf("pipeline error: %+v %+v", ee, m)
	}
	fe := FilexReleasedEvent("0.58.0", "0.57.3")
	m = Render(fe.Kind, data(t, fe), "https://apps.example", "")
	if fe.Audience != AudienceAll || fe.DedupeKey != "filex:0.58.0" || m.Title != "filex 0.58.0 is out" ||
		m.URL != "https://github.com/BRF-Tech/filex/releases/tag/v0.58.0" || !strings.Contains(m.Text, "(before: 0.57.3)") {
		t.Fatalf("filex: %+v %+v", fe, m)
	}
	for _, code := range []string{"dry_run_error", "validator_unavailable", "resolve_error", "resolver_unavailable"} {
		if !PipelineErrorCodes[code] {
			t.Errorf("%s is not a pipeline error", code)
		}
	}
	if PipelineErrorCodes["sha256_mismatch"] || PipelineErrorCodes["permissions_mismatch"] {
		t.Error("a submission's own failure counts as the store's")
	}
}

// An event stored by an older store, without some fields, still renders.
func TestRender_MissingFieldsAndUnknownKinds(t *testing.T) {
	m := Render(KindApproved, json.RawMessage(`{"repo":"a/b","ref":"v1"}`), "https://apps.example", "")
	if m.Title != "Approved: a/b at v1" {
		t.Fatalf("missing app: %+v", m)
	}
	if m := Render(KindTest, json.RawMessage(`{"channel":"x"}`), "https://apps.example", "Acme Apps"); m.Title != "Test notification from Acme Apps" {
		t.Errorf("test title %q", m.Title)
	}
	if m := Render(KindTest, nil, "https://apps.example:8443/", ""); m.Title != "Test notification from apps.example:8443" {
		t.Errorf("test title without a name %q", m.Title)
	}
	if m := Render("something.new", nil, "https://apps.example", "Acme Apps"); m.Title != "Acme Apps: something.new" {
		t.Errorf("unknown kind title %q", m.Title)
	}
	if m := Render("something.new", nil, "https://apps.example", ""); m.Title == "" || m.URL != "https://apps.example/" {
		t.Fatalf("unknown kind: %+v", m)
	}
}

func TestKinds(t *testing.T) {
	user := KindsFor(false)
	all := KindsFor(true)
	if len(all) != len(Kinds) || len(user) != 3 {
		t.Fatalf("user %v all %v", user, all)
	}
	for _, k := range user {
		if k.Admin {
			t.Errorf("an administrators' event offered to everyone: %s", k.Kind)
		}
	}
	if !AdminOnly(KindAppSubmitted) || AdminOnly(KindApproved) || Known(KindTest) || !Known(KindFilexReleased) {
		t.Error("kind rules")
	}
	if _, err := checkEvents([]string{KindApproved, KindApproved, KindFilexReleased}, false); err != nil {
		t.Errorf("a repeat: %v", err)
	}
	if ev, _ := checkEvents([]string{KindApproved, KindApproved}, false); len(ev) != 1 {
		t.Errorf("not deduplicated: %v", ev)
	}
	if _, err := checkEvents([]string{KindAppSubmitted}, false); err == nil {
		t.Error("a publisher chose an administrators' event")
	}
	if _, err := checkEvents([]string{"nope"}, true); err == nil {
		t.Error("an unknown event was taken")
	}
}

func TestLatestRelease(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"0.57.3", "v0.57.3 (6ca679fb, 2026-10-19T00:00:00Z)", "v0.56.0 (abc, x)"}, "0.57.3"},
		{[]string{"0.57.3", "v0.58.0 (def, y)"}, "0.58.0"},
		{[]string{"0.57.3", "v0.58.0-rc.1 (def, y)", "dev (local)"}, "0.57.3"},
		{[]string{"", "garbage"}, ""},
	} {
		if got := LatestRelease(c.in); got != c.want {
			t.Errorf("%v = %q, want %q", c.in, got, c.want)
		}
	}
}

func fakeResolver(m map[string]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		a, ok := m[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{netip.MustParseAddr(a)}, nil
	}
}

func TestCheckWebhookURL(t *testing.T) {
	s := &Service{Resolve: fakeResolver(map[string]string{"hooks.slack.com": "34.1.2.3", "notify.example": "93.184.216.34",
		"example.com": "93.184.216.34", "intranet.example": "10.1.2.3", "sneaky.example": "127.0.0.1"})}
	for raw, want := range map[string]string{
		"https://hooks.slack.com/services/T0/B0/XXXXSECRET": "https://hooks.slack.com/…",
		"https://notify.example/api/send?group=filex-apps":  "https://notify.example/…",
		"https://example.com":                               "https://example.com",
		"https://example.com/":                              "https://example.com",
		"https://unresolved.example/hook":                   "https://unresolved.example/…",
	} {
		u, err := s.checkWebhookURL(t.Context(), raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got := urlHint(u); got != want {
			t.Errorf("%s: hint %q, want %q", raw, got, want)
		}
	}
	for _, raw := range []string{"http://example.com/hook", "ftp://example.com", "https://user:pw@example.com/",
		"https://127.0.0.1/hook", "https://[::1]/hook", "https://10.0.0.5/", "https://169.254.169.254/", "https://localhost:8443/",
		"https://intranet.example/hook", "https://sneaky.example/hook", "not a url", "https://" + strings.Repeat("a", 2100) + ".example/"} {
		if _, err := s.checkWebhookURL(t.Context(), raw); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	dev := &Service{Development: true}
	if _, err := dev.checkWebhookURL(t.Context(), "http://127.0.0.1:9999/hook"); err != nil {
		t.Errorf("development, a local receiver: %v", err)
	}
}
