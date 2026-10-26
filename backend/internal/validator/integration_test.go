//go:build integration

// Integration tests against REAL filex validators (ghcr.io/brf-tech/filex
// v0.56.0 and v0.55.0), run on a test machine by
// scripts/xcheck/validators-it.sh, which starts both and sets:
//
//	FAPPS_IT_VALIDATORS=latest=http://127.0.0.1:5212,floor=http://127.0.0.1:5213
//	FAPPS_IT_EMAIL / FAPPS_IT_PASSWORD   the validators' seeded admin
//	FAPPS_IT_FIXTURES                    the recorded contents (<dir>/<sha256>)
//	go test -tags integration ./internal/validator/
//
// Every app here is a REAL tag (testdata/fixtures.json): its manifest and
// module are the bytes GitHub served, kept in a cache on that machine and
// checked against the recorded sha256, so every hash is the production one.
// FAPPS_IT_RECORD=1 fills a missing cache file from GitHub (raw and release
// downloads, no API call); without it a missing file fails the test.
//
// Two sides, one recording:
//   - The store's resolver reads GitHub's own addresses (api.github.com,
//     raw.githubusercontent.com, github.com releases -> release-assets),
//     answered by a local TLS server: safehttp's development-only test modes
//     (a CA file, private addresses, a host map) and nothing else differ
//     from production.
//   - The validators share the test's network namespace and read the same
//     bytes from a loopback server through validator.FixtureBody
//     (FILEX_PLUGIN_LOOPBACK_SOURCES=1 on these throw-away validators only).
//
// Interface-only real apps name their bundle at a GitHub release address
// that filex fetches itself: they are covered by TestLive_* (real GitHub,
// FAPPS_IT_LIVE_GITHUB=1, started by hand) and by the end-to-end stack.
package validator_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filexrange"
	"github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/resolve"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
	"github.com/brf-tech/filex-apps/backend/internal/validator"
)

func itValidators(t *testing.T) []validator.Validator {
	t.Helper()
	spec := os.Getenv("FAPPS_IT_VALIDATORS")
	if spec == "" {
		t.Skip("FAPPS_IT_VALIDATORS is not set: no real validators to ask")
	}
	var vs []validator.Validator
	for _, part := range strings.Split(spec, ",") {
		label, base, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			t.Fatalf("FAPPS_IT_VALIDATORS: %q", part)
		}
		vs = append(vs, validator.Validator{Label: label, BaseURL: base})
	}
	return vs
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// --- the recording ---------------------------------------------------------

type recordedFile struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type recordedRepo struct {
	Repo      string        `json:"repo"`
	Tag       string        `json:"tag"`
	Commit    string        `json:"commit"`
	RepoID    int64         `json:"repo_id"`
	OwnerID   int64         `json:"owner_id"`
	OwnerType string        `json:"owner_type"`
	License   string        `json:"license"`
	Manifest  recordedFile  `json:"manifest"`
	Module    *recordedFile `json:"module"`

	manifest, module []byte
}

