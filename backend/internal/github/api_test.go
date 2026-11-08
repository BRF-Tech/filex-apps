package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// fastRetry keeps the in-call retries of the tests short.
var fastRetry = []time.Duration{time.Millisecond, time.Millisecond}

// assetLab plays GitHub's API and web host on one TLS server reached as
// example.com (the guarded client's host map), and the asset host on a
// second server reached by its own address: the asset endpoint's redirect
// crosses hosts, as api.github.com -> release-assets.githubusercontent.com
// does, so a test sees which request carried the token.
type assetLab struct {
	gh, assets *httptest.Server
	mu         sync.Mutex
	seen       []string       // "<host> <path> <Authorization> <Accept>"
	refuse     string         // how the API answers the server's token: "", "401", "policy", "limit"
	fail       map[string]int // path -> 502 answers left before the real one
	blobs      map[string][]byte
	web        map[string][]byte
}

const labToken = "server-token"

func newAssetLab(t *testing.T) *assetLab {
	t.Helper()
	l := &assetLab{fail: map[string]int{}, blobs: map[string][]byte{
		"7":  []byte("\x00asm\x01\x00\x00\x00 module through the API"),
		"8":  []byte(`{"name":"s3x","version":"1.0.0"}`),
		"9":  []byte("the asset on the second page"),
		"13": []byte("cut short"),
	}, web: map[string][]byte{
		"/o/app/releases/download/v1.0.0/plugin.wasm": []byte("\x00asm\x01\x00\x00\x00 module from the web host"),
	}}
	l.assets = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.note(r)
		id := strings.TrimPrefix(r.URL.Path, "/blob/")
		b, ok := l.blobs[id]
		if !ok || r.URL.Query().Get("sig") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if id == "13" {
			// A body cut short: a Content-Length the answer never reaches.
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(b)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(l.assets.Close)
	l.gh = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.note(r)
		p := r.URL.Path
		l.mu.Lock()
		failing := l.fail[p] > 0
		if failing {
			l.fail[p]--
		}
		refuse := l.refuse
		l.mu.Unlock()
		if !strings.HasPrefix(p, "/repos/") {
			if b, ok := l.web[p]; ok {
				_, _ = w.Write(b)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if failing {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"Server Error"}`))
			return
		}
		if r.Header.Get("Authorization") == "Bearer "+labToken {
			switch refuse {
			case "401":
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"message":"Bad credentials","status":"401"}`))
				return
			case "policy":
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"The 'o' organization forbids access via a fine-grained personal access tokens if the token's lifetime is greater than 366 days.","status":"403"}`))
				return
			case "limit":
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", "1791057362")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
				return
			}
		}
		release := `{"id":11,"tag_name":"v1.0.0","assets":[{"id":7,"name":"plugin.wasm"},{"id":8,"name":"filex-storage.json"},{"id":13,"name":"short.bin"}]}`
		switch {
		case p == "/repos/o/app/releases/tags/v1.0.0", p == "/repos/o/app/releases/latest":
			_, _ = w.Write([]byte(release))
		case p == "/repos/o/many/releases/tags/v1":
			var a []string
			for i := 0; i < 100; i++ {
				a = append(a, fmt.Sprintf(`{"id":%d,"name":"other-%d.bin"}`, 1000+i, i))
			}
			_, _ = w.Write([]byte(`{"id":12,"assets":[` + strings.Join(a, ",") + `]}`))
		case p == "/repos/o/many/releases/12/assets" && r.URL.Query().Get("page") == "1":
			var a []string
			for i := 0; i < 100; i++ {
				a = append(a, fmt.Sprintf(`{"id":%d,"name":"other-%d.bin"}`, 1000+i, i))
			}
			_, _ = w.Write([]byte(`[` + strings.Join(a, ",") + `]`))
		case p == "/repos/o/many/releases/12/assets" && r.URL.Query().Get("page") == "2":
			_, _ = w.Write([]byte(`[{"id":9,"name":"plugin.wasm"}]`))
		case strings.HasPrefix(p, "/repos/o/") && strings.Contains(p, "/releases/assets/"):
			if r.Header.Get("Accept") != "application/octet-stream" {
				w.WriteHeader(http.StatusUnsupportedMediaType)
				return
			}
			id := p[strings.LastIndex(p, "/")+1:]
			http.Redirect(w, r, l.assets.URL+"/blob/"+id+"?sig=signed-and-short-lived", http.StatusFound)
		case p == "/repos/o/limited":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1791057362")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		case p == "/repos/o/flaky":
			_, _ = w.Write([]byte(`{"id":5,"name":"flaky","owner":{"id":1,"login":"o","type":"User"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(l.gh.Close)
	return l
}

func (l *assetLab) note(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, r.Host+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("Accept"))
}

// requests are the requests whose path is path, in order.
func (l *assetLab) requests(path string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, s := range l.seen {
		if f := strings.Fields(s); len(f) > 1 && f[1] == path {
			out = append(out, s)
		}
	}
	return out
}

func (l *assetLab) asked(prefix string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.seen {
		if f := strings.Fields(s); len(f) > 1 && strings.HasPrefix(f[1], prefix) {
			return true
		}
	}
	return false
}

func (l *assetLab) client(t *testing.T, token string) *Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(l.gh.Certificate())
	pool.AddCert(l.assets.Certificate())
	assetsHost := strings.TrimPrefix(l.assets.URL, "https://")
	hc, err := safehttp.Open(safehttp.Config{Env: safehttp.DevelopmentEnv, RootCAs: pool,
		AllowHosts: []string{"example.com", assetsHost},
		Addresses:  func(netip.Addr) error { return nil },
		HostMap:    map[string]string{"example.com:443": strings.TrimPrefix(l.gh.URL, "https://")}})
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{HTTP: hc, APIURL: "https://example.com", RawURL: "https://example.com", WebURL: "https://example.com",
		Token: token, RetryWait: fastRetry})
}

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

