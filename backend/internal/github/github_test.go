package github

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// fakeGitHub serves the answers GitHub gave on 2026-10-03 for
// BRF-Tech/filex-sign (testdata/recorded) plus hand-made edge cases, on one
// TLS server that plays both api.github.com and raw.githubusercontent.com.
type fakeGitHub struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string
}

const (
	blobA = "1111111111111111111111111111111111111111"
	blobB = "2222222222222222222222222222222222222222"
)

const signCommit = "ac3fa19e20be574cde1a154dbfbe01f59c55d2d1"

func recorded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/recorded/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFake(t *testing.T) (*fakeGitHub, *Client) {
	t.Helper()
	f := &fakeGitHub{}
	anon := recorded(t, "repo-filex-sign-anon.json")
	member := recorded(t, "repo-filex-sign-member.json")
	licenseAt := recorded(t, "license-filex-sign-at-ac3fa19e.json")
	license404 := recorded(t, "license-404.json")
	routes := map[string][]byte{
		"/repos/BRF-Tech/filex-sign/git/ref/tags/v0.3.0":                               recorded(t, "ref-tags-v0.3.0.json"),
		"/repos/BRF-Tech/filex-sign/git/tags/aa85dd6a0139e45817cc95ea3d8beb5afe95fc7e": recorded(t, "tag-aa85dd6a.json"),
		"/repos/BRF-Tech/filex-sign/git/ref/heads/main":                                recorded(t, "ref-heads-main.json"),
		"/repos/o/light/git/ref/tags/v1.0.0":                                           []byte(`{"ref":"refs/tags/v1.0.0","object":{"sha":"1111111111111111111111111111111111111111","type":"commit"}}`),
		"/repos/o/nested/git/ref/tags/v2":                                              []byte(`{"ref":"refs/tags/v2","object":{"sha":"aaaa","type":"tag"}}`),
		"/repos/o/nested/git/tags/aaaa":                                                []byte(`{"tag":"v2","sha":"aaaa","object":{"sha":"bbbb","type":"tag"},"verification":{"verified":false,"reason":"unsigned"}}`),
		"/repos/o/nested/git/tags/bbbb":                                                []byte(`{"tag":"inner","sha":"bbbb","object":{"sha":"2222222222222222222222222222222222222222","type":"commit"}}`),
		"/repos/o/tree/git/ref/tags/t":                                                 []byte(`{"ref":"refs/tags/t","object":{"sha":"3333333333333333333333333333333333333333","type":"tree"}}`),
		"/repos/o/slash/git/ref/tags/release/1.0":                                      []byte(`{"ref":"refs/tags/release/1.0","object":{"sha":"4444444444444444444444444444444444444444","type":"commit"}}`),
		"/BRF-Tech/filex-sign/" + signCommit + "/filex-app.json":                       []byte(`{"name":"sign"}`),
		"/BRF-Tech/filex-sign/" + signCommit + "/docs/README.md":                       []byte("# Sign\n"),
		"/releases/download/v0.3.0/plugin.wasm":                                        []byte("\x00asm\x01\x00\x00\x00"),
		"/users/BRF-Tech":                                                              []byte(`{"login":"BRF-Tech","id":159972728,"type":"Organization","site_admin":false}`),
		"/users/no-id":                                                                 []byte(`{"login":"no-id","type":"User"}`),
	}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.URL.Path+" "+r.Header.Get("Authorization"))
		f.mu.Unlock()
		switch r.URL.Path {
		case "/repos/BRF-Tech/filex-sign":
			if r.Header.Get("Authorization") == "" {
				_, _ = w.Write(anon)
			} else {
				_, _ = w.Write(member)
			}
			return
		case "/repos/BRF-Tech/filex-sign/license":
			if r.URL.Query().Get("ref") == signCommit {
				_, _ = w.Write(licenseAt)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(license404)
			return
		case "/repos/o/lic/license", "/repos/o/lic-other/license", "/repos/o/lic-dnone/license", "/repos/o/lic-nofile/license":
			ref := r.URL.Query().Get("ref")
			const na = `{"path":"LICENSE","sha":"` + blobA + `","license":{"spdx_id":"NOASSERTION","name":"Other"}}`
			const mit = `{"path":"LICENSE","sha":"` + blobA + `","license":{"spdx_id":"MIT","name":"MIT License"}}`
			switch {
			case ref != "":
				_, _ = w.Write([]byte(na))
			case r.URL.Path == "/repos/o/lic-dnone/license":
				_, _ = w.Write([]byte(na))
			default:
				_, _ = w.Write([]byte(mit))
			}
			return
		case "/repos/o/lic/contents/LICENSE", "/repos/o/lic-dnone/contents/LICENSE":
			_, _ = w.Write([]byte(`{"path":"LICENSE","sha":"` + blobA + `"}`))
			return
		case "/repos/o/lic-other/contents/LICENSE":
			_, _ = w.Write([]byte(`{"path":"LICENSE","sha":"` + blobB + `"}`))
			return
		case "/repos/o/limited":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1791057362")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		case "/repos/o/broken":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"Server Error"}`))
			return
		case "/repos/o/forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Repository access blocked"}`))
			return
		case "/repos/o/revoked":
			// GitHub's answer for a token it does not know any more: the
			// repository is public, the token is not (BRF-Tech/filex#118).
			if r.Header.Get("Authorization") != "" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":78,"name":"revoked","full_name":"o/revoked","owner":{"id":6,"login":"o","type":"User"},"private":false,"visibility":"public"}`))
			return
		case "/repos/o/policy":
			// GitHub's answer on 2026-10-04 when the BRF-Tech organization
			// refused the store's fine-grained token (lifetime over 366 days).
			if r.Header.Get("Authorization") != "" {
				w.Header().Set("X-RateLimit-Remaining", "4990")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"The 'o' organization forbids access via a fine-grained personal access tokens if the token's lifetime is greater than 366 days. Please adjust your token's lifetime at the following URL: https://github.com/settings/personal-access-tokens/1","documentation_url":"https://docs.github.com/rest/repos/repos#get-a-repository","status":"403"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":77,"name":"policy","full_name":"o/policy","owner":{"id":5,"login":"o","type":"Organization"},"private":false}`))
			return
		}
		if b, ok := routes[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(recorded(t, "ref-tags-404.json"))
	}))
	t.Cleanup(f.srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	u, _ := url.Parse(f.srv.URL)
	hc := safehttp.New(safehttp.Config{
		AllowHosts: []string{u.Host},
		RootCAs:    pool,
		Addresses:  func(netip.Addr) error { return nil },
	})
	return f, New(Options{HTTP: hc, APIURL: f.srv.URL, RawURL: f.srv.URL, Token: "server-token"})
}

