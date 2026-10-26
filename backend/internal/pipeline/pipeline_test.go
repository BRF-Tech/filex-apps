package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	ghapi "github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

func TestStateMachine(t *testing.T) {
	allowed := [][2]domain.SubmissionStatus{
		{domain.SubQueued, domain.SubResolving}, {domain.SubResolving, domain.SubValidating},
		{domain.SubValidating, domain.SubNeedsReview}, {domain.SubNeedsReview, domain.SubApproved},
		{domain.SubNeedsReview, domain.SubRejected}, {domain.SubNeedsReview, domain.SubValidating},
		{domain.SubResolving, domain.SubFailed}, {domain.SubValidating, domain.SubFailed},
		{domain.SubFailed, domain.SubQueued}, {domain.SubResolving, domain.SubQueued},
		{domain.SubQueued, domain.SubWithdrawn}, {domain.SubNeedsReview, domain.SubWithdrawn},
	}
	for _, a := range allowed {
		if !CanTransition(a[0], a[1]) {
			t.Errorf("%s -> %s refused", a[0], a[1])
		}
	}
	refused := [][2]domain.SubmissionStatus{
		{domain.SubQueued, domain.SubApproved}, {domain.SubValidating, domain.SubApproved},
		{domain.SubApproved, domain.SubWithdrawn}, {domain.SubRejected, domain.SubQueued},
		{domain.SubWithdrawn, domain.SubQueued}, {domain.SubFailed, domain.SubNeedsReview},
		{domain.SubApproved, domain.SubValidating},
	}
	for _, a := range refused {
		if CanTransition(a[0], a[1]) {
			t.Errorf("%s -> %s allowed", a[0], a[1])
		}
	}
	for _, s := range []domain.SubmissionStatus{domain.SubApproved, domain.SubRejected, domain.SubWithdrawn} {
		if !IsFinal(s) || IsOpen(s) {
			t.Errorf("%s must be final", s)
		}
	}
	for _, s := range []domain.SubmissionStatus{domain.SubQueued, domain.SubResolving, domain.SubValidating, domain.SubNeedsReview} {
		if !IsOpen(s) || IsFinal(s) {
			t.Errorf("%s must be open", s)
		}
	}
	if IsOpen(domain.SubFailed) || IsFinal(domain.SubFailed) {
		t.Error("failed is neither open (it may be retried) nor final")
	}
	// The store's list of open statuses is the state machine's.
	for _, s := range store.OpenStatuses {
		if !IsOpen(domain.SubmissionStatus(s)) {
			t.Errorf("store open status %s is not open", s)
		}
	}
}

func res(name, version string) *domain.Resolution {
	return &domain.Resolution{RepoOwner: "alice", RepoName: "filex-hello", RepoID: 9001, OwnerID: 1, Name: name, Version: version,
		Kind: domain.KindApp, License: "MIT", Ref: "v" + version, ManifestSHA256: strings.Repeat("a", 64),
		Permissions: []string{"files:read"}}
}

func findings(t *testing.T, c domain.CheckResult) []string {
	t.Helper()
	var d struct {
		Findings []struct{ Code string } `json:"findings"`
	}
	_ = json.Unmarshal(c.Data, &d)
	var out []string
	for _, f := range d.Findings {
		out = append(out, f.Code)
	}
	return out
}