const labWasm = "https://example.com/o/app/releases/download/v1.0.0/plugin.wasm"

// With the server's token a release download is read through the REST API:
// the release by its tag, the asset's id, the asset endpoint, whose redirect
// to the asset host goes without the token. The address recorded is the
// manifest's (the one filex downloads).
func TestDownload_AReleaseAssetGoesThroughTheAPIAndTheTokenStaysThere(t *testing.T) {
	l := newAssetLab(t)
	c := l.client(t, labToken)
	ctx := context.Background()
	a, err := c.Download(ctx, labWasm, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if a.SHA256 != sumOf(l.blobs["7"]) || a.Size != int64(len(l.blobs["7"])) || a.Via != ViaAPI || a.URL != labWasm {
		t.Errorf("asset %+v", a)
	}
	if len(a.Hops) != 1 || a.Hops[0] != l.assets.URL+"/blob/7" {
		t.Errorf("hops %v (one redirect, its signed query dropped)", a.Hops)
	}
	if got := l.requests("/repos/o/app/releases/tags/v1.0.0"); len(got) != 1 || !strings.Contains(got[0], "Bearer "+labToken) {
		t.Errorf("release lookup %v", got)
	}
	if got := l.requests("/repos/o/app/releases/assets/7"); len(got) != 1 || !strings.Contains(got[0], "Bearer "+labToken) || !strings.HasSuffix(got[0], "application/octet-stream") {
		t.Errorf("asset endpoint %v", got)
	}
	if got := l.requests("/blob/7"); len(got) != 1 || strings.Contains(got[0], "Bearer") {
		t.Errorf("the asset host must not get the token: %v", got)
	}
	if l.asked("/o/app/releases/download/") {
		t.Error("the anonymous web download was asked as well")
	}

	var buf bytes.Buffer
	a, err = c.DownloadTo(ctx, labWasm, 1<<20, &buf)
	if err != nil || !bytes.Equal(buf.Bytes(), l.blobs["7"]) || a.Via != ViaAPI {
		t.Errorf("DownloadTo: %+v %v (%d bytes)", a, err, buf.Len())
	}
	f, err := c.ReleaseAsset(ctx, "o", "app", "v1.0.0", "filex-storage.json", 1024)
	if err != nil || string(f.Bytes) != string(l.blobs["8"]) || f.SHA256 != sumOf(l.blobs["8"]) ||
		f.URL != "https://example.com/o/app/releases/download/v1.0.0/filex-storage.json" {
		t.Errorf("ReleaseAsset: %+v %v", f, err)
	}
	a, err = c.Download(ctx, "https://example.com/o/app/releases/latest/download/plugin.wasm", 1<<20)
	if err != nil || a.SHA256 != sumOf(l.blobs["7"]) || len(l.requests("/repos/o/app/releases/latest")) != 1 {
		t.Errorf("latest: %+v %v", a, err)
	}
	if _, err := c.ReleaseAsset(ctx, "o", "app", "v1.0.0", "filex-storage.json", 4); !errors.Is(err, safehttp.ErrTooLarge) {
		t.Errorf("over the cap: %v", err)
	}
}

// A release or an asset the API does not have is not found, as the web
// download's 404 is; the web host is not asked behind its back.
func TestDownload_WhatTheReleaseDoesNotHaveIsNotFound(t *testing.T) {
	l := newAssetLab(t)
	c := l.client(t, labToken)
	ctx := context.Background()
	for _, u := range []string{
		"https://example.com/o/app/releases/download/v1.0.0/missing.wasm",
		"https://example.com/o/app/releases/download/v9.9.9/plugin.wasm",
	} {
		_, err := c.Download(ctx, u, 1<<20)
		if !errors.Is(err, ErrNotFound) || Retryable(err) {
			t.Errorf("%s: %v (retryable %v)", u, err, Retryable(err))
		}
	}
	if _, err := c.ReleaseAsset(ctx, "o", "app", "v1.0.0", "nothing.json", 1024); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReleaseAsset: %v", err)
	}
	if l.asked("/o/app/releases/download/") {
		t.Error("a not-found answer must not fall back to the anonymous download")
	}
}

