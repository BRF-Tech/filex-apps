// Package validator implements pipeline.DryRunner: it asks real filex
// instances for their install review of a version (POST
// /api/admin/app-plugins?dry_run=1) and compares each answer with what the
// resolver measured (docs/ARCHITECTURE.md "The dry run").
//
// Measured on filex 0.50.0 and 0.49.0 (2026-10-03):
//
//   - Sign-in: POST /api/auth/login {"email","password"} answers {"user","token"};
//     the token works as `Authorization: Bearer`. The seeded admin has no
//     forced password change and no second factor. Sessions last 12 hours;
//     a 401 signs in again once.
//   - The dry run takes {"github_repo","ref"} and needs no `permissions`.
//   - An app WITH a module answers wasm_sha256 and NO manifest_sha256; an app
//     without one (interface-only, language pack) answers manifest_sha256,
//     and an interface its ui.sha256.
//   - The permission rows include ids filex derives from the `ui`,
//     `thumbnails`, `encrypted_folders` and `co_edit` blocks
//     (resolve.IsDerivedPermission); they are set aside before comparing
//     with the manifest's own list.
//   - engines_missing is a list of {id, name, kind}; it is reduced to ids.
//   - A filex outside the manifest's range does not always answer
//     compat.ok=false: 0.49.0 refused filex-convert 0.2.0 (">=0.50.0") as
//     manifest_invalid (an engine it does not know). So the range picks the
//     validators HERE (internal/filexrange), before any call.
//   - The version comes from anonymous GET /api/capabilities (`version`,
//     e.g. "v0.50.0 (9873e3e4…, 2026-10-03T00:50:59Z)").
//   - A dry run downloads but does not compile: 20 MB module 0.8-5.2 s.
package validator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filexrange"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/resolve"
)

// Codes the store writes itself (filex's own refusals are kept verbatim).
const (
	CodeOutOfRange          = "out_of_range"
	CodeNoValidatorInRange  = "no_validator_in_range"
	CodeKindMismatch        = "kind_mismatch"
	CodeIncompatible        = "incompatible"
	CodeManifestMismatch    = "manifest_sha256_mismatch"
	CodeSHA256Mismatch      = "sha256_mismatch"
	CodePermissionsMismatch = "permissions_mismatch"
	CodeAnswerIncomplete    = "answer_incomplete"
	CodeRangeInvalid        = "range_invalid"
)

// Validator is one filex the dry run is asked of.
type Validator struct {
	Label   string
	BaseURL string
}

// Options builds a Runner.
type Options struct {
	Validators []Validator
	Email      string
	Password   string
	// HTTP talks to the validators (the compose network: plain http, no
	// guard - these addresses are the store's own configuration, never a
	// publisher's). Default: a client with no overall timeout (CallTimeout
	// bounds each call).
	HTTP *http.Client
	// CallTimeout bounds one dry run (default 4 minutes).
	CallTimeout time.Duration
	// VersionTTL is how long a probed version is trusted (default 5 minutes).
	VersionTTL time.Duration
	// Body builds the install body. Default {"github_repo","ref"}; the
	// integration tests point filex at recorded fixtures with the URL form
	// instead (FILEX_PLUGIN_LOOPBACK_SOURCES=1 on test validators only).
	Body func(domain.DryRunRequest) any
	Now  func() time.Time
}

// Runner implements pipeline.DryRunner.
type Runner struct {
	vs      []*instance
	email   string
	pw      string
	hc      *http.Client
	timeout time.Duration
	ttl     time.Duration
	body    func(domain.DryRunRequest) any
	now     func() time.Time
}

var _ pipeline.DryRunner = (*Runner)(nil)

type instance struct {
	Validator
	// sem: at most one dry run in flight per validator (a large module
	// downloads, and on an install compiles, for tens of seconds).
	sem chan struct{}

	mu        sync.Mutex
	token     string
	version   string
	versionAt time.Time
}