// loadRecording reads testdata/fixtures.json and the cached contents,
// recording a missing one with FAPPS_IT_RECORD=1 through the production
// guard (default allow list, public addresses only).
func loadRecording(t *testing.T) map[string]*recordedRepo {
	t.Helper()
	dir := os.Getenv("FAPPS_IT_FIXTURES")
	if dir == "" {
		t.Skip("FAPPS_IT_FIXTURES is not set: no recorded contents")
	}
	b, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Repos []*recordedRepo `json:"repos"`
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	var live *safehttp.Client
	read := func(f recordedFile) []byte {
		p := filepath.Join(dir, f.SHA256)
		if b, err := os.ReadFile(p); err == nil && hexSum(b) == f.SHA256 && int64(len(b)) == f.Size {
			return b
		}
		if os.Getenv("FAPPS_IT_RECORD") != "1" {
			t.Fatalf("%s is not in %s (run once with FAPPS_IT_RECORD=1 to record it from %s)", f.SHA256, dir, f.URL)
		}
		if live == nil {
			live = safehttp.New(safehttp.Config{AllowHosts: config.DefaultAllowHosts, UserAgent: "fapps-fixture-recorder"})
		}
		got, err := live.Get(context.Background(), f.URL, 64<<20, nil)
		if err != nil {
			t.Fatalf("record %s: %v", f.URL, err)
		}
		if got.SHA256 != f.SHA256 || int64(len(got.Bytes)) != f.Size {
			t.Fatalf("record %s: GitHub serves %s (%d bytes), the recording says %s (%d)", f.URL, got.SHA256, len(got.Bytes), f.SHA256, f.Size)
		}
		if err := os.WriteFile(p, got.Bytes, 0o644); err != nil {
			t.Fatal(err)
		}
		return got.Bytes
	}
	out := map[string]*recordedRepo{}
	for _, r := range idx.Repos {
		r.manifest = read(r.Manifest)
		if r.Module != nil {
			r.module = read(*r.Module)
		}
		out[r.Repo] = r
	}
	return out
}

// --- GitHub, as the store reads it ------------------------------------------

var githubHosts = []string{"api.github.com", "github.com", "raw.githubusercontent.com", "release-assets.githubusercontent.com"}

// testCA makes a throw-away CA and a certificate for GitHub's host names.
func testCA(t *testing.T) (caPEM []byte, leaf tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fapps integration CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "github.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		DNSNames: githubHosts, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	leaf, err = tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), leaf
}

const consentDoc = `{"publisher":"brf-tech","summary":{"en":"A real app, listed by the integration test."}}`

// world serves the recording twice: to the resolver under GitHub's names
// (TLS), to the validators on loopback (FixtureBody's layout).
type world struct {
	repos map[string]*recordedRepo
	gh    *httptest.Server
	plain *httptest.Server
	caPEM []byte

	mu sync.Mutex
	// filexSees replaces what the validators are served after the
	// resolver read the original: "<owner>/<name>/<tag>/<file>" -> bytes.
	filexSees map[string][]byte
}

func newWorld(t *testing.T) *world {
	w := &world{repos: loadRecording(t), filexSees: map[string][]byte{}}
	var leaf tls.Certificate
	w.caPEM, leaf = testCA(t)
	w.gh = httptest.NewUnstartedServer(http.HandlerFunc(w.serveGitHub))
	w.gh.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	w.gh.StartTLS()
	w.plain = httptest.NewServer(http.HandlerFunc(w.serveFilex))
	t.Cleanup(w.gh.Close)
	t.Cleanup(w.plain.Close)
	return w
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}

func (w *world) serveGitHub(rw http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch host {
	case "api.github.com":
		if len(parts) < 3 || parts[0] != "repos" {
			break
		}
		rec := w.repos[parts[1]+"/"+parts[2]]
		if rec == nil {
			break
		}
		rest := strings.Join(parts[3:], "/")
		switch {
		case rest == "":
			writeJSON(rw, map[string]any{"id": rec.RepoID, "name": parts[2], "full_name": rec.Repo, "private": false, "archived": false,
				"visibility": "public", "owner": map[string]any{"id": rec.OwnerID, "login": parts[1], "type": rec.OwnerType},
				"license": map[string]any{"spdx_id": rec.License}})
			return
		case rest == "git/ref/tags/"+rec.Tag:
			writeJSON(rw, map[string]any{"ref": "refs/tags/" + rec.Tag, "object": map[string]string{"sha": rec.Commit, "type": "commit"}})
			return
		case rest == "license" && r.URL.Query().Get("ref") == rec.Commit:
			writeJSON(rw, map[string]any{"path": "LICENSE", "license": map[string]any{"spdx_id": rec.License}})
			return
		}
	case "raw.githubusercontent.com":
		if len(parts) == 4 {
			if rec := w.repos[parts[0]+"/"+parts[1]]; rec != nil && parts[2] == rec.Commit {
				switch parts[3] {
				case "filex-app.json":
					_, _ = rw.Write(rec.manifest)
					return
				case ".filex-apps.json":
					_, _ = rw.Write([]byte(consentDoc))
					return
				}
			}
		}
	case "github.com":
		if len(parts) == 6 && parts[2] == "releases" && parts[3] == "download" {
			if rec := w.repos[parts[0]+"/"+parts[1]]; rec != nil && rec.Module != nil && "https://github.com"+r.URL.Path == rec.Module.URL {
				http.Redirect(rw, r, fmt.Sprintf("https://release-assets.githubusercontent.com/github-production-release-asset/%d/%s?X-Amz-Signature=recorded", rec.RepoID, rec.Module.SHA256), http.StatusFound)
				return
			}
		}
	case "release-assets.githubusercontent.com":
		if len(parts) == 3 {
			for _, rec := range w.repos {
				if rec.Module != nil && parts[2] == rec.Module.SHA256 {
					_, _ = rw.Write(rec.module)
					return
				}
			}
		}
	}
	http.NotFound(rw, r)
}