func TestPolicy(t *testing.T) {
	now := time.Now()
	mine := &store.App{ID: 1, Name: "hello", RepoID: 9001, PublisherID: "alice-pub"}
	theirs := &store.App{ID: 2, Name: "hello", RepoID: 5555, PublisherID: "other"}
	sameNameMyOtherRepo := &store.App{ID: 3, Name: "hello", RepoID: 5555, PublisherID: "alice-pub"}

	cases := []struct {
		name   string
		res    *domain.Resolution
		facts  PolicyFacts
		status domain.CheckStatus
		code   string
	}{
		{"new app", res("hello", "1.0.0"), PolicyFacts{}, domain.CheckPassed, ""},
		{"name taken", res("hello", "1.0.0"), PolicyFacts{ByName: theirs}, domain.CheckFailed, "name_taken"},
		{"repo changed", res("hello", "1.0.0"), PolicyFacts{ByName: sameNameMyOtherRepo}, domain.CheckFailed, "repo_changed"},
		{"reserved", res("admin", "1.0.0"), PolicyFacts{Reserved: true, ReservedReason: "the product"}, domain.CheckFailed, "name_reserved"},
		{"known app keeps name", res("hello2", "1.1.0"), PolicyFacts{ByRepo: mine, Versions: []string{"1.0.0"}}, domain.CheckFailed, "name_changed"},
		{"next version", res("hello", "1.1.0"), PolicyFacts{ByRepo: mine, ByName: mine, Versions: []string{"1.0.0"}}, domain.CheckPassed, ""},
		{"version exists", res("hello", "1.0.0"), PolicyFacts{ByRepo: mine, ByName: mine, Versions: []string{"1.0.0"}}, domain.CheckFailed, "version_exists"},
		{"same version spelled with v", res("hello", "v1.0.0"), PolicyFacts{ByRepo: mine, ByName: mine, Versions: []string{"1.0.0"}}, domain.CheckFailed, "version_exists"},
		{"backport", res("hello", "0.9.1"), PolicyFacts{ByRepo: mine, ByName: mine, Versions: []string{"1.0.0", "0.9.0"}}, domain.CheckWarned, "version_backport"},
		{"not semver", res("hello", "1.0"), PolicyFacts{}, domain.CheckFailed, "version_invalid"},
		{"near name", res("hel1o", "1.0.0"), PolicyFacts{Names: map[string]string{"hello": "listed", "zzz": "listed"}}, domain.CheckWarned, "name_similar"},
		{"near a revoked name is fine", res("hel1o", "1.0.0"), PolicyFacts{Names: map[string]string{"hello": "revoked"}}, domain.CheckPassed, ""},
		{"no license", func() *domain.Resolution { r := res("hello", "1.0.0"); r.License = ""; return r }(), PolicyFacts{}, domain.CheckFailed, "license_missing"},
		{"unknown license", func() *domain.Resolution { r := res("hello", "1.0.0"); r.License = "NOASSERTION"; return r }(), PolicyFacts{}, domain.CheckFailed, "license_missing"},
		{"publisher moved", res("hello", "1.1.0"), PolicyFacts{ByRepo: &store.App{Name: "hello", RepoID: 9001, PublisherID: "other"}}, domain.CheckFailed, "publisher_changed"},
		{"an app does not become a storage plugin", func() *domain.Resolution { r := res("hello", "1.1.0"); r.Kind = domain.KindStorage; return r }(),
			PolicyFacts{ByRepo: mine, ByName: mine, Versions: []string{"1.0.0"}}, domain.CheckFailed, "kind_changed"},
		{"a storage plugin does not become an app", res("hello", "1.1.0"),
			PolicyFacts{ByRepo: &store.App{ID: 1, Name: "hello", Kind: "storage", RepoID: 9001, PublisherID: "alice-pub"}, Versions: []string{"1.0.0"}}, domain.CheckFailed, "kind_changed"},
		{"a language pack stays one", func() *domain.Resolution { r := res("hello", "1.1.0"); r.Kind = domain.KindLanguagePack; return r }(),
			PolicyFacts{ByRepo: &store.App{ID: 1, Name: "hello", Kind: "language_pack", RepoID: 9001, PublisherID: "alice-pub"}, Versions: []string{"1.0.0"}}, domain.CheckPassed, ""},
	}
	for _, c := range cases {
		got := Policy(c.res, "alice-pub", c.facts, now)
		if got.Kind != domain.CheckPolicy || got.Status != c.status || got.Code != c.code || got.EndedAt == nil {
			t.Errorf("%s: %s %s (%s), want %s %s; findings %v", c.name, got.Status, got.Code, got.Message, c.status, c.code, findings(t, got))
		}
	}
	// A failure outranks a warning, and every finding is kept.
	r := res("hel1o", "1.0.0")
	r.License = ""
	got := Policy(r, "alice-pub", PolicyFacts{Names: map[string]string{"hello": "listed"}}, now)
	if got.Status != domain.CheckFailed || got.Code != "license_missing" || len(findings(t, got)) != 2 {
		t.Fatalf("mixed findings: %+v %v", got, findings(t, got))
	}
}