// A release lists 100 assets at most in its own answer: past that, the
// asset list is read page by page.
func TestDownload_ManyAssetsAreReadPageByPage(t *testing.T) {
	l := newAssetLab(t)
	c := l.client(t, labToken)
	a, err := c.Download(context.Background(), "https://example.com/o/many/releases/download/v1/plugin.wasm", 1<<20)
	if err != nil || a.SHA256 != sumOf(l.blobs["9"]) {
		t.Fatalf("%+v %v", a, err)
	}
	if n := len(l.requests("/repos/o/many/releases/12/assets")); n != 2 {
		t.Errorf("asked %d pages, want 2", n)
	}
}

// GitHub refusing the token (revoked, an owner's policy) or limiting it is
// not an answer about the asset: the anonymous web download is made, as
// without a token.
func TestDownload_ARefusedOrLimitedTokenFallsBackToTheAnonymousDownload(t *testing.T) {
	for _, refuse := range []string{"401", "policy", "limit"} {
		l := newAssetLab(t)
		l.refuse = refuse
		c := l.client(t, labToken)
		a, err := c.Download(context.Background(), labWasm, 1<<20)
		if err != nil || a.Via != ViaWeb || a.SHA256 != sumOf(l.web["/o/app/releases/download/v1.0.0/plugin.wasm"]) {
			t.Errorf("%s: %+v %v", refuse, a, err)
			continue
		}
		if got := l.requests("/o/app/releases/download/v1.0.0/plugin.wasm"); len(got) != 1 || strings.Contains(got[0], "Bearer") {
			t.Errorf("%s: the web download %v", refuse, got)
		}
	}
}

// Without a token nothing asks the API: the reads are the anonymous ones.
func TestDownload_WithoutATokenNothingAsksTheAPI(t *testing.T) {
	l := newAssetLab(t)
	c := l.client(t, "")
	a, err := c.Download(context.Background(), labWasm, 1<<20)
	if err != nil || a.Via != ViaWeb || a.SHA256 != sumOf(l.web["/o/app/releases/download/v1.0.0/plugin.wasm"]) {
		t.Fatalf("%+v %v", a, err)
	}
	if l.asked("/repos/") {
		t.Error("an anonymous client asked the API for a download")
	}
	if c.Authenticated() || !l.client(t, labToken).Authenticated() {
		t.Error("Authenticated says whether the server has a token")
	}
}