// serveFilex is FixtureBody's layout: /<owner>/<name>/<tag>/filex-app.json
// and /<owner>/<name>/<tag>/module.
func (w *world) serveFilex(rw http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	w.mu.Lock()
	b, ok := w.filexSees[p]
	w.mu.Unlock()
	if ok {
		_, _ = rw.Write(b)
		return
	}
	parts := strings.Split(p, "/")
	if len(parts) == 4 {
		if rec := w.repos[parts[0]+"/"+parts[1]]; rec != nil && parts[2] == rec.Tag {
			switch {
			case parts[3] == "filex-app.json":
				_, _ = rw.Write(rec.manifest)
				return
			case parts[3] == "module" && rec.module != nil:
				_, _ = rw.Write(rec.module)
				return
			}
		}
	}
	http.NotFound(rw, r)
}

// resolver reads GitHub's own addresses, answered by the local TLS server:
// the production allow list and clients, plus safehttp's development-only
// test modes (the CA file, private addresses, the host map).
func (w *world) resolver(t *testing.T) *resolve.Resolver {
	t.Helper()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, w.caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	local := strings.TrimPrefix(w.gh.URL, "https://")
	hm := map[string]string{}
	for _, h := range githubHosts {
		hm[h+":443"] = local
	}
	hc, err := safehttp.Open(safehttp.Config{Env: safehttp.DevelopmentEnv, AllowHosts: config.DefaultAllowHosts,
		CAFile: caFile, AllowPrivate: true, HostMap: hm, UserAgent: "fapps-integration-test"})
	if err != nil {
		t.Fatal(err)
	}
	return resolve.New(resolve.Options{GitHub: github.New(github.Options{HTTP: hc})})
}

func (w *world) runner(t *testing.T) *validator.Runner {
	return validator.New(validator.Options{Validators: itValidators(t), Email: os.Getenv("FAPPS_IT_EMAIL"),
		Password: os.Getenv("FAPPS_IT_PASSWORD"), Body: validator.FixtureBody(w.plain.URL)})
}

func dryRunRequest(res *domain.Resolution) domain.DryRunRequest {
	q := domain.DryRunRequest{Repo: res.RepoOwner + "/" + res.RepoName, Ref: res.Ref, Kind: res.Kind, FilexRange: res.FilexRange,
		ExpectManifestSHA256: res.ManifestSHA256, ExpectPermissions: res.Permissions}
	if res.Wasm != nil {
		q.ExpectWasmSHA256 = res.Wasm.SHA256
	}
	if res.UI != nil {
		q.ExpectUISHA256 = res.UI.SHA256
	}
	return q
}

