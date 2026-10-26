package resolve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// The repository GitHub showed on 2026-10-03 (../github/testdata/recorded):
// BRF-Tech/filex-sign, tag v0.3.0 = annotated tag aa85dd6a over commit
// ac3fa19e. Its files below are made up for the tests (the real repository
// has no .filex-apps.json yet).
const (
	signRepo   = "BRF-Tech/filex-sign"
	signCommit = "ac3fa19e20be574cde1a154dbfbe01f59c55d2d1"
	otherSHA   = "1111111111111111111111111111111111111111"
)

var wasmBytes = []byte("\x00asm\x01\x00\x00\x00")

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func recordedGH(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../github/testdata/recorded/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 1, A: 255})
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fake is GitHub (API, raw files, release downloads) on one TLS server.
type fake struct {
	srv *httptest.Server
	mu  sync.Mutex
	// repos: "owner/name" -> repository JSON (without permissions); perms is
	// added when the request carries a token.
	repos    map[string]map[string]any
	perms    map[string]map[string]bool
	tags     map[string]string // "owner/name@tag" -> commit (lightweight)
	licenses map[string]string // "owner/name@commit" -> SPDX id; absent = no license file (404)
	branches map[string]bool   // "owner/name@branch"
	files    map[string][]byte // "owner/name/<commit or ref>/<path>"
	assets   map[string][]byte // path on the server
	failRepo int               // answer 502 to repository reads
	badToken string            // a token GitHub answers 401 "Bad credentials" for
}

// refuse makes the fake GitHub answer 401 for that token, the way GitHub
// answers for one it does not know any more (BRF-Tech/filex#118).
func (f *fake) refuse(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.badToken = token
}