func (f *fakeGitHub) lastAuth(prefix string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.auth) - 1; i >= 0; i-- {
		if strings.HasPrefix(f.auth[i], prefix+" ") {
			return strings.TrimPrefix(f.auth[i], prefix+" ")
		}
	}
	return "(none)"
}

func TestParseRepo_TheFormsFilexAccepts(t *testing.T) {
	for in, want := range map[string]string{
		"BRF-Tech/filex-sign":                        "BRF-Tech/filex-sign",
		"github.com/BRF-Tech/filex-sign":             "BRF-Tech/filex-sign",
		"https://github.com/BRF-Tech/filex-sign":     "BRF-Tech/filex-sign",
		"https://github.com/BRF-Tech/filex-sign.git": "BRF-Tech/filex-sign",
		" https://github.com/BRF-Tech/filex-sign/ ":  "BRF-Tech/filex-sign",
		"alice/filex.lang_es":                        "alice/filex.lang_es",
	} {
		o, n, err := ParseRepo(in)
		if err != nil || o+"/"+n != want {
			t.Errorf("%q = %s/%s, %v", in, o, n, err)
		}
	}
	for _, bad := range []string{"", "filex-sign", "a/b/c", "https://gitlab.com/a/b", "-x/y", "o/..", "o/", "/x", "o/n?x=1", "o/n#f"} {
		if _, _, err := ParseRepo(bad); !errors.Is(err, ErrBadRepo) {
			t.Errorf("%q: %v, want ErrBadRepo", bad, err)
		}
	}
}