// New builds a Runner.
func New(o Options) *Runner {
	r := &Runner{email: o.Email, pw: o.Password, hc: o.HTTP, timeout: o.CallTimeout, ttl: o.VersionTTL, body: o.Body, now: o.Now}
	if r.hc == nil {
		r.hc = &http.Client{Transport: &http.Transport{Proxy: nil, ResponseHeaderTimeout: 5 * time.Minute}}
	}
	if r.timeout <= 0 {
		r.timeout = 4 * time.Minute
	}
	if r.ttl <= 0 {
		r.ttl = 5 * time.Minute
	}
	if r.body == nil {
		r.body = GitHubBody
	}
	if r.now == nil {
		r.now = time.Now
	}
	for _, v := range o.Validators {
		r.vs = append(r.vs, &instance{Validator: Validator{Label: v.Label, BaseURL: strings.TrimRight(v.BaseURL, "/")}, sem: make(chan struct{}, 1)})
	}
	return r
}

// GitHubBody is the production install body: filex fetches the manifest and
// artifacts from GitHub itself.
func GitHubBody(req domain.DryRunRequest) any {
	return map[string]string{"github_repo": req.Repo, "ref": req.Ref}
}

// Status is one validator's health for GET /api/v1/admin/validators
// (openapi ValidatorStatus).
type Status struct {
	Label        string `json:"label"`
	BaseURL      string `json:"base_url"`
	Reachable    bool   `json:"reachable"`
	FilexVersion string `json:"filex_version,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Statuses probes every validator (its version, anonymously).
func (r *Runner) Statuses(ctx context.Context) []Status {
	out := make([]Status, len(r.vs))
	var wg sync.WaitGroup
	for i, in := range r.vs {
		wg.Add(1)
		go func(i int, in *instance) {
			defer wg.Done()
			st := Status{Label: in.Label, BaseURL: in.BaseURL}
			v, err := r.probe(ctx, in, true)
			if err != nil {
				st.Error = err.Error()
			} else {
				st.Reachable, st.FilexVersion = true, v
			}
			out[i] = st
		}(i, in)
	}
	wg.Wait()
	return out
}

// probe answers the validator's reported version, cached for VersionTTL
// unless fresh is set.
func (r *Runner) probe(ctx context.Context, in *instance, fresh bool) (string, error) {
	in.mu.Lock()
	if !fresh && in.version != "" && r.now().Sub(in.versionAt) < r.ttl {
		v := in.version
		in.mu.Unlock()
		return v, nil
	}
	in.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.BaseURL+"/api/capabilities", nil)
	if err != nil {
		return "", err
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("validator %s unreachable: %w", in.Label, err)
	}
	defer resp.Body.Close()
	var caps struct {
		Version string `json:"version"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &caps) != nil || strings.TrimSpace(caps.Version) == "" {
		return "", fmt.Errorf("validator %s: capabilities answered %d without a version", in.Label, resp.StatusCode)
	}
	in.mu.Lock()
	in.version, in.versionAt = caps.Version, r.now()
	in.mu.Unlock()
	return caps.Version, nil
}

// DryRun asks every validator whose filex the range admits. One check per
// validator; err is for a failure worth retrying (a validator down, a
// timeout), never for a version that fails.
func (r *Runner) DryRun(ctx context.Context, req domain.DryRunRequest) ([]domain.DryRunResult, []domain.CheckResult, error) {
	start := r.now().UTC()
	rng, err := filexrange.Parse(req.FilexRange)
	if err != nil {
		end := r.now().UTC()
		return nil, []domain.CheckResult{{Kind: domain.CheckDryRun, Status: domain.CheckFailed, Code: CodeRangeInvalid,
			Message: "the filex range does not parse: " + err.Error(), StartedAt: start, EndedAt: &end}}, nil
	}
	type slot struct {
		res   *domain.DryRunResult
		check domain.CheckResult
		err   error
	}
	slots := make([]slot, len(r.vs))
	var wg sync.WaitGroup
	for i, in := range r.vs {
		wg.Add(1)
		go func(i int, in *instance) {
			defer wg.Done()
			res, check, err := r.one(ctx, in, rng, req)
			slots[i] = slot{res, check, err}
		}(i, in)
	}
	wg.Wait()
	var results []domain.DryRunResult
	var checks []domain.CheckResult
	ran := 0
	for _, s := range slots {
		if s.err != nil {
			return nil, nil, s.err
		}
		checks = append(checks, s.check)
		if s.res != nil {
			results = append(results, *s.res)
			ran++
		}
	}
	if ran == 0 {
		end := r.now().UTC()
		var vers []string
		for _, c := range checks {
			var d struct {
				FilexVersion string `json:"filex_version"`
			}
			_ = json.Unmarshal(c.Data, &d)
			vers = append(vers, c.Validator+" "+d.FilexVersion)
		}
		checks = append(checks, domain.CheckResult{Kind: domain.CheckDryRun, Status: domain.CheckFailed, Code: CodeNoValidatorInRange,
			Message:   fmt.Sprintf("the manifest's range %q admits none of the store's filex versions (%s)", rng.String(), strings.Join(vers, ", ")),
			StartedAt: start, EndedAt: &end})
	}
	return results, checks, nil
}