func newFake(t *testing.T) *fake {
	f := &fake{
		repos: map[string]map[string]any{}, perms: map[string]map[string]bool{},
		tags: map[string]string{}, licenses: map[string]string{}, branches: map[string]bool{}, files: map[string][]byte{}, assets: map[string][]byte{},
	}
	var anon map[string]any
	if err := json.Unmarshal(recordedGH(t, "repo-filex-sign-anon.json"), &anon); err != nil {
		t.Fatal(err)
	}
	f.repos[signRepo] = anon
	f.perms[signRepo] = map[string]bool{"admin": true, "maintain": true, "push": true, "triage": true, "pull": true}
	refTag := recordedGH(t, "ref-tags-v0.3.0.json")
	tagObj := recordedGH(t, "tag-aa85dd6a.json")
	headMain := recordedGH(t, "ref-heads-main.json")
	licenseAt := recordedGH(t, "license-filex-sign-at-ac3fa19e.json")
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		write := func(b []byte) { _, _ = w.Write(b) }
		switch {
		case p == "/repos/BRF-Tech/filex-sign/git/ref/tags/v0.3.0":
			write(refTag)
			return
		case p == "/repos/BRF-Tech/filex-sign/git/tags/aa85dd6a0139e45817cc95ea3d8beb5afe95fc7e":
			write(tagObj)
			return
		case p == "/repos/BRF-Tech/filex-sign/git/ref/heads/main":
			write(headMain)
			return
		case p == "/repos/BRF-Tech/filex-sign/license" && r.URL.Query().Get("ref") == signCommit && f.licenses[signRepo+"@"+signCommit] == "":
			write(licenseAt)
			return
		case strings.HasPrefix(p, "/repos/") && strings.HasSuffix(p, "/license"):
			full := strings.TrimSuffix(strings.TrimPrefix(p, "/repos/"), "/license")
			if id, ok := f.licenses[full+"@"+r.URL.Query().Get("ref")]; ok && id != "none" {
				write([]byte(`{"name":"LICENSE","path":"LICENSE","license":{"spdx_id":"` + id + `"}}`))
				return
			}
		case strings.HasPrefix(p, "/repos/"):
			rest := strings.TrimPrefix(p, "/repos/")
			parts := strings.SplitN(rest, "/", 3)
			if len(parts) >= 2 {
				full := parts[0] + "/" + parts[1]
				if len(parts) == 2 {
					if f.failRepo > 0 {
						f.failRepo--
						w.WriteHeader(http.StatusBadGateway)
						write([]byte(`{"message":"Server Error"}`))
						return
					}
					if f.badToken != "" && r.Header.Get("Authorization") == "Bearer "+f.badToken {
						w.WriteHeader(http.StatusUnauthorized)
						write([]byte(`{"message":"Bad credentials","status":"401"}`))
						return
					}
					if repo, ok := f.repos[full]; ok {
						out := map[string]any{}
						for k, v := range repo {
							out[k] = v
						}
						if r.Header.Get("Authorization") != "" {
							if perms, ok := f.perms[full]; ok {
								out["permissions"] = perms
							}
						}
						b, _ := json.Marshal(out)
						write(b)
						return
					}
				}
				if len(parts) == 3 {
					if tag, ok := strings.CutPrefix(parts[2], "git/ref/tags/"); ok {
						if c, ok := f.tags[full+"@"+tag]; ok {
							write([]byte(`{"ref":"refs/tags/` + tag + `","object":{"sha":"` + c + `","type":"commit"}}`))
							return
						}
					}
					if br, ok := strings.CutPrefix(parts[2], "git/ref/heads/"); ok && f.branches[full+"@"+br] {
						write([]byte(`{"ref":"refs/heads/` + br + `","object":{"sha":"` + otherSHA + `","type":"commit"}}`))
						return
					}
				}
			}
		default:
			if b, ok := f.assets[p]; ok {
				write(b)
				return
			}
			if b, ok := f.files[strings.TrimPrefix(p, "/")]; ok {
				write(b)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		write([]byte(`{"message":"Not Found","status":"404"}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) put(repo, at, path string, b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[repo+"/"+at+"/"+path] = b
}

func (f *fake) asset(path string, b []byte) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assets[path] = b
	return f.srv.URL + path
}

func (f *fake) addRepo(full string, id, ownerID int64, mutate func(map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owner, name, _ := strings.Cut(full, "/")
	r := map[string]any{"id": id, "name": name, "full_name": full, "private": false, "archived": false, "visibility": "public",
		"owner": map[string]any{"id": ownerID, "login": owner, "type": "User"}, "license": map[string]any{"spdx_id": "Apache-2.0"}}
	if mutate != nil {
		mutate(r)
	}
	f.repos[full] = r
}

type sink struct {
	mu   sync.Mutex
	puts []domain.MediaRef
}

func (s *sink) Put(_ context.Context, m domain.MediaRef, _ string, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sum(body) != m.SHA256 {
		panic("media sha mismatch")
	}
	s.puts = append(s.puts, m)
	return nil
}

func (f *fake) resolver(t *testing.T, ms *sink, lim Limits) *Resolver {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	u, _ := url.Parse(f.srv.URL)
	hc := safehttp.New(safehttp.Config{AllowHosts: []string{u.Host}, RootCAs: pool, Addresses: func(netip.Addr) error { return nil }})
	gh := github.New(github.Options{HTTP: hc, APIURL: f.srv.URL, RawURL: f.srv.URL})
	clock := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	o := Options{GitHub: gh, Limits: lim, Now: func() time.Time { return clock }}
	if ms != nil {
		o.Media = ms
	}
	return New(o)
}

// signFixture lays out the module app at BRF-Tech/filex-sign v0.3.0.
func (f *fake) signFixture(t *testing.T, mutateManifest func(map[string]any), consentDoc string) {
	t.Helper()
	wasmURL := f.asset("/BRF-Tech/filex-sign/releases/download/v0.3.0/plugin.wasm", wasmBytes)
	m := map[string]any{
		"manifest_version": 1, "name": "sign", "version": "0.3.0", "label": map[string]string{"en": "e-Signature"},
		"filex": ">=0.49.0", "permissions": []string{"files:read", " files:write", "http:FreeTSA.org", "files:read"},
		"languages": []string{"en", "TR"},
		"wasm":      map[string]string{"url": strings.Replace(wasmURL, "v0.3.0", "{tag}", 1), "sha256": sum(wasmBytes)},
		"actions":   []map[string]string{{"id": "sign"}},
		"brand_new": "a field this store does not know",
	}
	if mutateManifest != nil {
		mutateManifest(m)
	}
	mb, _ := json.Marshal(m)
	f.put(signRepo, signCommit, "filex-app.json", mb)
	if consentDoc == "" {
		consentDoc = `{"publisher":"brf-tech","categories":["documents"],"summary":{"en":"Sign PDFs."},"description":"docs/STORE.md","icon":"assets/icon.png","screenshots":[{"path":"assets/shot.png","caption":{"en":"Signing"}},{"path":"assets/shot.webp"}]}`
	}
	f.put(signRepo, signCommit, ".filex-apps.json", []byte(consentDoc))
	f.put(signRepo, signCommit, "docs/STORE.md", []byte("# Sign\n\nSign PDFs in filex.\n"))
	f.put(signRepo, signCommit, "assets/icon.png", pngOf(t, 256, 256))
	f.put(signRepo, signCommit, "assets/shot.png", pngOf(t, 1280, 720))
	f.put(signRepo, signCommit, "assets/shot.webp", fixture(t, "shot-640x360.webp"))
}

func req(token string) domain.ResolveRequest {
	return domain.ResolveRequest{SubmissionID: 1, Repo: "https://github.com/BRF-Tech/filex-sign", Ref: "v0.3.0", Publisher: "brf-tech", UserToken: token}
}

func statuses(checks []domain.CheckResult) string {
	var b strings.Builder
	for _, c := range checks {
		b.WriteString(string(c.Kind) + "=" + string(c.Status))
		if c.Code != "" {
			b.WriteString("(" + c.Code + ")")
		}
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}

func last(checks []domain.CheckResult) domain.CheckResult {
	if len(checks) == 0 {
		return domain.CheckResult{}
	}
	return checks[len(checks)-1]
}

func TestResolve_AModuleAppEndToEnd(t *testing.T) {
	f := newFake(t)
	f.signFixture(t, nil, "")
	ms := &sink{}
	res, checks, err := f.resolver(t, ms, Limits{}).Resolve(context.Background(), req("user-token"))
	if err != nil {
		t.Fatal(err)
	}
	want := "repo_access=passed tag=passed manifest=passed consent=passed artifacts=passed media=passed"
	if got := statuses(checks); got != want {
		t.Fatalf("checks: %s\nwant:   %s\n%s", got, want, last(checks).Message)
	}
	if res.RepoID != 1386221114 || res.OwnerID != 159972728 || res.OwnerType != "Organization" || res.RepoOwner != "BRF-Tech" || res.RepoName != "filex-sign" || res.License != "MIT" {
		t.Errorf("repository fields %+v", res)
	}
	if res.Ref != "v0.3.0" || res.CommitSHA != signCommit || !res.TagVerified {
		t.Errorf("tag fields %s %s %v", res.Ref, res.CommitSHA, res.TagVerified)
	}
	if res.Name != "sign" || res.Version != "0.3.0" || res.Kind != domain.KindApp || res.FilexRange != ">=0.49.0" {
		t.Errorf("manifest fields %s %s %s %q", res.Name, res.Version, res.Kind, res.FilexRange)
	}
	if strings.Join(res.Permissions, ",") != "files:read,files:write,http:freetsa.org" {
		t.Errorf("permissions %v (filex trims, lower-cases an http host and drops repeats)", res.Permissions)
	}
	if strings.Join(res.Languages, ",") != "en,tr" {
		t.Errorf("languages %v", res.Languages)
	}
	if !strings.HasSuffix(res.ManifestURL, "/BRF-Tech/filex-sign/"+signCommit+"/filex-app.json") || len(res.ManifestSHA256) != 64 || !json.Valid(res.Manifest) {
		t.Errorf("manifest pin %s %s", res.ManifestURL, res.ManifestSHA256)
	}
	if res.Wasm == nil || res.Wasm.SHA256 != sum(wasmBytes) || res.Wasm.Size != 8 || !strings.HasSuffix(res.Wasm.URL, "/releases/download/v0.3.0/plugin.wasm") {
		t.Errorf("module %+v: {tag} expands to the tag name", res.Wasm)
	}
	if res.UI != nil {
		t.Errorf("ui %+v", res.UI)
	}
	if res.Consent.Publisher != "brf-tech" || res.Description != "# Sign\n\nSign PDFs in filex.\n" {
		t.Errorf("consent %+v / %q", res.Consent, res.Description)
	}
	if len(res.Media) != 3 || len(ms.puts) != 3 {
		t.Fatalf("media %+v, stored %d", res.Media, len(ms.puts))
	}
	if m := res.Media[0]; m.Role != "icon" || m.Type != "image/png" || m.Width != 256 || m.Height != 256 {
		t.Errorf("icon %+v", m)
	}
	if m := res.Media[1]; m.Role != "screenshot" || m.Width != 1280 || m.Caption["en"] != "Signing" {
		t.Errorf("screenshot %+v", m)
	}
	if m := res.Media[2]; m.Type != "image/webp" || m.Width != 640 || m.Height != 360 {
		t.Errorf("webp screenshot %+v", m)
	}
	for _, c := range checks {
		if c.EndedAt == nil || c.StartedAt.IsZero() {
			t.Errorf("%s has no times", c.Kind)
		}
	}
}

func TestResolve_RepositoryAccess(t *testing.T) {
	f := newFake(t)
	f.signFixture(t, nil, "")
	r := f.resolver(t, nil, Limits{})
	ctx := context.Background()

	// A background resolve (no user token) does not judge the role.
	_, checks, err := r.Resolve(ctx, req(""))
	if err != nil || checks[0].Status != domain.CheckPassed || !strings.Contains(string(checks[0].Data), "checked at submission") {
		t.Errorf("background: %v %s", err, statuses(checks))
	}

	f.perms[signRepo] = map[string]bool{"push": true, "pull": true}
	_, checks, _ = r.Resolve(ctx, req("user-token"))
	if c := last(checks); c.Code != CodeRepoPermission || len(checks) != 1 {
		t.Errorf("push only: %s", statuses(checks))
	}
	delete(f.perms, signRepo)
	_, checks, _ = r.Resolve(ctx, req("user-token"))
	if c := last(checks); c.Code != CodeRepoPermission {
		t.Errorf("no permissions field: %s", statuses(checks))
	}
	f.perms[signRepo] = map[string]bool{"maintain": true, "pull": true}
	if _, checks, _ = r.Resolve(ctx, req("user-token")); checks[0].Status != domain.CheckPassed {
		t.Errorf("maintain: %s", statuses(checks))
	}

	f.addRepo("o/archived", 7, 8, func(m map[string]any) { m["archived"] = true })
	f.addRepo("o/private", 9, 8, func(m map[string]any) { m["private"] = true; m["visibility"] = "private" })
	for repo, code := range map[string]string{"o/archived": CodeRepoArchived, "o/private": CodeRepoPrivate, "o/missing": CodeRepoPrivate, "not a repo": CodeRepoAddress} {
		rq := req("")
		rq.Repo = repo
		_, checks, err := r.Resolve(ctx, rq)
		if err != nil || len(checks) != 1 || checks[0].Code != code {
			t.Errorf("%s: %v %s", repo, err, statuses(checks))
		}
	}

	// GitHub failing is the pipeline's to retry, not a check.
	f.failRepo = 1
	_, checks, err = r.Resolve(ctx, req(""))
	if err == nil || len(checks) != 0 || !github.Retryable(err) {
		t.Errorf("5xx: %v %s", err, statuses(checks))
	}
}

func TestResolve_TheRefMustBeATag(t *testing.T) {
	f := newFake(t)
	f.signFixture(t, nil, "")
	r := f.resolver(t, nil, Limits{})
	for ref, code := range map[string]string{"main": CodeNotATag, "v9.9.9": CodeTagNotFound} {
		rq := req("")
		rq.Ref = ref
		_, checks, err := r.Resolve(context.Background(), rq)
		if err != nil || len(checks) != 2 || last(checks).Code != code {
			t.Errorf("%s: %v %s", ref, err, statuses(checks))
		}
	}
}

func TestResolve_TheManifestAtTheCommit(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		body []byte
		lim  Limits
		code string
	}{
		"missing":       {nil, Limits{}, CodeManifestMissing},
		"not json":      {[]byte("{"), Limits{}, CodeManifestBad},
		"no name":       {[]byte(`{"version":"1.0.0"}`), Limits{}, CodeManifestBad},
		"bad name":      {[]byte(`{"name":"Sign","version":"1.0.0"}`), Limits{}, CodeManifestBad},
		"no version":    {[]byte(`{"name":"sign"}`), Limits{}, CodeManifestBad},
		"bad range":     {[]byte(`{"name":"sign","version":"1.0.0","filex":"^0.47.0"}`), Limits{}, CodeManifestBad},
		"bad min_filex": {[]byte(`{"name":"sign","version":"1.0.0","min_filex":"0.47"}`), Limits{}, CodeManifestBad},
		"too large":     {[]byte(`{"name":"sign","version":"1.0.0","pad":"` + strings.Repeat("x", 100) + `"}`), Limits{Manifest: 64}, CodeTooLarge},
	} {
		f := newFake(t)
		f.signFixture(t, nil, "")
		if c.body == nil {
			delete(f.files, signRepo+"/"+signCommit+"/filex-app.json")
		} else {
			f.put(signRepo, signCommit, "filex-app.json", c.body)
		}
		_, checks, err := f.resolver(t, nil, c.lim).Resolve(ctx, req(""))
		if err != nil || last(checks).Kind != domain.CheckManifest || last(checks).Code != c.code {
			t.Errorf("%s: %v %s", name, err, statuses(checks))
		}
	}
	// The manifest is read at the COMMIT: a different file at the tag name
	// is not what is measured.
	f := newFake(t)
	f.signFixture(t, nil, "")
	f.put(signRepo, "v0.3.0", "filex-app.json", []byte(`{"name":"evil","version":"6.6.6"}`))
	res, checks, err := f.resolver(t, nil, Limits{}).Resolve(ctx, req(""))
	if err != nil || res.Name != "sign" {
		t.Errorf("read at the tag name instead of the commit: %v %s %s", err, res.Name, statuses(checks))
	}
	// min_filex is folded into the one range string.
	f.signFixture(t, func(m map[string]any) { m["filex"] = ">=0.48.0 <0.60.0 || >=0.70.0"; m["min_filex"] = "0.49.0" }, "")
	res, _, _ = f.resolver(t, nil, Limits{}).Resolve(ctx, req(""))
	if res.FilexRange != ">=0.48.0 <0.60.0 >=0.49.0 || >=0.70.0 >=0.49.0" {
		t.Errorf("range %q", res.FilexRange)
	}
}

func TestResolve_TheConsentFile(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		doc  string
		drop string
		code string
	}{
		"missing":             {"", ".filex-apps.json", CodeConsentMissing},
		"invalid":             {`{"publisher":"brf-tech","icon":"/abs.png"}`, "", CodeConsentInvalid},
		"other publisher":     {`{"publisher":"someone-else"}`, "", CodeConsentPub},
		"description missing": {`{"publisher":"brf-tech","description":"docs/NOPE.md"}`, "", CodeConsentInvalid},
	} {
		f := newFake(t)
		f.signFixture(t, nil, c.doc)
		if c.drop != "" {
			delete(f.files, signRepo+"/"+signCommit+"/"+c.drop)
		}
		_, checks, err := f.resolver(t, nil, Limits{}).Resolve(ctx, req(""))
		if err != nil || last(checks).Kind != domain.CheckConsent || last(checks).Code != c.code {
			t.Errorf("%s: %v %s", name, err, statuses(checks))
		}
	}
	f := newFake(t)
	f.signFixture(t, nil, `{"publisher":"brf-tech","description":"docs/STORE.md"}`)
	f.put(signRepo, signCommit, "docs/STORE.md", []byte{0xff, 0xfe})
	if _, checks, _ := f.resolver(t, nil, Limits{}).Resolve(ctx, req("")); last(checks).Code != CodeConsentInvalid {
		t.Errorf("a description that is not text: %s", statuses(checks))
	}
	f.signFixture(t, nil, `{"publisher":"brf-tech","description":"docs/STORE.md"}`)
	f.put(signRepo, signCommit, "docs/STORE.md", bytes.Repeat([]byte("a"), MaxDescriptionBytes+1))
	if _, checks, _ := f.resolver(t, nil, Limits{}).Resolve(ctx, req("")); last(checks).Code != CodeConsentInvalid {
		t.Errorf("a description over 64 KiB: %s", statuses(checks))
	}
}

func TestResolve_ArtifactsArePinnedAndGuarded(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		mutate func(f *fake, m map[string]any)
		lim    Limits
		code   string
	}{
		"sha256 mismatch": {func(f *fake, m map[string]any) {
			m["wasm"].(map[string]string)["sha256"] = strings.Repeat("0", 64)
		}, Limits{}, CodeSHA256Mismatch},
		"no sha256": {func(f *fake, m map[string]any) {
			m["wasm"].(map[string]string)["sha256"] = ""
		}, Limits{}, CodeSHA256Required},
		"host off the list": {func(f *fake, m map[string]any) {
			m["wasm"].(map[string]string)["url"] = "https://evil.example/plugin.wasm"
		}, Limits{}, CodeHostNotAllowed},
		"plain http": {func(f *fake, m map[string]any) {
			m["wasm"].(map[string]string)["url"] = strings.Replace(f.srv.URL, "https:", "http:", 1) + "/plugin.wasm"
		}, Limits{}, CodeHostNotAllowed},
		"missing asset": {func(f *fake, m map[string]any) {
			m["wasm"].(map[string]string)["url"] = f.srv.URL + "/releases/download/{tag}/nope.wasm"
		}, Limits{}, CodeFetchFailed},
		"too large": {nil, Limits{Wasm: 4}, CodeTooLarge},
		"neither module nor interface": {func(f *fake, m map[string]any) {
			delete(m, "wasm")
		}, Limits{}, CodeManifestBad},
	} {
		f := newFake(t)
		f.signFixture(t, func(m map[string]any) {
			if c.mutate != nil {
				c.mutate(f, m)
			}
		}, "")
		_, checks, err := f.resolver(t, nil, c.lim).Resolve(ctx, req(""))
		if err != nil || last(checks).Kind != domain.CheckArtifacts || last(checks).Code != c.code {
			t.Errorf("%s: %v %s: %s", name, err, statuses(checks), last(checks).Message)
		}
	}
}

// A relative address is a file of the repository at the tag, exactly as
// filex's FetchGitHub builds it.
func TestResolve_ARelativeModuleAddressIsARawFileAtTheTag(t *testing.T) {
	f := newFake(t)
	f.signFixture(t, func(m map[string]any) {
		m["wasm"] = map[string]string{"url": "dist/{tag}/plugin.wasm", "sha256": sum(wasmBytes)}
	}, "")
	f.put(signRepo, "v0.3.0", "dist/v0.3.0/plugin.wasm", wasmBytes)
	res, checks, err := f.resolver(t, nil, Limits{}).Resolve(context.Background(), req(""))
	if err != nil || last(checks).Status != domain.CheckPassed {
		t.Fatalf("%v %s: %s", err, statuses(checks), last(checks).Message)
	}
	if want := f.srv.URL + "/BRF-Tech/filex-sign/v0.3.0/dist/v0.3.0/plugin.wasm"; res.Wasm.URL != want {
		t.Errorf("url %s, want %s", res.Wasm.URL, want)
	}
}

func TestResolve_AnInterfaceOnlyApp(t *testing.T) {
	f := newFake(t)
	f.addRepo("BRF-Tech/filex-drawio", 1392172181, 159972728, nil)
	f.tags["BRF-Tech/filex-drawio@v31.5.3"] = strings.Repeat("b", 40)
	f.licenses["BRF-Tech/filex-drawio@"+strings.Repeat("b", 40)] = "Apache-2.0"
	zip := []byte("PK\x03\x04 a tiny interface bundle")
	u := f.asset("/BRF-Tech/filex-drawio/releases/download/v31.5.3/ui.zip", zip)
	mb, _ := json.Marshal(map[string]any{"manifest_version": 1, "name": "drawio", "version": "31.5.3", "filex": ">=0.48.0",
		"permissions": []string{"files:read", "files:write"}, "ui": map[string]any{"bundle": map[string]string{"url": u, "sha256": sum(zip)}}})
	at := "BRF-Tech/filex-drawio/" + strings.Repeat("b", 40)
	f.files[at+"/filex-app.json"] = mb
	f.files[at+"/.filex-apps.json"] = []byte(`{"publisher":"brf-tech","icon":"icon.webp"}`)
	f.files[at+"/icon.webp"] = fixture(t, "icon-128.webp")
	rq := domain.ResolveRequest{Repo: "BRF-Tech/filex-drawio", Ref: "v31.5.3", Publisher: "brf-tech"}
	res, checks, err := f.resolver(t, nil, Limits{}).Resolve(context.Background(), rq)
	if err != nil || statuses(checks) != "repo_access=passed tag=passed manifest=passed consent=passed artifacts=passed media=passed" {
		t.Fatalf("%v %s: %s", err, statuses(checks), last(checks).Message)
	}
	if res.Kind != domain.KindApp || res.Wasm != nil || res.UI == nil || res.UI.SHA256 != sum(zip) || res.License != "Apache-2.0" {
		t.Errorf("%+v", res)
	}
	if m := res.Media[0]; m.Type != "image/webp" || m.Width != 128 {
		t.Errorf("webp icon %+v", m)
	}
}

func TestResolve_ALanguagePack(t *testing.T) {
	f := newFake(t)
	f.addRepo("BRF-Tech/filex-lang-es", 5, 159972728, nil)
	f.tags["BRF-Tech/filex-lang-es@v0.1.7"] = strings.Repeat("c", 40)
	at := "BRF-Tech/filex-lang-es/" + strings.Repeat("c", 40)
	f.files[at+"/filex-app.json"] = []byte(`{"manifest_version":1,"name":"lang-es","version":"0.1.7","label":{"en":"Spanish"},"ui_locales":{"ES":{"a":"b"}}}`)
	f.files[at+"/.filex-apps.json"] = []byte(`{"publisher":"brf-tech","summary":{"en":"Spanish for filex."}}`)
	rq := domain.ResolveRequest{Repo: "BRF-Tech/filex-lang-es", Ref: "v0.1.7", Publisher: "brf-tech"}
	res, checks, err := f.resolver(t, nil, Limits{}).Resolve(context.Background(), rq)
	if err != nil || statuses(checks) != "repo_access=passed tag=passed manifest=passed consent=passed artifacts=skipped media=skipped" {
		t.Fatalf("%v %s", err, statuses(checks))
	}
	if res.Kind != domain.KindLanguagePack || strings.Join(res.Languages, ",") != "es" || len(res.Permissions) != 0 || res.FilexRange != "" {
		t.Errorf("%+v", res)
	}
	// A manifest with ui_locales AND something that runs is an app.
	f.files[at+"/filex-app.json"] = []byte(`{"name":"lang-es","version":"0.1.7","ui_locales":{"es":{"a":"b"}},"permissions":["files:read"],"wasm":{"url":"x.wasm","sha256":"` + sum(wasmBytes) + `"}}`)
	f.files["BRF-Tech/filex-lang-es/v0.1.7/x.wasm"] = wasmBytes
	res, _, _ = f.resolver(t, nil, Limits{}).Resolve(context.Background(), rq)
	if res.Kind != domain.KindApp {
		t.Errorf("kind %s", res.Kind)
	}
}

func TestResolve_MediaRules(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		icon []byte
		shot []byte
		lim  Limits
		code string
	}{
		"jpeg icon":         {jpegOf(t, 256, 256), nil, Limits{}, CodeMediaInvalid},
		"icon not square":   {pngOf(t, 256, 200), nil, Limits{}, CodeMediaInvalid},
		"icon too small":    {pngOf(t, 64, 64), nil, Limits{}, CodeMediaInvalid},
		"icon too big":      {pngOf(t, 2048, 2048), nil, Limits{}, CodeMediaInvalid},
		"not an image":      {[]byte("<svg onload=alert(1)>"), nil, Limits{}, CodeMediaInvalid},
		"truncated png":     {pngOf(t, 256, 256)[:200], nil, Limits{}, CodeMediaInvalid},
		"tiny webp icon":    {fixture(t, "tiny-100.webp"), nil, Limits{}, CodeMediaInvalid},
		"screenshot small":  {nil, pngOf(t, 300, 600), Limits{}, CodeMediaInvalid},
		"screenshot jpeg":   {nil, jpegOf(t, 800, 600), Limits{}, ""},
		"over the size cap": {pngOf(t, 256, 256), nil, Limits{Media: 100}, CodeTooLarge},
	} {
		f := newFake(t)
		f.signFixture(t, nil, `{"publisher":"brf-tech","icon":"i.png","screenshots":[{"path":"s.png"}]}`)
		if c.icon == nil {
			c.icon = pngOf(t, 128, 128)
		}
		if c.shot == nil {
			c.shot = pngOf(t, 640, 480)
		}
		f.put(signRepo, signCommit, "i.png", c.icon)
		f.put(signRepo, signCommit, "s.png", c.shot)
		_, checks, err := f.resolver(t, nil, c.lim).Resolve(ctx, req(""))
		if err != nil || last(checks).Kind != domain.CheckMedia || last(checks).Code != c.code {
			t.Errorf("%s: %v %s: %s", name, err, statuses(checks), last(checks).Message)
		}
	}
	f := newFake(t)
	f.signFixture(t, nil, `{"publisher":"brf-tech","icon":"gone.png"}`)
	if _, checks, _ := f.resolver(t, nil, Limits{}).Resolve(ctx, req("")); last(checks).Code != CodeMediaInvalid {
		t.Errorf("a missing icon: %s", statuses(checks))
	}
}

func TestDerivedPermissions_AreFilexsOwn(t *testing.T) {
	for _, id := range []string{"ui", "ui:package-fetch", "ui:download", "ui:eval", "ui:wasm-eval", "ui:frame-package", "ui:connect-blob", "ui:print", "ui-viewer:.drawio", "ui-viewer:image/png", "ui-new:.drawio", "ui-net:style:https://fonts.example/a.css", "thumbnail:.jar", "files:e2e-plaintext", "files:co-edit", "office-engine"} {
		if !IsDerivedPermission(id) {
			t.Errorf("%s is derived", id)
		}
	}
	for _, id := range []string{"files:read", "files:write", "files:lock", "files:e2e", "http:freetsa.org", "engines:office", "office", "office-engine:x", "public_pages", "uix", "sign"} {
		if IsDerivedPermission(id) {
			t.Errorf("%s is declared, not derived", id)
		}
	}
}

// filex 0.56 derives files:co-edit from the manifest's `co_edit` block: the
// store must neither ask a manifest to list it nor read its absence from
// `permissions` as a missing grant. Red before: it was a declared id.
func TestDerivedPermissions_CoEditIsFilexsOwn(t *testing.T) {
	if !IsDerivedPermission("files:co-edit") {
		t.Error("files:co-edit is derived")
	}
	for _, id := range []string{"files:co", "files:co-edit:x", "co-edit"} {
		if IsDerivedPermission(id) {
			t.Errorf("%s is not filex's derived id", id)
		}
	}
}

// The backend's synchronous check at submission: the same codes, and the
// repository for the owner comparison even when the role is refused.
func TestRepoAccess_TheSynchronousCheck(t *testing.T) {
	f := newFake(t)
	r := f.resolver(t, nil, Limits{})
	ctx := context.Background()
	repo, c, err := r.RepoAccess(ctx, "BRF-Tech/filex-sign", "user-token")
	if err != nil || c.Status != domain.CheckPassed || c.Kind != domain.CheckRepoAccess || repo.Owner.ID != 159972728 || repo.ID != 1386221114 {
		t.Fatalf("%v %+v %+v", err, c, repo)
	}
	f.perms[signRepo] = map[string]bool{"triage": true, "pull": true}
	repo, c, err = r.RepoAccess(ctx, "BRF-Tech/filex-sign", "user-token")
	if err != nil || c.Code != CodeRepoPermission || repo == nil || repo.Owner.ID != 159972728 {
		t.Errorf("triage: %v %+v %+v", err, c, repo)
	}
	repo, c, err = r.RepoAccess(ctx, "o/missing", "user-token")
	if err != nil || c.Code != CodeRepoPrivate || repo != nil {
		t.Errorf("missing: %v %+v %+v", err, c, repo)
	}
}

// The publisher of a PUBLIC repository was told it "is private or does not
// exist" because GitHub had stopped accepting their sign-in: the one thing
// they could not fix by changing the repository (BRF-Tech/filex#118). The refusal
// now names the sign-in, and an address that is not a repository address is
// no longer a verdict on privacy either.
func TestRepoAccess_ARefusedSignInIsNotAPrivateRepository(t *testing.T) {
	f := newFake(t)
	r := f.resolver(t, nil, Limits{})
	ctx := context.Background()
	f.refuse("user-token")

	repo, c, err := r.RepoAccess(ctx, "BRF-Tech/filex-sign", "user-token")
	if err != nil || c.Code != CodeGitHubTokenInvalid || repo != nil {
		t.Fatalf("a refused sign-in: %v %+v %+v", err, c, repo)
	}
	if strings.Contains(c.Message, "private") || strings.Contains(c.Message, "does not exist") {
		t.Errorf("the refusal still blames the repository: %q", c.Message)
	}
	if _, c, err = r.RepoAccess(ctx, "not a repo", "user-token"); err != nil || c.Code != CodeRepoAddress {
		t.Errorf("an address that is not a repository: %v %+v", err, c)
	}
}

func TestCleanHomepage_OnlyHTTPAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"https://filex.sh/apps/sign":    "https://filex.sh/apps/sign",
		" http://example.org ":          "http://example.org",
		"javascript:alert(1)":           "",
		"github.com/x":                  "",
		"https://user:pw@evil.example/": "",
		"":                              "",
	} {
		if got := cleanHomepage(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
	f := newFake(t)
	f.signFixture(t, func(m map[string]any) { m["homepage"] = "javascript:alert(1)" }, "")
	res, _, err := f.resolver(t, nil, Limits{}).Resolve(context.Background(), req(""))
	if err != nil || res.Homepage != "" {
		t.Errorf("homepage %q (%v): a script address must not reach the app page", res.Homepage, err)
	}
}

// The license is the one AT the tag's commit: the default branch's (the
// repository object's) may have changed since.
func TestResolve_TheLicenceAtTheCommit(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	f.signFixture(t, nil, "")
	f.repos[signRepo]["license"] = map[string]any{"spdx_id": "AGPL-3.0"}
	res, checks, err := f.resolver(t, nil, Limits{}).Resolve(ctx, req(""))
	if err != nil || res.License != "MIT" {
		t.Fatalf("license %q (%v): the default branch's AGPL-3.0 must not win over the commit's MIT", res.License, err)
	}
	if !strings.Contains(string(checks[1].Data), `"license":"MIT"`) {
		t.Errorf("tag check data %s", checks[1].Data)
	}
	f.licenses[signRepo+"@"+signCommit] = "none"
	res, checks, err = f.resolver(t, nil, Limits{}).Resolve(ctx, req(""))
	if err != nil || res.License != "" || checks[1].Status != domain.CheckPassed {
		t.Errorf("no license file at the commit: %q %v %s", res.License, err, statuses(checks))
	}
	f.licenses[signRepo+"@"+signCommit] = "NOASSERTION"
	if res, _, _ = f.resolver(t, nil, Limits{}).Resolve(ctx, req("")); res.License != "" {
		t.Errorf("an undetected license: %q", res.License)
	}
}