// Anonymous (or the server token): no `permissions` unless GitHub gives it.
// The submitter's token: their role, recorded from GitHub on 2026-10-03.
func TestRepository_PermissionsAreTheCallersRole(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	r, err := c.Repository(ctx, "user-token", "BRF-Tech", "filex-sign")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.lastAuth("/repos/BRF-Tech/filex-sign"); got != "Bearer user-token" {
		t.Errorf("Authorization %q: the submitter's own token must be used", got)
	}
	if r.ID != 1386221114 || r.Owner.ID != 159972728 || r.Owner.Type != "Organization" || r.SPDX() != "MIT" || r.Private || r.Archived {
		t.Errorf("repo %+v", r)
	}
	if !r.Permissions.CanList() {
		t.Errorf("permissions %+v: admin must be able to list", r.Permissions)
	}

	anon := New(Options{HTTP: c.http, APIURL: c.api, RawURL: c.raw})
	r, err = anon.Repository(ctx, "", "BRF-Tech", "filex-sign")
	if err != nil {
		t.Fatal(err)
	}
	if got := f.lastAuth("/repos/BRF-Tech/filex-sign"); got != "" {
		t.Errorf("anonymous read sent Authorization %q", got)
	}
	if r.Permissions != nil || r.Permissions.CanList() {
		t.Errorf("anonymous answer carried permissions %+v", r.Permissions)
	}
	if _, err := c.Repository(ctx, "", "BRF-Tech", "filex-sign"); err != nil {
		t.Fatal(err)
	}
	if got := f.lastAuth("/repos/BRF-Tech/filex-sign"); got != "Bearer server-token" {
		t.Errorf("without a user token the server token is used, got %q", got)
	}
	for _, p := range []Permissions{{Push: true, Pull: true}, {Triage: true}, {Pull: true}} {
		if p.CanList() {
			t.Errorf("%+v must not be able to list", p)
		}
	}
	if !(&Permissions{Maintain: true}).CanList() {
		t.Error("maintain lists")
	}
}

func TestRepository_ErrorsSayWhetherToRetry(t *testing.T) {
	_, c := newFake(t)
	ctx := context.Background()
	_, err := c.Repository(ctx, "", "o", "missing")
	if !errors.Is(err, ErrNotFound) || Retryable(err) {
		t.Errorf("missing: %v (retryable %v)", err, Retryable(err))
	}
	_, err = c.Repository(ctx, "", "o", "limited")
	var rl *RateLimitError
	if !errors.As(err, &rl) || !Retryable(err) || rl.Reset.Unix() != 1791057362 {
		t.Errorf("rate limit: %v", err)
	}
	_, err = c.Repository(ctx, "", "o", "broken")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 502 || !Retryable(err) {
		t.Errorf("5xx: %v", err)
	}
	_, err = c.Repository(ctx, "", "o", "forbidden")
	if !errors.As(err, &ae) || ae.Status != 403 || Retryable(err) {
		t.Errorf("403 without a rate limit: %v", err)
	}
}