func (r *Runner) one(ctx context.Context, in *instance, rng filexrange.Range, req domain.DryRunRequest) (*domain.DryRunResult, domain.CheckResult, error) {
	start := r.now().UTC()
	check := domain.CheckResult{Kind: domain.CheckDryRun, Validator: in.Label, StartedAt: start}
	finish := func(status domain.CheckStatus, code, msg string, data any) domain.CheckResult {
		end := r.now().UTC()
		check.Status, check.Code, check.Message, check.EndedAt = status, code, msg, &end
		if data != nil {
			if b, err := json.Marshal(data); err == nil {
				check.Data = b
			}
		}
		return check
	}
	reported, err := r.probe(ctx, in, false)
	if err != nil {
		return nil, check, err
	}
	host, enforced := filexrange.HostRelease(reported)
	version := strings.TrimPrefix(strings.Fields(reported)[0], "v")
	if enforced {
		version = host.Plain()
	}
	if enforced && !rng.Admits(host) {
		return nil, finish(domain.CheckSkipped, CodeOutOfRange,
			fmt.Sprintf("filex %s is outside the manifest's range %q", version, rng.String()),
			map[string]any{"filex_version": version}), nil
	}
	select {
	case in.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, check, ctx.Err()
	}
	defer func() { <-in.sem }()

	status, body, err := r.call(ctx, in, req)
	if err != nil {
		return nil, check, err
	}
	res := &domain.DryRunResult{Validator: in.Label, FilexVersion: version, Raw: json.RawMessage(body), Permissions: []domain.PermissionRow{}}
	if !json.Valid(body) {
		res.Raw = nil
	}
	if status != http.StatusOK {
		var refusal struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &refusal)
		res.Code, res.Message, res.Kind = refusal.Error, refusal.Message, req.Kind
		if res.Code == "" {
			res.Code = fmt.Sprintf("http_%d", status)
		}
		if res.Message == "" {
			res.Message = fmt.Sprintf("filex %s refused the install review (%d)", version, status)
		}
		return res, finish(domain.CheckFailed, res.Code, "filex "+version+": "+res.Message, map[string]any{"filex_version": version, "status": status}), nil
	}
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		res.Code, res.Message, res.Kind = CodeAnswerIncomplete, "unreadable answer: "+err.Error(), req.Kind
		return res, finish(domain.CheckFailed, res.Code, "filex "+version+": "+res.Message, map[string]any{"filex_version": version}), nil
	}
	a.fill(res)
	problems := compare(req, &a)
	data := map[string]any{"filex_version": version, "permissions": len(res.Permissions)}
	if a.Compat != nil {
		data["compat"] = a.Compat
	}
	if len(problems) > 0 {
		res.Code, res.Message = problems[0].code, joinProblems(problems)
		data["problems"] = problems
		return res, finish(domain.CheckFailed, res.Code, "filex "+version+": "+res.Message, data), nil
	}
	res.OK = true
	return res, finish(domain.CheckPassed, "", "filex "+version+" reviewed the version as the store resolved it", data), nil
}