// A failure worth retrying (GitHub's 5xx) is tried again in the call, up to
// three tries; then it is the pipeline's, still retryable. A rate limit is
// not tried again at once, and neither is a person's own read.
func TestTry_AFailureWorthItIsTriedAgainInTheCall(t *testing.T) {
	ctx := context.Background()
	l := newAssetLab(t)
	l.fail["/repos/o/app/releases/tags/v1.0.0"] = 2
	c := l.client(t, labToken)
	if a, err := c.Download(ctx, labWasm, 1<<20); err != nil || a.Via != ViaAPI {
		t.Fatalf("two 502s, then the answer: %+v %v", a, err)
	}
	if n := len(l.requests("/repos/o/app/releases/tags/v1.0.0")); n != 3 {
		t.Errorf("tries %d, want 3", n)
	}

	l = newAssetLab(t)
	l.fail["/repos/o/app/releases/tags/v1.0.0"] = 3
	c = l.client(t, labToken)
	_, err := c.Download(ctx, labWasm, 1<<20)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusBadGateway || !Retryable(err) {
		t.Errorf("three 502s: %v", err)
	}
	if l.asked("/o/app/releases/download/") {
		t.Error("a 5xx of the API is not a reason for the anonymous download")
	}

	_, err = c.Repository(ctx, "", "o", "limited")
	var rl *RateLimitError
	if !errors.As(err, &rl) || len(l.requests("/repos/o/limited")) != 1 {
		t.Errorf("a rate limit: %v, asked %d times", err, len(l.requests("/repos/o/limited")))
	}

	l.fail["/repos/o/flaky"] = 1
	if _, err := c.Repository(ctx, "user-token", "o", "flaky"); !errors.As(err, &ae) || ae.Status != http.StatusBadGateway {
		t.Errorf("a person's read is tried once: %v", err)
	}
	l.fail["/repos/o/flaky"] = 1
	if r, err := c.Repository(ctx, "", "o", "flaky"); err != nil || r.ID != 5 {
		t.Errorf("a background read is tried again: %+v %v", r, err)
	}
}

// A download that already handed bytes to its writer is not repeated: the
// writer (a build on its way to the plugin validator) would hold two
// copies. Hashing alone is repeated.
func TestDownloadTo_ATryThatWroteBytesIsNotRepeated(t *testing.T) {
	l := newAssetLab(t)
	c := l.client(t, labToken)
	const short = "https://example.com/o/app/releases/download/v1.0.0/short.bin"
	var buf bytes.Buffer
	if _, err := c.DownloadTo(context.Background(), short, 1<<20, &buf); err == nil || !Retryable(err) {
		t.Fatalf("a body cut short: %v", err)
	}
	if n := len(l.requests("/blob/13")); n != 1 {
		t.Errorf("DownloadTo asked %d times after writing %d bytes, want 1", n, buf.Len())
	}
	if _, err := c.Download(context.Background(), short, 1<<20); err == nil {
		t.Fatal("a body cut short must fail")
	}
	if n := len(l.requests("/blob/13")); n != 4 {
		t.Errorf("Download (hash only) asked %d times in all, want 1 + 3", n)
	}
}

// Which addresses are a release's assets on the web host, and so read
// through the API; everything else is read from the address itself.
func TestReleaseDownload_TheAddressesThatAreReleaseAssets(t *testing.T) {
	c := New(Options{})
	for in, want := range map[string]releaseRef{
		"https://github.com/BRF-Tech/filex-sign/releases/download/v0.3.1/plugin.wasm": {"BRF-Tech", "filex-sign", "v0.3.1", "plugin.wasm"},
		"https://GitHub.com/o/n/releases/download/v1.0.0-rc.1/ui.zip":                 {"o", "n", "v1.0.0-rc.1", "ui.zip"},
		"https://github.com/o/n/releases/latest/download/ui.zip":                      {"o", "n", "", "ui.zip"},
		"https://github.com/o/n/releases/download/latest/ui.zip":                      {"o", "n", "latest", "ui.zip"},
		"https://github.com/o/n/releases/download/v1/a%20b.zip":                       {"o", "n", "v1", "a b.zip"},
	} {
		got, ok := c.releaseDownload(in)
		if !ok || got != want {
			t.Errorf("%s = %+v %v, want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"https://raw.githubusercontent.com/o/n/v1/plugin.wasm",
		"http://github.com/o/n/releases/download/v1/x",
		"https://github.com/o/n/releases/download/release%2F1.0/x",
		"https://github.com/o/n/releases/download/release/1.0/x",
		"https://github.com/o/n/releases/download/v1/x?y=1",
		"https://github.com/o/n/releases/download/v1/",
		"https://github.com/o/n/archive/refs/tags/v1.zip",
		"https://github.com/-o/n/releases/download/v1/x",
		"https://evil.example/o/n/releases/download/v1/x",
		"https://github.com:8443/o/n/releases/download/v1/x",
	} {
		if got, ok := c.releaseDownload(in); ok {
			t.Errorf("%s = %+v: not a release asset address", in, got)
		}
	}
}