func TestOneEditApart(t *testing.T) {
	yes := [][2]string{{"hello", "hallo"}, {"hello", "hell"}, {"hello", "helloo"}, {"hello", "ehllo"}, {"sign", "sing"}, {"a", "b"}, {"pdf", "pd"}}
	no := [][2]string{{"hello", "hello"}, {"hello", "help"}, {"hello", "world"}, {"sign", "signer"}, {"abc", "cba"}}
	for _, p := range yes {
		if !oneEditApart(p[0], p[1]) || !oneEditApart(p[1], p[0]) {
			t.Errorf("%s ~ %s should be one edit", p[0], p[1])
		}
	}
	for _, p := range no {
		if oneEditApart(p[0], p[1]) {
			t.Errorf("%s ~ %s should not be one edit", p[0], p[1])
		}
	}
}

func dryRow(label string, st domain.CheckStatus) domain.CheckResult {
	return domain.CheckResult{Kind: domain.CheckDryRun, Validator: label, Status: st}
}

// strict counts every permission id filex lists (no derived ones).
var strict = PermissionRule{IsDerived: func(string) bool { return false }}

func TestEnforcePins(t *testing.T) {
	r := res("hello", "1.0.0")
	r.Wasm = &domain.Artifact{SHA256: strings.Repeat("b", 64)}
	r.UI = &domain.Artifact{SHA256: strings.Repeat("c", 64)}
	perms := []domain.PermissionRow{{ID: "files:read", Label: "Reads files"}}
	good := domain.DryRunResult{Validator: "latest", FilexVersion: "0.50.0", OK: true, Kind: domain.KindApp,
		ManifestSHA256: strings.Repeat("A", 64), WasmSHA256: strings.Repeat("b", 64), UISHA256: strings.Repeat("c", 64), Permissions: perms}
	now := time.Now()

	out := EnforcePins(r, []domain.DryRunResult{good}, []domain.CheckResult{dryRow("latest", domain.CheckPassed)}, now, strict)
	if out[0].Status != domain.CheckPassed {
		t.Fatalf("matching answer failed: %+v", out[0])
	}
	var back domain.DryRunResult
	if json.Unmarshal(out[0].Data, &back) != nil || back.FilexVersion != "0.50.0" {
		t.Fatalf("the answer is not the row's data: %s", out[0].Data)
	}

	for name, mut := range map[string]func(d *domain.DryRunResult){
		"manifest":  func(d *domain.DryRunResult) { d.ManifestSHA256 = strings.Repeat("0", 64) },
		"module":    func(d *domain.DryRunResult) { d.WasmSHA256 = strings.Repeat("0", 64) },
		"interface": func(d *domain.DryRunResult) { d.UISHA256 = strings.Repeat("0", 64) },
		"kind":      func(d *domain.DryRunResult) { d.Kind = domain.KindLanguagePack },
		"permissions": func(d *domain.DryRunResult) {
			d.Permissions = append(d.Permissions, domain.PermissionRow{ID: "net:fetch"})
		},
	} {
		d := good
		d.Permissions = append([]domain.PermissionRow{}, good.Permissions...)
		mut(&d)
		out := EnforcePins(r, []domain.DryRunResult{d}, []domain.CheckResult{dryRow("latest", domain.CheckPassed)}, now, strict)
		if out[0].Status != domain.CheckFailed || !strings.HasSuffix(out[0].Code, "_mismatch") {
			t.Errorf("%s disagreement reached review: %+v", name, out[0])
		}
	}

	// filex does not answer manifest_sha256 for an app with a module: the
	// comparison is made only when it is given.
	noManifest := good
	noManifest.ManifestSHA256 = ""
	if out := EnforcePins(r, []domain.DryRunResult{noManifest}, []domain.CheckResult{dryRow("latest", domain.CheckPassed)}, now, strict); out[0].Status != domain.CheckPassed {
		t.Fatalf("absent manifest_sha256 failed: %+v", out[0])
	}

	// An answer the dry runner made no row for still gets one; a refusal stays a refusal.
	refused := domain.DryRunResult{Validator: "floor", OK: false, Code: "incompatible", Message: "needs 0.50"}
	out = EnforcePins(r, []domain.DryRunResult{good, refused}, []domain.CheckResult{dryRow("latest", domain.CheckPassed)}, now, strict)
	if len(out) != 2 || out[1].Validator != "floor" || out[1].Status != domain.CheckFailed || out[1].Code != "incompatible" {
		t.Fatalf("extra answer: %+v", out)
	}
	// Without a rule the permission comparison is the dry runner's alone.
	extra := good
	extra.Permissions = append(append([]domain.PermissionRow{}, good.Permissions...), domain.PermissionRow{ID: "ui"})
	if out := EnforcePins(r, []domain.DryRunResult{extra}, []domain.CheckResult{dryRow("latest", domain.CheckPassed)}, now, PermissionRule{}); out[0].Status != domain.CheckPassed {
		t.Fatalf("no rule, yet permissions compared: %+v", out[0])
	}
	// Other check kinds pass through.
	other := domain.CheckResult{Kind: domain.CheckPolicy, Status: domain.CheckPassed}
	if out := EnforcePins(r, nil, []domain.CheckResult{other}, now, strict); len(out) != 1 || out[0].Kind != domain.CheckPolicy {
		t.Fatalf("non dry-run rows changed: %+v", out)
	}
}