// call signs in when needed and posts the dry run; a 401 signs in again
// once. A non-2xx answer from filex comes back as (status, body, nil) unless
// it is worth retrying (filex busy or failing, GitHub unreachable from it).
func (r *Runner) call(ctx context.Context, in *instance, req domain.DryRunRequest) (int, []byte, error) {
	payload, err := json.Marshal(r.body(req))
	if err != nil {
		return 0, nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := r.signIn(ctx, in, attempt > 0)
		if err != nil {
			return 0, nil, err
		}
		cctx, cancel := context.WithTimeout(ctx, r.timeout)
		hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, in.BaseURL+"/api/admin/app-plugins?dry_run=1", bytes.NewReader(payload))
		if err != nil {
			cancel()
			return 0, nil, err
		}
		hreq.Header.Set("Content-Type", "application/json")
		hreq.Header.Set("Accept", "application/json")
		hreq.Header.Set("Accept-Language", "en")
		hreq.Header.Set("Authorization", "Bearer "+tok)
		resp, err := r.hc.Do(hreq)
		if err != nil {
			cancel()
			return 0, nil, fmt.Errorf("validator %s: %w", in.Label, err)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		cancel()
		if rerr != nil {
			return 0, nil, fmt.Errorf("validator %s: reading the answer: %w", in.Label, rerr)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if retryableAnswer(resp.StatusCode, body) {
			return 0, nil, fmt.Errorf("validator %s answered %d: %s", in.Label, resp.StatusCode, snippet(body))
		}
		return resp.StatusCode, body, nil
	}
	return 0, nil, fmt.Errorf("validator %s: the seeded account is refused after signing in again", in.Label)
}

// retryableAnswer: filex busy or failing, the seeded account refused, or
// filex could not reach GitHub - not a judgement of the version.
func retryableAnswer(status int, body []byte) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusServiceUnavailable:
		return true
	}
	var e struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Error == "fetch_failed" {
		return e.Reason == "unreachable"
	}
	return status >= 500 && e.Error == ""
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// signIn returns the cached session token, or signs in (again, with again).
func (r *Runner) signIn(ctx context.Context, in *instance, again bool) (string, error) {
	in.mu.Lock()
	if in.token != "" && !again {
		t := in.token
		in.mu.Unlock()
		return t, nil
	}
	in.token = ""
	in.mu.Unlock()
	body, _ := json.Marshal(map[string]string{"email": r.email, "password": r.pw})
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, in.BaseURL+"/api/auth/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("validator %s: sign-in: %w", in.Label, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &out) != nil || out.Token == "" {
		var e struct {
			Error        string `json:"error"`
			TOTPRequired bool   `json:"totp_required"`
		}
		_ = json.Unmarshal(b, &e)
		if e.TOTPRequired {
			return "", fmt.Errorf("validator %s: the seeded account asks for a second factor; a validator's account must not have one", in.Label)
		}
		return "", fmt.Errorf("validator %s: sign-in answered %d %s", in.Label, resp.StatusCode, e.Error)
	}
	in.mu.Lock()
	in.token = out.Token
	in.mu.Unlock()
	return out.Token, nil
}

// answer is filex's DryRunAnswer, the part the store reads.
type answer struct {
	Permissions []struct {
		ID     string      `json:"id"`
		Label  string      `json:"label"`
		Reason domain.Text `json:"reason"`
	} `json:"permissions"`
	WasmSHA256     string `json:"wasm_sha256"`
	Kind           string `json:"kind"`
	ManifestSHA256 string `json:"manifest_sha256"`
	Languages      []struct {
		Code    string `json:"code"`
		Keys    int    `json:"keys"`
		Total   int    `json:"total"`
		Percent int    `json:"percent"`
	} `json:"languages"`
	EnginesMissing []struct {
		ID string `json:"id"`
	} `json:"engines_missing"`
	Compat *struct {
		Requires string `json:"requires"`
		OK       bool   `json:"ok"`
		Filex    string `json:"filex"`
	} `json:"compat"`
	UI *struct {
		SHA256 string `json:"sha256"`
	} `json:"ui"`
}

