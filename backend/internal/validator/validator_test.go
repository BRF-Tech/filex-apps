package validator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// The answers filex 0.50.0 ("latest") and 0.49.0 ("floor") gave on
// 2026-10-03 (testdata/recorded; each `manifest` cut to the fields the store
// reads). Pins measured at the same time:
const (
	signWasm     = "6a3d2e59aaadb9c69bd219652d2078787e09520f9b7da223646e9dd131396ff3"
	drawioUI     = "2ec43b1f0e2a31f24e7bf26900e177dd5591f0e06ad118875ffdf16885012c83"
	drawioMan    = "4cced6df431dd2152b8c3091c1cc2d2ef7c1f805cdf8cca4f62698b04727cdaa"
	langEsMan    = "570ae0bb3b0bedcd81269694be3c56ffa0d44a3f65c0b8ae87a7e61218c14abd"
	convertWasm  = "e8786f26cf19974e65f0423e0d666bca72281e562deb962cd0668e5383a602d9"
	version050   = "v0.50.0 (9873e3e4835a785a12b62a1ff725868c04103cb5, 2026-10-03T00:50:59Z)"
	version049   = "v0.49.0 (9b183842053c5dd2b44fd225558fae6e2f5dd9f9, 2026-09-29T01:09:29Z)"
	seedEmail    = "validator@local"
	seedPassword = "pw-for-tests"
)

var signPerms = []string{"files:read", "files:write", "files:lock", "sign", "state", "settings", "public_pages", "mail:send", "notify:send", "users:lookup", "http:freetsa.org", "http:fonts.gstatic.com", "schedule"}

type reply struct {
	status int
	body   []byte
}

// fakeFilex is one validator: capabilities, sign-in and the dry run.
type fakeFilex struct {
	srv     *httptest.Server
	version string
	// answers: "<github_repo>@<ref>" -> reply.
	answers map[string]reply
	delay   time.Duration
	totp    bool
	// expireOnce answers the next dry run 401 (the session ended).
	expireOnce atomic.Bool

	logins, caps, calls atomic.Int32
	inflight, maxIn     atomic.Int32
	mu                  sync.Mutex
	tokens              map[string]bool
	bodies              []map[string]any
	langs               []string
}