func TestDryRunOutcomeAndPrimary(t *testing.T) {
	ok, code, _ := DryRunOutcome([]domain.CheckResult{dryRow("latest", domain.CheckPassed), dryRow("floor", domain.CheckSkipped)})
	if !ok || code != "" {
		t.Fatal("one pass and one skip is ready for review")
	}
	if ok, code, msg := DryRunOutcome([]domain.CheckResult{dryRow("latest", domain.CheckPassed), {Kind: domain.CheckDryRun, Validator: "floor", Status: domain.CheckFailed, Code: "incompatible", Message: "no"}}); ok || code != "incompatible" || !strings.HasPrefix(msg, "floor: ") {
		t.Fatalf("a failed row: %v %s %s", ok, code, msg)
	}
	if ok, code, _ := DryRunOutcome([]domain.CheckResult{dryRow("latest", domain.CheckSkipped), dryRow("floor", domain.CheckSkipped)}); ok || code != "no_validator_in_range" {
		t.Fatalf("all skipped: %v %s", ok, code)
	}

	row := func(label, version string, st domain.CheckStatus, okAnswer bool) domain.CheckResult {
		c := dryRow(label, st)
		c.Data, _ = json.Marshal(domain.DryRunResult{Validator: label, FilexVersion: version, OK: okAnswer})
		return c
	}
	p, found := Primary([]domain.CheckResult{row("floor", "0.49.0", domain.CheckPassed, true), row("latest", "0.50.0", domain.CheckPassed, true),
		row("next", "0.51.0", domain.CheckFailed, false)})
	if !found || p.Validator != "latest" {
		t.Fatalf("primary = %+v, want the newest passing filex", p)
	}
	if len(DryRuns([]domain.CheckResult{row("floor", "0.49.0", domain.CheckPassed, true), {Kind: domain.CheckPolicy}})) != 1 {
		t.Fatal("DryRuns reads only dry-run rows")
	}
}

func TestReadyForApproval(t *testing.T) {
	passed := domain.CheckResult{Kind: domain.CheckManifest, Status: domain.CheckPassed}
	if ok, _ := ReadyForApproval([]domain.CheckResult{passed, dryRow("latest", domain.CheckWarned)}); !ok {
		t.Fatal("passed + warned is ready")
	}
	for name, cs := range map[string][]domain.CheckResult{
		"failed":     {passed, {Kind: domain.CheckPolicy, Status: domain.CheckFailed}, dryRow("latest", domain.CheckPassed)},
		"running":    {passed, {Kind: domain.CheckMedia, Status: domain.CheckRunning}, dryRow("latest", domain.CheckPassed)},
		"no dry run": {passed},
		"only skips": {passed, dryRow("latest", domain.CheckSkipped)},
	} {
		if ok, why := ReadyForApproval(cs); ok || why == "" {
			t.Errorf("%s: ready", name)
		}
	}
}