func TestResolveTag_PeelsAnnotatedTagsAndRefusesBranches(t *testing.T) {
	_, c := newFake(t)
	ctx := context.Background()
	tag, err := c.ResolveTag(ctx, "", "BRF-Tech", "filex-sign", "v0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if tag.Commit != signCommit || !tag.Annotated || !tag.Verified || tag.Reason != "valid" {
		t.Errorf("v0.3.0 = %+v", tag)
	}
	tag, err = c.ResolveTag(ctx, "", "o", "light", "refs/tags/v1.0.0")
	if err != nil || tag.Commit != strings.Repeat("1", 40) || tag.Annotated || tag.Verified {
		t.Errorf("lightweight = %+v, %v", tag, err)
	}
	tag, err = c.ResolveTag(ctx, "", "o", "nested", "v2")
	if err != nil || tag.Commit != strings.Repeat("2", 40) || !tag.Annotated || tag.Verified || tag.Reason != "unsigned" {
		t.Errorf("nested = %+v, %v", tag, err)
	}
	tag, err = c.ResolveTag(ctx, "", "o", "slash", "release/1.0")
	if err != nil || tag.Commit != strings.Repeat("4", 40) {
		t.Errorf("a tag with a slash = %+v, %v", tag, err)
	}
	if _, err := c.ResolveTag(ctx, "", "BRF-Tech", "filex-sign", "main"); !errors.Is(err, ErrNotATag) {
		t.Errorf("a branch: %v, want ErrNotATag", err)
	}
	if _, err := c.ResolveTag(ctx, "", "BRF-Tech", "filex-sign", "v9.9.9"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing tag: %v", err)
	}
	// The singular ref endpoint is an exact match: "v0" is not "v0.3.0".
	if _, err := c.ResolveTag(ctx, "", "BRF-Tech", "filex-sign", "v0"); !errors.Is(err, ErrNotFound) {
		t.Errorf("prefix: %v", err)
	}
	if _, err := c.ResolveTag(ctx, "", "o", "tree", "t"); err == nil {
		t.Error("a tag of a tree must be refused")
	}
	for _, bad := range []string{"", "a..b", "a b", "x~1", "x^", "a:b"} {
		if _, err := c.ResolveTag(ctx, "", "o", "light", bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestFileAt_ReadsAtTheCommitWithACap(t *testing.T) {
	_, c := newFake(t)
	ctx := context.Background()
	f, err := c.FileAt(ctx, "BRF-Tech", "filex-sign", signCommit, "filex-app.json", 1024)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(`{"name":"sign"}`))
	if string(f.Bytes) != `{"name":"sign"}` || f.SHA256 != hex.EncodeToString(sum[:]) || !strings.HasSuffix(f.URL, "/BRF-Tech/filex-sign/"+signCommit+"/filex-app.json") {
		t.Errorf("file %+v", f)
	}
	if f, err := c.FileAt(ctx, "BRF-Tech", "filex-sign", signCommit, "/docs/README.md", 1024); err != nil || string(f.Bytes) != "# Sign\n" {
		t.Errorf("nested path: %v", err)
	}
	if _, err := c.FileAt(ctx, "BRF-Tech", "filex-sign", signCommit, ".filex-apps.json", 1024); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := c.FileAt(ctx, "BRF-Tech", "filex-sign", signCommit, "filex-app.json", 4); !errors.Is(err, safehttp.ErrTooLarge) {
		t.Errorf("over the cap: %v", err)
	}
}

func TestDownload_StreamsADigest(t *testing.T) {
	f, c := newFake(t)
	a, err := c.Download(context.Background(), f.srv.URL+"/releases/download/v0.3.0/plugin.wasm", 1024)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("\x00asm\x01\x00\x00\x00"))
	if a.SHA256 != hex.EncodeToString(sum[:]) || a.Size != 8 {
		t.Errorf("asset %+v", a)
	}
	if _, err := c.Download(context.Background(), f.srv.URL+"/releases/download/v0.3.0/missing.wasm", 1024); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing asset: %v", err)
	}
	if _, err := c.Download(context.Background(), "https://evil.example/x.wasm", 1024); !errors.Is(err, safehttp.ErrHostNotAllowed) || Retryable(err) {
		t.Errorf("off-list host: %v", err)
	}
}

// The license is GitHub's detection AT the commit (?ref=), recorded from
// GitHub on 2026-10-03; a ref without a license file is ErrNotFound.
func TestLicenseAt_TheCommitsLicence(t *testing.T) {
	f, c := newFake(t)
	l, err := c.LicenseAt(context.Background(), "", "BRF-Tech", "filex-sign", signCommit)
	if err != nil || l.SPDX() != "MIT" || l.Path != "LICENSE" {
		t.Fatalf("%+v %v", l, err)
	}
	if got := f.lastAuth("/repos/BRF-Tech/filex-sign/license"); got != "Bearer server-token" {
		t.Errorf("Authorization %q", got)
	}
	if _, err := c.LicenseAt(context.Background(), "", "BRF-Tech", "filex-sign", strings.Repeat("0", 40)); !errors.Is(err, ErrNotFound) {
		t.Errorf("no license at that ref: %v", err)
	}
	for _, l := range []*LicenseFile{nil, {}, {License: &License{SPDXID: "NOASSERTION"}}} {
		if l.SPDX() != "" {
			t.Errorf("%+v: an undetected license is empty", l)
		}
	}
}

// An owner by login: the numeric id an official publisher is bound to
// (GitHub's answer for the BRF-Tech organization).
func TestOwner_TheNumericIDOfALogin(t *testing.T) {
	f, c := newFake(t)
	o, err := c.Owner(context.Background(), "", "BRF-Tech")
	if err != nil || o.ID != 159972728 || o.Login != "BRF-Tech" || o.Type != "Organization" {
		t.Fatalf("%+v %v", o, err)
	}
	if got := f.lastAuth("/users/BRF-Tech"); got != "Bearer server-token" {
		t.Errorf("Authorization %q", got)
	}
	if _, err := c.Owner(context.Background(), "", "nobody-here"); !errors.Is(err, ErrNotFound) || Retryable(err) {
		t.Errorf("missing: %v", err)
	}
	if _, err := c.Owner(context.Background(), "", "../repos/x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("not a login: %v", err)
	}
	var ae *APIError
	if _, err := c.Owner(context.Background(), "", "no-id"); !errors.As(err, &ae) {
		t.Errorf("an answer without an id: %v", err)
	}
}