func recordedAnswer(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/recorded/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFakeFilex(t *testing.T, version string) *fakeFilex {
	f := &fakeFilex{version: version, answers: map[string]reply{}, tokens: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/capabilities":
			f.caps.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"version": f.version, "versions": true})
		case r.Method == http.MethodPost && r.URL.Path == "/api/auth/login":
			var in struct{ Email, Password string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Email != seedEmail || in.Password != seedPassword {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid credentials"}`))
				return
			}
			if f.totp {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"two-factor code required","totp_required":true}`))
				return
			}
			n := f.logins.Add(1)
			tok := strings.Repeat("t", 60) + string(rune('0'+n))
			f.mu.Lock()
			f.tokens[tok] = true
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"role": "admin"}, "token": tok})
		case r.Method == http.MethodPost && r.URL.Path == "/api/admin/app-plugins" && r.URL.Query().Get("dry_run") == "1":
			f.mu.Lock()
			ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			f.mu.Unlock()
			if !ok || f.expireOnce.CompareAndSwap(true, false) {
				f.mu.Lock()
				f.tokens = map[string]bool{}
				f.mu.Unlock()
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			n := f.inflight.Add(1)
			for {
				m := f.maxIn.Load()
				if n <= m || f.maxIn.CompareAndSwap(m, n) {
					break
				}
			}
			defer f.inflight.Add(-1)
			f.calls.Add(1)
			time.Sleep(f.delay)
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			f.mu.Lock()
			f.bodies = append(f.bodies, body)
			f.langs = append(f.langs, r.Header.Get("Accept-Language"))
			f.mu.Unlock()
			key, _ := body["github_repo"].(string)
			ref, _ := body["ref"].(string)
			rep, ok := f.answers[key+"@"+ref]
			if !ok {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":"fetch_failed","reason":"manifest_not_found","status":404,"message":"filex-app.json not found"}`))
				return
			}
			w.WriteHeader(rep.status)
			_, _ = w.Write(rep.body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// pair is the two validators the store runs, answering what filex answered.
func pair(t *testing.T) (*fakeFilex, *fakeFilex, *Runner) {
	t.Helper()
	latest, floor := newFakeFilex(t, version050), newFakeFilex(t, version049)
	for _, c := range []struct {
		f      *fakeFilex
		prefix string
	}{{latest, "latest"}, {floor, "floor"}} {
		c.f.answers["BRF-Tech/filex-sign@v0.3.0"] = reply{200, recordedAnswer(t, c.prefix+"-sign030")}
		c.f.answers["BRF-Tech/filex-drawio@v31.5.3"] = reply{200, recordedAnswer(t, c.prefix+"-drawio")}
		c.f.answers["BRF-Tech/filex-lang-es@main"] = reply{200, recordedAnswer(t, c.prefix+"-lang-es-main")}
	}
	latest.answers["BRF-Tech/filex-convert@v0.2.0"] = reply{200, recordedAnswer(t, "latest-convert020")}
	floor.answers["BRF-Tech/filex-convert@v0.2.0"] = reply{400, recordedAnswer(t, "floor-convert020")}
	latest.answers["BRF-Tech/filex-sign@v9.9.9"] = reply{502, recordedAnswer(t, "latest-notag")}
	floor.answers["BRF-Tech/filex-sign@v9.9.9"] = reply{502, recordedAnswer(t, "latest-notag")}
	return latest, floor, runner(latest, floor)
}

func runner(fs ...*fakeFilex) *Runner {
	var vs []Validator
	for i, f := range fs {
		vs = append(vs, Validator{Label: []string{"latest", "floor", "third"}[i], BaseURL: f.srv.URL + "/"})
	}
	return New(Options{Validators: vs, Email: seedEmail, Password: seedPassword})
}

func signReq() domain.DryRunRequest {
	return domain.DryRunRequest{Repo: "BRF-Tech/filex-sign", Ref: "v0.3.0", Kind: domain.KindApp, FilexRange: ">=0.49.0",
		ExpectManifestSHA256: strings.Repeat("9", 64), ExpectWasmSHA256: signWasm, ExpectPermissions: signPerms}
}

func summary(checks []domain.CheckResult) string {
	var parts []string
	for _, c := range checks {
		s := c.Validator + "=" + string(c.Status)
		if c.Code != "" {
			s += "(" + c.Code + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestDryRun_AModuleAppOnBothValidators(t *testing.T) {
	latest, floor, r := pair(t)
	results, checks, err := r.DryRun(context.Background(), signReq())
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(checks); got != "latest=passed floor=passed" {
		t.Fatalf("checks %s: %v", got, checks)
	}
	if len(results) != 2 {
		t.Fatalf("results %d", len(results))
	}
	for i, want := range []string{"0.50.0", "0.49.0"} {
		res := results[i]
		if !res.OK || res.FilexVersion != want || res.Kind != domain.KindApp || res.WasmSHA256 != signWasm || res.ManifestSHA256 != "" || res.CompatOK == nil || !*res.CompatOK {
			t.Errorf("%s: %+v", res.Validator, res)
		}
		if len(res.Permissions) != 13 || res.Permissions[0].ID != "files:read" || res.Permissions[0].Reason["en"] == "" || len(res.Raw) == 0 {
			t.Errorf("%s permission rows %+v", res.Validator, res.Permissions)
		}
	}
	// filex words the same permission differently between versions; both are kept.
	if results[0].Permissions[0].Label == results[1].Permissions[0].Label {
		t.Error("expected the recorded labels of 0.50.0 and 0.49.0 to differ")
	}
	for _, f := range []*fakeFilex{latest, floor} {
		if len(f.bodies) != 1 || f.bodies[0]["github_repo"] != "BRF-Tech/filex-sign" || f.bodies[0]["ref"] != "v0.3.0" || len(f.bodies[0]) != 2 {
			t.Errorf("body %v: {github_repo, ref} and nothing else", f.bodies)
		}
		if f.langs[0] != "en" {
			t.Errorf("Accept-Language %q", f.langs[0])
		}
	}
}

// The interface-only app: no module, the manifest and the bundle pinned,
// and five permission rows filex derives from the ui block.
func TestDryRun_AnInterfaceOnlyAppDerivedPermissionsAreSetAside(t *testing.T) {
	_, _, r := pair(t)
	req := domain.DryRunRequest{Repo: "BRF-Tech/filex-drawio", Ref: "v31.5.3", Kind: domain.KindApp, FilexRange: ">=0.48.0",
		ExpectManifestSHA256: drawioMan, ExpectUISHA256: drawioUI, ExpectPermissions: []string{"files:read", "files:write"}}
	results, checks, err := r.DryRun(context.Background(), req)
	if err != nil || summary(checks) != "latest=passed floor=passed" {
		t.Fatalf("%v %s: %s", err, summary(checks), checks[0].Message)
	}
	if res := results[0]; res.UISHA256 != drawioUI || res.ManifestSHA256 != drawioMan || res.WasmSHA256 != "" || len(res.Permissions) != 7 {
		t.Errorf("%+v", res)
	}
	req.ExpectUISHA256 = strings.Repeat("0", 64)
	_, checks, _ = r.DryRun(context.Background(), req)
	if summary(checks) != "latest=failed(sha256_mismatch) floor=failed(sha256_mismatch)" {
		t.Errorf("a moved bundle: %s", summary(checks))
	}
	req.ExpectUISHA256, req.ExpectManifestSHA256 = drawioUI, strings.Repeat("1", 64)
	_, checks, _ = r.DryRun(context.Background(), req)
	if summary(checks) != "latest=failed(manifest_sha256_mismatch) floor=failed(manifest_sha256_mismatch)" {
		t.Errorf("a moved manifest: %s", summary(checks))
	}
}

func TestDryRun_ALanguagePackCarriesItsCoverage(t *testing.T) {
	_, _, r := pair(t)
	req := domain.DryRunRequest{Repo: "BRF-Tech/filex-lang-es", Ref: "main", Kind: domain.KindLanguagePack, ExpectManifestSHA256: langEsMan}
	results, checks, err := r.DryRun(context.Background(), req)
	if err != nil || summary(checks) != "latest=passed floor=passed" {
		t.Fatalf("%v %s", err, summary(checks))
	}
	got := []domain.LanguageCoverage{results[0].Languages[0], results[1].Languages[0]}
	want := []domain.LanguageCoverage{{Code: "es", Percent: 100, Keys: 5317, Total: 5313}, {Code: "es", Percent: 99, Keys: 5317, Total: 4501}}
	if got[0] != want[0] || got[1] != want[1] {
		t.Errorf("coverage %+v", got)
	}
	if results[0].CompatOK != nil {
		t.Error("no range, no compat")
	}
}

// filex-convert 0.2.0 needs filex >=0.50.0: the floor is never asked (0.49.0
// would have refused it as manifest_invalid, not as out of range).
func TestDryRun_TheRangePicksTheValidators(t *testing.T) {
	latest, floor, r := pair(t)
	req := domain.DryRunRequest{Repo: "BRF-Tech/filex-convert", Ref: "v0.2.0", Kind: domain.KindApp, FilexRange: ">=0.50.0 >=0.50.0",
		ExpectWasmSHA256: convertWasm, ExpectPermissions: []string{"files:read", "files:write", "engines:ffmpeg", "engines:imagemagick", "engines:office", "engines:ghostscript", "engines:poppler", "engines:rsvg"}}
	results, checks, err := r.DryRun(context.Background(), req)
	if err != nil || summary(checks) != "latest=passed floor=skipped(out_of_range)" {
		t.Fatalf("%v %s", err, summary(checks))
	}
	if len(results) != 1 || strings.Join(results[0].EnginesMissing, ",") != "office" {
		t.Errorf("results %+v", results)
	}
	if floor.calls.Load() != 0 || latest.calls.Load() != 1 {
		t.Errorf("calls latest %d floor %d", latest.calls.Load(), floor.calls.Load())
	}
	// Without the range the floor is asked, and filex's own refusal is kept verbatim.
	req.FilexRange = ""
	results, checks, _ = r.DryRun(context.Background(), req)
	if summary(checks) != "latest=passed floor=failed(manifest_invalid)" || results[1].OK || !strings.Contains(results[1].Message, "unknown engine") {
		t.Errorf("%s %+v", summary(checks), results)
	}
	// A range that admits neither.
	req.FilexRange = ">=0.60.0"
	results, checks, _ = r.DryRun(context.Background(), req)
	if summary(checks) != "latest=skipped(out_of_range) floor=skipped(out_of_range) =failed(no_validator_in_range)" || len(results) != 0 {
		t.Errorf("%s", summary(checks))
	}
	if !strings.Contains(checks[2].Message, "0.50.0") || !strings.Contains(checks[2].Message, "0.49.0") {
		t.Errorf("the refusal must name the versions: %s", checks[2].Message)
	}
	req.FilexRange = "^0.50.0"
	_, checks, err = r.DryRun(context.Background(), req)
	if err != nil || summary(checks) != "=failed(range_invalid)" {
		t.Errorf("%v %s", err, summary(checks))
	}
}

func TestDryRun_AnswersThatDisagreeWithTheResolverFail(t *testing.T) {
	_, _, r := pair(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		mutate func(*domain.DryRunRequest)
		code   string
	}{
		"module moved":     {func(q *domain.DryRunRequest) { q.ExpectWasmSHA256 = strings.Repeat("a", 64) }, CodeSHA256Mismatch},
		"permission added": {func(q *domain.DryRunRequest) { q.ExpectPermissions = signPerms[1:] }, CodePermissionsMismatch},
		"permission dropped": {func(q *domain.DryRunRequest) {
			q.ExpectPermissions = append(append([]string{}, signPerms...), "users:list")
		}, CodePermissionsMismatch},
		"kind":        {func(q *domain.DryRunRequest) { q.Kind = domain.KindLanguagePack }, CodeKindMismatch},
		"ui appeared": {func(q *domain.DryRunRequest) { q.ExpectUISHA256 = drawioUI }, CodeSHA256Mismatch},
	} {
		req := signReq()
		c.mutate(&req)
		results, checks, err := r.DryRun(ctx, req)
		if err != nil || len(checks) != 2 || checks[0].Code != c.code || checks[1].Code != c.code || results[0].OK {
			t.Errorf("%s: %v %s", name, err, summary(checks))
		}
	}
	// Permission ids compare as filex writes them (trimmed, http host lower-cased).
	req := signReq()
	req.ExpectPermissions = append([]string{" files:read", "http:FreeTSA.org"}, signPerms...)
	if _, checks, _ := r.DryRun(ctx, req); summary(checks) != "latest=passed floor=passed" {
		t.Errorf("normalised ids: %s", summary(checks))
	}
}

func TestDryRun_FilexSayingOutOfRangeIsIncompatible(t *testing.T) {
	latest := newFakeFilex(t, version050)
	var a map[string]any
	_ = json.Unmarshal(recordedAnswer(t, "latest-sign030"), &a)
	a["compat"] = map[string]any{"requires": ">=0.51.0", "ok": false, "filex": "0.50.0"}
	b, _ := json.Marshal(a)
	latest.answers["BRF-Tech/filex-sign@v0.3.0"] = reply{200, b}
	_, checks, err := runner(latest).DryRun(context.Background(), signReq())
	if err != nil || summary(checks) != "latest=failed(incompatible)" {
		t.Errorf("%v %s", err, summary(checks))
	}
}

func TestDryRun_FilexRefusalsAreKeptVerbatim(t *testing.T) {
	_, _, r := pair(t)
	req := signReq()
	req.Ref = "v9.9.9"
	results, checks, err := r.DryRun(context.Background(), req)
	if err != nil || summary(checks) != "latest=failed(fetch_failed) floor=failed(fetch_failed)" {
		t.Fatalf("%v %s", err, summary(checks))
	}
	if results[0].OK || !strings.Contains(results[0].Message, "filex-app.json not found") || !strings.Contains(string(results[0].Raw), "manifest_not_found") {
		t.Errorf("%+v", results[0])
	}
}

// Not a judgement of the version: the pipeline retries.
func TestDryRun_FailuresWorthRetrying(t *testing.T) {
	ctx := context.Background()
	for name, rep := range map[string]reply{
		"filex could not reach GitHub": {502, []byte(`{"error":"fetch_failed","reason":"unreachable","message":"dial tcp: i/o timeout"}`)},
		"filex busy":                   {503, []byte(`{"error":"busy"}`)},
		"filex failing":                {500, []byte(`{"error":"database is locked"}`)},
		"a proxy in between":           {504, []byte(`gateway timeout`)},
	} {
		f := newFakeFilex(t, version050)
		f.answers["BRF-Tech/filex-sign@v0.3.0"] = rep
		if _, _, err := runner(f).DryRun(ctx, signReq()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	f := newFakeFilex(t, version050)
	f.srv.Close()
	if _, _, err := runner(f).DryRun(ctx, signReq()); err == nil {
		t.Error("a validator that is down must be an error, not a check")
	}
}

func TestSignIn_CachedRenewedAndRefusedWithASecondFactor(t *testing.T) {
	latest, _, r := pair(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, checks, err := r.DryRun(ctx, signReq()); err != nil || checks[0].Status != domain.CheckPassed {
			t.Fatalf("%v %s", err, summary(checks))
		}
	}
	if latest.logins.Load() != 1 {
		t.Errorf("signed in %d times for three dry runs", latest.logins.Load())
	}
	if latest.caps.Load() != 1 {
		t.Errorf("asked the version %d times within its TTL", latest.caps.Load())
	}
	latest.expireOnce.Store(true)
	if _, checks, err := r.DryRun(ctx, signReq()); err != nil || checks[0].Status != domain.CheckPassed {
		t.Fatalf("after the session ended: %v %s", err, summary(checks))
	}
	if latest.logins.Load() != 2 {
		t.Errorf("logins %d after a 401, want 2", latest.logins.Load())
	}
	f := newFakeFilex(t, version050)
	f.totp = true
	f.answers["BRF-Tech/filex-sign@v0.3.0"] = latest.answers["BRF-Tech/filex-sign@v0.3.0"]
	_, _, err := runner(f).DryRun(ctx, signReq())
	if err == nil || !strings.Contains(err.Error(), "second factor") {
		t.Errorf("TOTP: %v", err)
	}
	bad := New(Options{Validators: []Validator{{Label: "latest", BaseURL: latest.srv.URL}}, Email: seedEmail, Password: "wrong"})
	if _, _, err := bad.DryRun(ctx, signReq()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong password: %v", err)
	}
}

func TestDryRun_AtMostOneCallInFlightPerValidator(t *testing.T) {
	latest, floor, r := pair(t)
	latest.delay, floor.delay = 60*time.Millisecond, 60*time.Millisecond
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := r.DryRun(context.Background(), signReq()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if latest.maxIn.Load() != 1 || floor.maxIn.Load() != 1 || latest.calls.Load() != 4 {
		t.Errorf("max in flight latest %d floor %d, calls %d", latest.maxIn.Load(), floor.maxIn.Load(), latest.calls.Load())
	}
	// A caller that gives up while waiting for its turn is released.
	latest.delay = 300 * time.Millisecond
	go func() { _, _, _ = r.DryRun(context.Background(), signReq()) }()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := r.DryRun(ctx, signReq()); err == nil {
		t.Error("a cancelled wait must return its context's error")
	}
}

func TestDryRun_ADevelopmentBuildEnforcesNoRange(t *testing.T) {
	dev := newFakeFilex(t, "0.1.0-dev")
	dev.answers["BRF-Tech/filex-sign@v0.3.0"] = reply{200, recordedAnswer(t, "latest-sign030")}
	req := signReq()
	req.FilexRange = ">=9.0.0"
	results, checks, err := runner(dev).DryRun(context.Background(), req)
	if err != nil || summary(checks) != "latest=passed" || results[0].FilexVersion != "0.1.0-dev" {
		t.Errorf("%v %s %+v", err, summary(checks), results)
	}
}

func TestDryRun_TheBodyCanBeReplaced(t *testing.T) {
	f := newFakeFilex(t, version050)
	f.answers["fixture@v1"] = reply{200, recordedAnswer(t, "latest-sign030")}
	r := New(Options{Validators: []Validator{{Label: "latest", BaseURL: f.srv.URL}}, Email: seedEmail, Password: seedPassword,
		Body: func(q domain.DryRunRequest) any {
			return map[string]string{"github_repo": "fixture", "ref": "v1", "manifest_url": "http://127.0.0.1:1/filex-app.json"}
		}})
	if _, checks, err := r.DryRun(context.Background(), signReq()); err != nil || summary(checks) != "latest=passed" {
		t.Fatalf("%v %s", err, summary(checks))
	}
	if f.bodies[0]["manifest_url"] != "http://127.0.0.1:1/filex-app.json" {
		t.Errorf("body %v", f.bodies[0])
	}
}

func TestStatuses_ReachableAndDown(t *testing.T) {
	latest, floor := newFakeFilex(t, version050), newFakeFilex(t, version049)
	floor.srv.Close()
	st := runner(latest, floor).Statuses(context.Background())
	if len(st) != 2 || !st[0].Reachable || st[0].FilexVersion != version050 || st[0].Label != "latest" || st[0].BaseURL != latest.srv.URL {
		t.Errorf("latest %+v", st[0])
	}
	if st[1].Reachable || st[1].Error == "" || st[1].Label != "floor" {
		t.Errorf("floor %+v", st[1])
	}
	b, _ := json.Marshal(st[1])
	if !strings.Contains(string(b), `"reachable":false`) || !strings.Contains(string(b), `"base_url"`) {
		t.Errorf("wire %s (openapi ValidatorStatus)", b)
	}
}

func TestFixtureBody_TheURLFormOnTheValidatorsLoopback(t *testing.T) {
	body := FixtureBody("http://127.0.0.1:5299/")
	got := body(signReq()).(map[string]string)
	if got["manifest_url"] != "http://127.0.0.1:5299/BRF-Tech/filex-sign/v0.3.0/filex-app.json" ||
		got["url"] != "http://127.0.0.1:5299/BRF-Tech/filex-sign/v0.3.0/module" || len(got) != 2 {
		t.Errorf("module app %v (no sha256: the manifest pins the module, as in production)", got)
	}
	got = body(domain.DryRunRequest{Repo: "BRF-Tech/filex-lang-es", Ref: "v0.1.7", Kind: domain.KindLanguagePack, ExpectManifestSHA256: langEsMan}).(map[string]string)
	if got["url"] != "" || got["manifest_url"] != "http://127.0.0.1:5299/BRF-Tech/filex-lang-es/v0.1.7/filex-app.json" {
		t.Errorf("language pack %v (filex refuses a module address for a pack)", got)
	}
}