func checkSummary(checks []domain.CheckResult) string {
	var parts []string
	for _, c := range checks {
		s := string(c.Kind)
		if c.Validator != "" {
			s += "[" + c.Validator + "]"
		}
		s += "=" + string(c.Status)
		if c.Code != "" {
			s += "(" + c.Code + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func (w *world) resolve(t *testing.T, repo string) *domain.Resolution {
	t.Helper()
	rec := w.repos[repo]
	res, checks, err := w.resolver(t).Resolve(context.Background(), domain.ResolveRequest{Repo: repo, Ref: rec.Tag, Publisher: "brf-tech"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Status == domain.CheckFailed {
			t.Fatalf("resolve %s@%s: %s: %s", repo, rec.Tag, checkSummary(checks), c.Message)
		}
	}
	// The store measured GitHub's own bytes.
	if res.ManifestSHA256 != rec.Manifest.SHA256 || res.CommitSHA != rec.Commit || res.License != rec.License {
		t.Fatalf("resolution %s %s %s, recording %s %s %s", res.ManifestSHA256, res.CommitSHA, res.License, rec.Manifest.SHA256, rec.Commit, rec.License)
	}
	if rec.Module != nil && (res.Wasm == nil || res.Wasm.SHA256 != rec.Module.SHA256) {
		t.Fatalf("module %+v, recording %s", res.Wasm, rec.Module.SHA256)
	}
	return res
}

// filex-sign v0.3.0 (a module app) and filex-lang-es (a language pack):
// the store resolves GitHub's bytes, both real filex review the same bytes,
// and every pin agrees.
func TestIntegration_RealTagsPassBothValidators(t *testing.T) {
	w := newWorld(t)
	r := w.runner(t)
	for _, repo := range []string{"BRF-Tech/filex-sign", "BRF-Tech/filex-lang-es"} {
		res := w.resolve(t, repo)
		start := time.Now()
		results, checks, err := r.DryRun(context.Background(), dryRunRequest(res))
		if err != nil {
			t.Fatalf("%s: %v", repo, err)
		}
		t.Logf("%s@%s: %s in %s", repo, res.Ref, checkSummary(checks), time.Since(start).Round(time.Millisecond))
		if checkSummary(checks) != "dry_run[latest]=passed dry_run[floor]=passed" {
			for _, rr := range results {
				t.Logf("  %s: %s %s", rr.Validator, rr.Code, rr.Message)
			}
			t.Fatalf("%s: %s", repo, checkSummary(checks))
		}
		for _, rr := range results {
			switch res.Kind {
			case domain.KindLanguagePack:
				if rr.ManifestSHA256 != res.ManifestSHA256 || len(rr.Languages) != 1 || rr.Languages[0].Code != "es" {
					t.Errorf("%s %s: %+v", repo, rr.Validator, rr)
					continue
				}
				t.Logf("  %s: es %d%% of %d", rr.Validator, rr.Languages[0].Percent, rr.Languages[0].Total)
			default:
				if rr.WasmSHA256 != res.Wasm.SHA256 || len(rr.Permissions) != len(res.Permissions) {
					t.Errorf("%s %s: %+v", repo, rr.Validator, rr)
				}
			}
		}
	}
}

// filex-convert v0.2.0 says filex ">=0.50.0" and min_filex "0.50.0": the
// index carries ">=0.50.0" once, and each validator is asked as that range
// and its own version decide. Up to filex 0.50 the floor sat below it and
// was skipped; since the validators follow the releases (0.56.0 and 0.55.0
// on 2026-10-11) the real range admits both. No recorded real tag leaves the
// floor out any more: the ones that do, filex-office-editor v0.2.0 and later
// (">=0.56.0", the release whose manifest blocks they use), are
// interface-only, and filex fetches their bundle from GitHub itself
// (TestLive_*). So the skip
// is then asked on the same real bytes with the range such an app declares,
// the latest validator's own minor: only the latest filex is asked.
func TestIntegration_TheRangeSkipsTheFloor(t *testing.T) {
	w := newWorld(t)
	res := w.resolve(t, "BRF-Tech/filex-convert")
	if res.FilexRange != ">=0.50.0" {
		t.Errorf("FilexRange %q, want the normal form >=0.50.0", res.FilexRange)
	}
	vers := itVersions(t)
	latest, floor := vers["latest"], vers["floor"]
	if latest.Major != floor.Major || latest.Minor <= floor.Minor {
		t.Fatalf("latest %s is not a newer minor than floor %s (docs/ARCHITECTURE.md Q7)", latest.Raw, floor.Raw)
	}
	r := w.runner(t)

	rng, err := filexrange.Parse(res.FilexRange)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, label := range []string{"latest", "floor"} {
		if rng.Admits(vers[label]) {
			want = append(want, "dry_run["+label+"]=passed")
		} else {
			want = append(want, "dry_run["+label+"]=skipped(out_of_range)")
		}
	}
	results, checks, err := r.DryRun(context.Background(), dryRunRequest(res))
	if err != nil {
		t.Fatal(err)
	}
	if got := checkSummary(checks); got != strings.Join(want, " ") {
		t.Fatalf("the real range %s on latest %s, floor %s: %s", res.FilexRange, latest.Raw, floor.Raw, got)
	}
	if strings.Join(results[0].EnginesMissing, ",") != "office" {
		t.Errorf("engines_missing %v", results[0].EnginesMissing)
	}

	q := dryRunRequest(res)
	q.FilexRange = fmt.Sprintf(">=%d.%d.0", latest.Major, latest.Minor)
	results, checks, err = r.DryRun(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkSummary(checks); got != "dry_run[latest]=passed dry_run[floor]=skipped(out_of_range)" {
		t.Fatalf("range %s: %s", q.FilexRange, got)
	}
	if len(results) != 1 || results[0].Validator != "latest" {
		t.Fatalf("only the latest filex is asked: %+v", results)
	}
}

// itVersions: each validator's filex release, as it reports it (a release,
// so filex enforces ranges on it).
func itVersions(t *testing.T) map[string]filexrange.Version {
	t.Helper()
	out := map[string]filexrange.Version{}
	for _, st := range validator.New(validator.Options{Validators: itValidators(t)}).Statuses(context.Background()) {
		v, enforced := filexrange.HostRelease(st.FilexVersion)
		if !st.Reachable || !enforced {
			t.Fatalf("validator %s: %+v is not a reachable filex release", st.Label, st)
		}
		out[st.Label] = v
	}
	return out
}

// After the store read the tag, the bytes filex is served change: the
// review fails, never passes on the new bytes.
func TestIntegration_BytesThatMovedAfterResolvingFail(t *testing.T) {
	w := newWorld(t)
	r := w.runner(t)
	rec := w.repos["BRF-Tech/filex-sign"]
	res := w.resolve(t, "BRF-Tech/filex-sign")
	at := "BRF-Tech/filex-sign/" + rec.Tag + "/"

	swapped := append([]byte{}, rec.module...)
	swapped[len(swapped)-1] ^= 0xff
	w.mu.Lock()
	w.filexSees[at+"module"] = swapped
	w.mu.Unlock()
	_, checks, err := r.DryRun(context.Background(), dryRunRequest(res))
	if err != nil {
		t.Fatal(err)
	}
	if got := checkSummary(checks); got != "dry_run[latest]=failed(sha256_mismatch) dry_run[floor]=failed(sha256_mismatch)" {
		t.Errorf("a swapped module (filex's own refusal: the manifest pins it): %s", got)
	}

	var m map[string]any
	if err := json.Unmarshal(rec.manifest, &m); err != nil {
		t.Fatal(err)
	}
	m["permissions"] = append(m["permissions"].([]any), "http:example.org")
	moved, _ := json.Marshal(m)
	w.mu.Lock()
	delete(w.filexSees, at+"module")
	w.filexSees[at+"filex-app.json"] = moved
	w.mu.Unlock()
	_, checks, err = r.DryRun(context.Background(), dryRunRequest(res))
	if err != nil {
		t.Fatal(err)
	}
	if got := checkSummary(checks); got != "dry_run[latest]=failed(permissions_mismatch) dry_run[floor]=failed(permissions_mismatch)" {
		t.Errorf("a permission added after resolving: %s", got)
	}
}

func TestIntegration_ValidatorStatuses(t *testing.T) {
	for _, st := range validator.New(validator.Options{Validators: itValidators(t)}).Statuses(context.Background()) {
		if !st.Reachable || !strings.HasPrefix(st.FilexVersion, "v0.") {
			t.Errorf("%+v", st)
		}
		t.Logf("%s %s %s", st.Label, st.BaseURL, st.FilexVersion)
	}
}

// --- the production path, against real GitHub (FAPPS_IT_LIVE_GITHUB=1) -----

func liveGitHub(t *testing.T) *github.Client {
	if os.Getenv("FAPPS_IT_LIVE_GITHUB") != "1" {
		t.Skip("FAPPS_IT_LIVE_GITHUB=1 asks real GitHub (anonymous); started by hand")
	}
	hc := safehttp.New(safehttp.Config{AllowHosts: config.DefaultAllowHosts, UserAgent: "fapps-integration-test"})
	return github.New(github.Options{HTTP: hc})
}

// The production guard (default allow list, public addresses only) and the
// production body: what the resolver measures of BRF-Tech/filex-drawio
// v31.5.3 (interface only) and filex-sign v0.3.0 equals what both filex
// answer.
func TestLive_ResolverAndFilexAgreeOnRealRepositories(t *testing.T) {
	gh := liveGitHub(t)
	r := validator.New(validator.Options{Validators: itValidators(t), Email: os.Getenv("FAPPS_IT_EMAIL"), Password: os.Getenv("FAPPS_IT_PASSWORD")})
	ctx := context.Background()
	res, checks, err := resolve.New(resolve.Options{GitHub: gh}).Resolve(ctx, domain.ResolveRequest{Repo: "BRF-Tech/filex-drawio", Ref: "v31.5.3", Publisher: "brf-tech"})
	if err != nil {
		t.Fatal(err)
	}
	// The real repository has no consent file yet.
	if got := checkSummary(checks); got != "repo_access=passed tag=passed manifest=passed consent=failed(consent_missing)" {
		t.Fatalf("%s", got)
	}
	if res.License != "MIT" {
		t.Errorf("license at the commit %q", res.License)
	}
	q := dryRunRequest(res)
	ui, err := gh.Download(ctx, "https://github.com/BRF-Tech/filex-drawio/releases/download/v31.5.3/ui.zip", 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ui.zip %d bytes via %v", ui.Size, ui.Hops)
	q.ExpectUISHA256 = ui.SHA256
	_, checks, err = r.DryRun(ctx, q)
	if err != nil || checkSummary(checks) != "dry_run[latest]=passed dry_run[floor]=passed" {
		t.Fatalf("drawio: %v %s", err, checkSummary(checks))
	}

	res, _, err = resolve.New(resolve.Options{GitHub: gh}).Resolve(ctx, domain.ResolveRequest{Repo: "BRF-Tech/filex-sign", Ref: "v0.3.0", Publisher: "brf-tech"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	mod, err := gh.Download(ctx, "https://github.com/BRF-Tech/filex-sign/releases/download/v0.3.0/plugin.wasm", 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("plugin.wasm %d bytes in %s via %v", mod.Size, time.Since(start).Round(time.Millisecond), mod.Hops)
	q = dryRunRequest(res)
	q.ExpectWasmSHA256 = mod.SHA256
	_, checks, err = r.DryRun(ctx, q)
	if err != nil || checkSummary(checks) != "dry_run[latest]=passed dry_run[floor]=passed" {
		t.Fatalf("sign: %v %s", err, checkSummary(checks))
	}
}