// GitHub refusing the token says nothing about the repository: a person's
// own token comes back as ErrBadCredentials (never retried: credentials do
// not heal), while the SERVER's refused token falls back to the anonymous
// read, because the store reads public data only. Red before BRF-Tech/filex#118:
// both were an APIError the resolver called "private or does not exist".
func TestAPI_ARefusedTokenIsNotAVerdictOnTheRepository(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()

	_, err := c.Repository(ctx, "user-token", "o", "revoked")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a person's refused token: %v", err)
	}
	if Retryable(err) {
		t.Error("a refused token is not worth retrying")
	}
	var ae *APIError
	if errors.As(err, &ae) {
		t.Error("a refused token is not an APIError a caller reads as a repository answer")
	}

	r, err := c.Repository(ctx, "", "o", "revoked")
	if err != nil || r.ID != 78 || r.Owner.ID != 6 {
		t.Fatalf("the server's refused token falls back to anonymous: %+v %v", r, err)
	}
	if got := f.lastAuth("/repos/o/revoked"); got != "" {
		t.Errorf("the second read sent Authorization %q", got)
	}
}

func TestCredentialsRefused_401AndTheRevokedOrExpired403(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{401, `{"message":"Bad credentials"}`, true},
		{401, `{}`, true},
		{403, `{"message":"Bad credentials"}`, true},
		{403, `{"message":"Token expired"}`, true},
		{403, `{"message":"This token has been revoked"}`, true},
		{403, `{"message":"Repository access blocked"}`, false},
		{403, `{"message":"API rate limit exceeded"}`, false},
		{404, `{"message":"Not Found"}`, false},
		{200, `{}`, false},
	} {
		if got := credentialsRefused(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%d %s = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}

// An owner whose policy refuses the server's token (not the data): the
// same public read is made anonymously. A person's own token is never
// swapped: its answer is their role.
func TestAPI_AnOwnersPolicyAgainstTheServerTokenFallsBackToAnonymous(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	r, err := c.Repository(ctx, "", "o", "policy")
	if err != nil || r.ID != 77 || r.Owner.ID != 5 {
		t.Fatalf("server token refused by policy: %+v %v", r, err)
	}
	if got := f.lastAuth("/repos/o/policy"); got != "" {
		t.Errorf("the second read sent Authorization %q", got)
	}
	var ae *APIError
	if _, err := c.Repository(ctx, "user-token", "o", "policy"); !errors.As(err, &ae) || ae.Status != 403 {
		t.Errorf("a person's token refused by policy: %v", err)
	}
	if _, err := c.Repository(ctx, "", "o", "forbidden"); !errors.As(err, &ae) || ae.Status != 403 {
		t.Errorf("a 403 about the data is not retried anonymously: %v", err)
	}
	if got := f.lastAuth("/repos/o/forbidden"); got != "Bearer server-token" {
		t.Errorf("forbidden: Authorization %q", got)
	}
}

// GitHub answers NOASSERTION at a commit whose LICENSE is byte for byte the
// default branch's (filextext-app v0.1.0/v0.1.1). The default branch's
// verdict is carried only for the same blob.
func TestLicenseAt_CarriesTheDefaultBranchForTheSameBlob(t *testing.T) {
	ctx := context.Background()
	_, c := newFake(t)
	commit := strings.Repeat("a", 40)

	l, err := c.LicenseAt(ctx, "", "o", "lic", commit)
	if err != nil || l.SPDX() != "MIT" || !l.Carried || l.Path != "LICENSE" {
		t.Errorf("same blob: %+v %v", l, err)
	}
	l, err = c.LicenseAt(ctx, "", "o", "lic-other", commit)
	if err != nil || l.SPDX() != "" || l.Carried {
		t.Errorf("different blob: %+v %v", l, err)
	}
	l, err = c.LicenseAt(ctx, "", "o", "lic-dnone", commit)
	if err != nil || l.SPDX() != "" || l.Carried {
		t.Errorf("default branch also NOASSERTION: %+v %v", l, err)
	}
	l, err = c.LicenseAt(ctx, "", "o", "lic-nofile", commit)
	if err != nil || l.SPDX() != "" || l.Carried {
		t.Errorf("file absent at the commit: %+v %v", l, err)
	}
	l, err = c.LicenseAt(ctx, "", "BRF-Tech", "filex-sign", signCommit)
	if err != nil || l.Carried || l.SPDX() != "MIT" {
		t.Errorf("a detected license is never carried: %+v %v", l, err)
	}
}