func TestParseRepoAndRef(t *testing.T) {
	for in, want := range map[string]string{
		"alice/filex-hello":                        "alice/filex-hello",
		"https://github.com/alice/filex-hello":     "alice/filex-hello",
		"https://github.com/alice/filex-hello.git": "alice/filex-hello",
		" github.com/Alice/Filex.Hello/ ":          "Alice/Filex.Hello",
	} {
		o, n, ok := ParseRepo(in)
		if !ok || o+"/"+n != want {
			t.Errorf("ParseRepo(%q) = %s/%s %v, want %s", in, o, n, ok, want)
		}
	}
	for _, bad := range []string{"", "alice", "alice/", "/x", "https://gitlab.com/a/b", "a/b/c", "-a/b", "a/..", "al ice/x", "a/" + strings.Repeat("x", 101)} {
		if _, _, ok := ParseRepo(bad); ok {
			t.Errorf("ParseRepo(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"v1.0.0", "1.2.3", "release/2026-10"} {
		if !ValidRef(ok) {
			t.Errorf("ValidRef(%q) refused", ok)
		}
	}
	for _, bad := range []string{"", "-x", "a..b", "a b", "a~1", "a^", "a:b", "x\x00", strings.Repeat("v", 256)} {
		if ValidRef(bad) {
			t.Errorf("ValidRef(%q) accepted", bad)
		}
	}
}

func TestManifestReading(t *testing.T) {
	if l := ManifestLabel(json.RawMessage(`{"label":{"en":"Sign","tr":"İmza"}}`), "sign"); l["en"] != "Sign" || l["tr"] != "İmza" {
		t.Fatalf("label = %v", l)
	}
	if l := ManifestLabel(json.RawMessage(`{"label":{"tr":"İmza"}}`), "sign"); l["en"] != "sign" {
		t.Fatalf("label without English = %v", l)
	}
	if l := ManifestLabel(json.RawMessage(`{"label":"Sign"}`), "sign"); l["en"] != "Sign" {
		t.Fatalf("string label = %v", l)
	}
	if l := ManifestLabel(json.RawMessage(`{}`), "sign"); l["en"] != "sign" {
		t.Fatalf("no label = %v", l)
	}
	if e := ManifestEngines(json.RawMessage(`{"engines":{"poppler":true,"ffmpeg":true,"office":false}}`)); strings.Join(e, ",") != "ffmpeg,poppler" {
		t.Fatalf("engines = %v", e)
	}
	if e := ManifestEngines(json.RawMessage(`not json`)); e == nil || len(e) != 0 {
		t.Fatalf("engines of garbage = %v", e)
	}
}

func TestMakeDiffAndRequest(t *testing.T) {
	prev := store.Version{Version: "1.0.0", FilexRange: ">=0.49.0", Permissions: []string{"files:read", "net:fetch"},
		Wasm: &store.Artifact{SHA256: strings.Repeat("b", 64)}}
	r := res("hello", "1.1.0")
	r.FilexRange = ">=0.50.0"
	r.Permissions = []string{"files:read", "files:write"}
	r.Wasm = &domain.Artifact{SHA256: strings.Repeat("B", 64)}
	r.UI = &domain.Artifact{SHA256: strings.Repeat("c", 64)}
	d := MakeDiff(prev, r)
	if d.From != "1.0.0" || strings.Join(d.PermissionsAdded, ",") != "files:write" || strings.Join(d.PermissionsRemoved, ",") != "net:fetch" ||
		d.FilexFrom != ">=0.49.0" || d.FilexTo != ">=0.50.0" || d.ModuleChanged || !d.UIChanged {
		t.Fatalf("diff = %+v", d)
	}
	req := DryRunRequestFor(r)
	if req.Repo != "alice/filex-hello" || req.ExpectWasmSHA256 != r.Wasm.SHA256 || req.ExpectUISHA256 != r.UI.SHA256 ||
		req.ExpectManifestSHA256 != r.ManifestSHA256 || len(req.ExpectPermissions) != 2 || req.FilexRange != ">=0.50.0" {
		t.Fatalf("request = %+v", req)
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	if backoff(1) != 10*time.Second || backoff(2) != 30*time.Second || backoff(3) != 90*time.Second || backoff(20) != 15*time.Minute {
		t.Fatalf("backoff: %v %v %v %v", backoff(1), backoff(2), backoff(3), backoff(20))
	}
}

type fakeAccess struct {
	repo            *ghapi.Repo
	check           domain.CheckResult
	err             error
	gotRepo, gotTok string
}

func (f *fakeAccess) RepoAccess(_ context.Context, repo, token string) (*ghapi.Repo, domain.CheckResult, error) {
	f.gotRepo, f.gotTok = repo, token
	return f.repo, f.check, f.err
}

func TestCheckRepoUsesTheResolversCheckAndTheBoundOwner(t *testing.T) {
	ctx := context.Background()
	pub := store.Publisher{ID: "alice-pub", GitHubOwner: "alice", GitHubOwnerID: 1}
	now := time.Now()
	passed := domain.CheckResult{Kind: domain.CheckRepoAccess, Status: domain.CheckPassed, StartedAt: now}
	repo := &ghapi.Repo{ID: 9001, Name: "filex-hello", FullName: "alice/filex-hello", Owner: ghapi.Owner{ID: 1, Login: "alice", Type: "User"}}

	f := &fakeAccess{repo: repo, check: passed}
	a, c, err := (&Service{Repos: f}).checkRepo(ctx, "gho_tok", pub, "alice", "filex-hello")
	if err != nil || a.ID != 9001 || a.OwnerID != 1 || a.Owner+"/"+a.Name != "alice/filex-hello" || c.Kind != domain.CheckRepoAccess || c.Status != domain.CheckPassed {
		t.Fatalf("passing check: %+v %+v %v", a, c, err)
	}
	if f.gotRepo != "alice/filex-hello" || f.gotTok != "gho_tok" {
		t.Fatalf("asked for %s with %q", f.gotRepo, f.gotTok)
	}

	elsewhere := *repo
	elsewhere.Owner = ghapi.Owner{ID: 2, Login: "mallory", Type: "User"}
	failed := domain.CheckResult{Kind: domain.CheckRepoAccess, Status: domain.CheckFailed, Code: "repo_archived", Message: "alice/filex-hello is archived"}
	for name, c := range map[string]struct {
		svc  *Service
		tok  string
		kind RefusalKind
		code string
	}{
		"other owner": {&Service{Repos: &fakeAccess{repo: &elsewhere, check: passed}}, "t", Forbidden, "owner_mismatch"},
		"failed":      {&Service{Repos: &fakeAccess{repo: repo, check: failed}}, "t", Forbidden, "repo_archived"},
		"github down": {&Service{Repos: &fakeAccess{err: errors.New("503")}}, "t", Unavailable, "github_unavailable"},
		"no token":    {&Service{Repos: &fakeAccess{repo: repo, check: passed}}, "", Forbidden, "github_required"},
		"no checker":  {&Service{}, "t", Forbidden, "github_required"},
	} {
		_, _, err := c.svc.checkRepo(ctx, c.tok, pub, "alice", "filex-hello")
		var r *Refusal
		if !errors.As(err, &r) || r.Kind != c.kind || r.Code != c.code {
			t.Errorf("%s: %v, want %s", name, err, c.code)
		}
	}
}

func TestApprovedRangesAreInFilexrangesNormalForm(t *testing.T) {
	for in, want := range map[string]string{
		">=0.50.0 >=0.50.0":        ">=0.50.0",
		">=0.49.0 || >=0.49.0":     ">=0.49.0",
		">=v0.49.0 <0.60.0":        ">=0.49.0 <0.60.0",
		"not a range at all ~~ ^1": "not a range at all ~~ ^1",
	} {
		if got := canonicalRange(in); got != want {
			t.Errorf("canonicalRange(%q) = %q, want %q", in, got, want)
		}
	}
}