func (a *answer) fill(res *domain.DryRunResult) {
	res.Kind = domain.AppKind(a.Kind)
	res.ManifestSHA256, res.WasmSHA256 = strings.ToLower(a.ManifestSHA256), strings.ToLower(a.WasmSHA256)
	if a.UI != nil {
		res.UISHA256 = strings.ToLower(a.UI.SHA256)
	}
	if a.Compat != nil {
		ok := a.Compat.OK
		res.CompatOK = &ok
	}
	for _, p := range a.Permissions {
		res.Permissions = append(res.Permissions, domain.PermissionRow{ID: p.ID, Label: p.Label, Reason: p.Reason})
	}
	for _, e := range a.EnginesMissing {
		res.EnginesMissing = append(res.EnginesMissing, e.ID)
	}
	for _, l := range a.Languages {
		res.Languages = append(res.Languages, domain.LanguageCoverage{Code: l.Code, Percent: l.Percent, Keys: l.Keys, Total: l.Total})
	}
}

type problem struct {
	code string
	msg  string
}

func (p problem) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"code": p.code, "message": p.msg})
}

func joinProblems(ps []problem) string {
	msgs := make([]string, len(ps))
	for i, p := range ps {
		msgs[i] = p.msg
	}
	return strings.Join(msgs, "; ")
}

// compare holds one answer to the resolver's pins.
func compare(req domain.DryRunRequest, a *answer) []problem {
	var ps []problem
	add := func(code, f string, args ...any) { ps = append(ps, problem{code, fmt.Sprintf(f, args...)}) }
	if domain.AppKind(a.Kind) != req.Kind {
		add(CodeKindMismatch, "filex reads it as %q, the store resolved %q", a.Kind, req.Kind)
	}
	if a.Compat != nil && !a.Compat.OK {
		add(CodeIncompatible, "filex %s says the range %q leaves it out", a.Compat.Filex, a.Compat.Requires)
	}
	wantWasm, gotWasm := strings.ToLower(req.ExpectWasmSHA256), strings.ToLower(a.WasmSHA256)
	if gotWasm != wantWasm {
		add(CodeSHA256Mismatch, "module sha256: filex %q, the store %q", gotWasm, wantWasm)
	}
	gotUI := ""
	if a.UI != nil {
		gotUI = strings.ToLower(a.UI.SHA256)
	}
	if want := strings.ToLower(req.ExpectUISHA256); gotUI != want {
		add(CodeSHA256Mismatch, "interface bundle sha256: filex %q, the store %q", gotUI, want)
	}
	gotMan := strings.ToLower(a.ManifestSHA256)
	switch {
	case gotMan != "" && req.ExpectManifestSHA256 != "" && gotMan != strings.ToLower(req.ExpectManifestSHA256):
		add(CodeManifestMismatch, "manifest sha256: filex %q, the store %q", gotMan, strings.ToLower(req.ExpectManifestSHA256))
	case gotMan == "" && gotWasm == "":
		add(CodeAnswerIncomplete, "filex named neither a module nor a manifest sha256")
	}
	declared := map[string]bool{}
	for _, p := range a.Permissions {
		if id := resolve.NormalizePermissions([]string{p.ID}); len(id) == 1 && !resolve.IsDerivedPermission(id[0]) {
			declared[id[0]] = true
		}
	}
	want := map[string]bool{}
	for _, id := range resolve.NormalizePermissions(req.ExpectPermissions) {
		want[id] = true
	}
	var added, missing []string
	for id := range declared {
		if !want[id] {
			added = append(added, id)
		}
	}
	for id := range want {
		if !declared[id] {
			missing = append(missing, id)
		}
	}
	if len(added)+len(missing) > 0 {
		sort.Strings(added)
		sort.Strings(missing)
		add(CodePermissionsMismatch, "permissions differ from the manifest the store read: filex adds %v, lacks %v", added, missing)
	}
	return ps
}
