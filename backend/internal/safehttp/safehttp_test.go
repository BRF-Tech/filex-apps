package safehttp

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPublicOnly_RefusesEveryNonPublicRange(t *testing.T) {
	refused := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"10.0.0.1", "172.16.5.4", "192.168.0.199",
		"169.254.169.254", "169.254.1.1", "fe80::1",
		"100.64.0.1", "100.96.0.9", "100.127.255.254",
		"0.0.0.0", "0.1.2.3", "::",
		"224.0.0.1", "ff02::1", "255.255.255.255", "240.0.0.1",
		"192.0.0.8", "192.0.2.1", "198.18.0.1", "198.51.100.7", "203.0.113.9",
		"fc00::1", "fd12:3456::1", "fec0::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"64:ff9b::7f00:1", "64:ff9b:1::1", "2002:7f00:1::1", "2001:0:4136:e378::1",
		"2001:db8::1", "100::1", "::7f00:1",
	}
	for _, s := range refused {
		if err := PublicOnly(netip.MustParseAddr(s)); !errors.Is(err, ErrPrivateAddress) {
			t.Errorf("%s: got %v, want ErrPrivateAddress", s, err)
		}
	}
	// GitHub's addresses as measured on 2026-10-03, and IPv6 ones.
	for _, s := range []string{"185.199.108.133", "185.199.111.133", "140.82.121.4", "8.8.8.8", "2606:50c0:8000::154", "::ffff:185.199.108.133"} {
		if err := PublicOnly(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s: %v, want allowed", s, err)
		}
	}
}

// lab is two TLS servers on loopback and a client that trusts them; policy
// decides which dialled addresses pass (loopback only reaches them when the
// test says so - production never does).
type lab struct {
	a, b   *httptest.Server
	pool   *x509.CertPool
	mu     sync.Mutex
	dialed []string
}

func newLab(t *testing.T, ha, hb http.Handler) *lab {
	t.Helper()
	l := &lab{a: httptest.NewTLSServer(ha), b: httptest.NewTLSServer(hb)}
	t.Cleanup(l.a.Close)
	t.Cleanup(l.b.Close)
	l.pool = x509.NewCertPool()
	l.pool.AddCert(l.a.Certificate())
	l.pool.AddCert(l.b.Certificate())
	return l
}

func (l *lab) hostport(s *httptest.Server) string {
	u, _ := url.Parse(s.URL)
	return u.Host
}

func (l *lab) client(allow []string, permissive bool) *Client {
	cfg := Config{AllowHosts: allow, RootCAs: l.pool, UserAgent: "fapps-test"}
	if permissive {
		cfg.Addresses = func(a netip.Addr) error {
			l.mu.Lock()
			l.dialed = append(l.dialed, a.String())
			l.mu.Unlock()
			return nil
		}
	}
	return New(cfg)
}

func ok(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
}

func TestGet_RefusesPlainHTTPAndUnlistedHostsWithoutDialling(t *testing.T) {
	l := newLab(t, ok("a"), ok("b"))
	c := l.client([]string{"github.com", l.hostport(l.a)}, true)
	ctx := context.Background()
	if _, err := c.Get(ctx, "http://github.com/x", 10, nil); !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("http: %v", err)
	}
	if _, err := c.Get(ctx, l.b.URL+"/x", 10, nil); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("unlisted: %v", err)
	}
	if _, err := c.Get(ctx, "https://user:pw@github.com/x", 10, nil); !errors.Is(err, ErrBadURL) {
		t.Errorf("credentials: %v", err)
	}
	if _, err := c.Get(ctx, "https://github.com:8443/x", 10, nil); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("other port of a listed host: %v", err)
	}
	if _, err := c.Get(ctx, "github.com/x", 10, nil); err == nil {
		t.Error("an address without a scheme must be refused")
	}
	if len(l.dialed) != 0 {
		t.Errorf("dialled %v for refused addresses", l.dialed)
	}
}

// The heart of the guard: an allow-listed name that resolves to loopback is
// refused by the dialer, after DNS.
func TestGet_RefusesALoopbackAddressAfterDNS(t *testing.T) {
	l := newLab(t, ok("secret"), ok("b"))
	_, port, _ := net.SplitHostPort(l.hostport(l.a))
	c := l.client([]string{"localhost:" + port, l.hostport(l.a)}, false)
	for _, u := range []string{"https://localhost:" + port + "/", l.a.URL + "/"} {
		_, err := c.Get(context.Background(), u, 100, nil)
		if !errors.Is(err, ErrPrivateAddress) {
			t.Errorf("%s: got %v, want ErrPrivateAddress", u, err)
		}
		if !Refused(err) {
			t.Errorf("%s: Refused(%v) = false", u, err)
		}
	}
}

func TestGet_FollowsAnAllowedRedirectCheckingEveryHop(t *testing.T) {
	var l *lab
	l = newLab(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, l.b.URL+"/asset?X-Amz-Signature=secret", http.StatusFound)
	}), ok("payload"))
	c := l.client([]string{l.hostport(l.a), l.hostport(l.b)}, true)
	body, err := c.Get(context.Background(), l.a.URL+"/download", 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(body.Bytes) != "payload" {
		t.Errorf("body %q", body.Bytes)
	}
	if len(body.Hops) != 1 || body.Hops[0] != l.b.URL+"/asset" {
		t.Errorf("hops %v (the signed query must not be kept)", body.Hops)
	}
	if len(l.dialed) < 2 {
		t.Errorf("the address check ran %d times for two hosts: %v", len(l.dialed), l.dialed)
	}
}

func TestGet_RefusesARedirectOffTheListOrToHTTP(t *testing.T) {
	var target string
	l := newLab(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}), ok("b"))
	c := l.client([]string{l.hostport(l.a)}, true)
	target = l.b.URL + "/x"
	if _, err := c.Get(context.Background(), l.a.URL, 100, nil); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("off-list redirect: %v", err)
	}
	target = "http://" + l.hostport(l.a) + "/x"
	if _, err := c.Get(context.Background(), l.a.URL, 100, nil); !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("https to http: %v", err)
	}
}

func TestGet_StopsAfterMaxRedirects(t *testing.T) {
	var l *lab
	n := 0
	l = newLab(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Redirect(w, r, l.a.URL+"/again", http.StatusFound)
	}), ok("b"))
	c := l.client([]string{l.hostport(l.a)}, true)
	if _, err := c.Get(context.Background(), l.a.URL, 100, nil); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("got %v", err)
	}
	if n != 6 {
		t.Errorf("served %d answers, want 6 (the request and 5 redirects)", n)
	}
}

func TestGet_RefusesOversizedBodiesNeverTruncates(t *testing.T) {
	l := newLab(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chunked" {
			w.(http.Flusher).Flush()
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 11)))
	}), ok("b"))
	c := l.client([]string{l.hostport(l.a)}, true)
	for _, p := range []string{"/sized", "/chunked"} {
		if _, err := c.Get(context.Background(), l.a.URL+p, 10, nil); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Get %s: %v", p, err)
		}
		if _, err := c.Hash(context.Background(), l.a.URL+p, 10); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Hash %s: %v", p, err)
		}
		if b, err := c.Get(context.Background(), l.a.URL+p, 11, nil); err != nil || len(b.Bytes) != 11 {
			t.Errorf("Get %s at the cap: %v", p, err)
		}
	}
}

func TestHash_StreamsTheDigestAndSize(t *testing.T) {
	payload := strings.Repeat("filex", 200000)
	l := newLab(t, ok(payload), ok("b"))
	c := l.client([]string{l.hostport(l.a)}, true)
	d, err := c.Hash(context.Background(), l.a.URL+"/plugin.wasm", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(payload))
	if d.SHA256 != hex.EncodeToString(sum[:]) || d.Size != int64(len(payload)) {
		t.Errorf("got %s/%d", d.SHA256, d.Size)
	}
}

func TestGet_NotFoundIsAStatusError(t *testing.T) {
	l := newLab(t, http.NotFoundHandler(), ok("b"))
	c := l.client([]string{l.hostport(l.a)}, true)
	_, err := c.Get(context.Background(), l.a.URL+"/nope", 10, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 {
		t.Fatalf("got %v", err)
	}
	if Refused(err) {
		t.Error("a 404 is an answer, not a refusal of the address")
	}
}

// A proxy from the environment would dial for us and skip the address
// check. (Go's own ProxyFromEnvironment never proxies loopback, so an
// httptest server cannot show it: the transport itself is checked.)
func TestNew_TakesNoProxyFromTheEnvironment(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	c := New(Config{AllowHosts: []string{"github.com"}})
	tr, ok := c.base.Transport.(*http.Transport)
	if !ok || tr.Proxy != nil {
		t.Fatal("the transport must not take a proxy from the environment")
	}
}

// writeCA writes the httptest server's certificate as a PEM file (it is
// self-signed, so it is its own CA).
func writeCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The end-to-end stack's two test modes: honoured in development only, and
// refused loudly anywhere else (the server does not start).
func TestOpen_TestModesOnlyInDevelopment(t *testing.T) {
	srv := httptest.NewTLSServer(ok("mock github"))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")
	ca := writeCA(t, srv)
	for _, env := range []string{"demo", "production", "", "Development"} {
		for name, cfg := range map[string]Config{
			"CAFile":       {Env: env, AllowHosts: []string{host}, CAFile: ca},
			"AllowPrivate": {Env: env, AllowHosts: []string{host}, AllowPrivate: true},
		} {
			c, err := Open(cfg)
			if !errors.Is(err, ErrTestModeRefused) || c != nil {
				t.Errorf("%s with APP_ENV=%q: %v", name, env, err)
			}
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("New must panic on a refused test mode, never drop it")
			}
		}()
		New(Config{Env: "demo", AllowPrivate: true})
	}()

	ctx := context.Background()
	dev, err := Open(Config{Env: DevelopmentEnv, AllowHosts: []string{host}, CAFile: ca, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := dev.Get(ctx, srv.URL+"/x", 100, nil)
	if err != nil || string(b.Bytes) != "mock github" {
		t.Fatalf("development, both modes: %v", err)
	}
	// Each mode does its own part: without AllowPrivate the loopback mock is
	// refused; without CAFile its certificate is not trusted.
	noPriv, err := Open(Config{Env: DevelopmentEnv, AllowHosts: []string{host}, CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noPriv.Get(ctx, srv.URL+"/x", 100, nil); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("CAFile alone: %v, want ErrPrivateAddress", err)
	}
	noCA, err := Open(Config{Env: DevelopmentEnv, AllowHosts: []string{host}, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	var unknown x509.UnknownAuthorityError
	if _, err := noCA.Get(ctx, srv.URL+"/x", 100, nil); !errors.As(err, &unknown) {
		t.Errorf("AllowPrivate alone: %v, want an unknown authority", err)
	}
	// Neither mode relaxes HTTPS or the allow list.
	if _, err := dev.Get(ctx, "http://"+host+"/x", 100, nil); !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("plain http in development: %v", err)
	}
	if _, err := dev.Get(ctx, "https://127.0.0.1:1/x", 100, nil); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("unlisted host in development: %v", err)
	}
	// A CA file that is missing or holds no certificate stops the start.
	bad := filepath.Join(t.TempDir(), "bad.pem")
	_ = os.WriteFile(bad, []byte("not a certificate"), 0o600)
	for _, f := range []string{bad, filepath.Join(t.TempDir(), "missing.pem")} {
		if _, err := Open(Config{Env: DevelopmentEnv, CAFile: f}); !errors.Is(err, ErrTestModeRefused) {
			t.Errorf("%s: %v", f, err)
		}
	}
	// Without test modes every environment opens.
	for _, env := range []string{"demo", "production", DevelopmentEnv} {
		if _, err := Open(Config{Env: env, AllowHosts: []string{"github.com"}}); err != nil {
			t.Errorf("%s: %v", env, err)
		}
	}
}

func TestAnyUnicast_StillRefusesUnspecifiedAndMulticast(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.18.0.5", "::1", "185.199.108.133"} {
		if err := anyUnicast(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{"0.0.0.0", "::", "224.0.0.1", "ff02::1"} {
		if err := anyUnicast(netip.MustParseAddr(s)); !errors.Is(err, ErrPrivateAddress) {
			t.Errorf("%s: %v", s, err)
		}
	}
}

// The host map dials a local server for GitHub's own names; the address
// check still runs on what is dialled, and the map is development only.
func TestOpen_HostMapDialsTheMappedAddressStillChecked(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("served for " + r.Host))
	}))
	t.Cleanup(srv.Close)
	local := strings.TrimPrefix(srv.URL, "https://")
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	cfg := Config{Env: DevelopmentEnv, AllowHosts: []string{"example.com"}, RootCAs: pool, AllowPrivate: true,
		HostMap: map[string]string{"Example.com:443": local}}
	c, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Get(context.Background(), "https://example.com/x", 100, nil)
	if err != nil || string(b.Bytes) != "served for example.com" {
		t.Fatalf("%v %q", err, b)
	}
	cfg.AllowPrivate = false
	c, _ = Open(cfg)
	if _, err := c.Get(context.Background(), "https://example.com/x", 100, nil); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("the mapped loopback address must still be checked: %v", err)
	}
	cfg.Env = "demo"
	cfg.AllowPrivate = false
	if _, err := Open(cfg); !errors.Is(err, ErrTestModeRefused) {
		t.Errorf("a host map outside development: %v", err)
	}
}

// AnyHost (the notification webhooks): no allow list, but HTTPS, the
// public-address rule on every hop and the redirect checks hold.
func TestAnyHost_DropsTheListKeepsEveryOtherRule(t *testing.T) {
	var target string
	l := newLab(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if target != "" {
			http.Redirect(w, r, target, http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write([]byte("hook"))
	}), ok("b"))
	ctx := context.Background()
	// Only 127.0.0.1 is "public" here: 127.0.0.2 stands for a private address.
	only1 := func(a netip.Addr) error {
		if a.Unmap() != netip.MustParseAddr("127.0.0.1") {
			return ErrPrivateAddress
		}
		return nil
	}
	c := New(Config{AnyHost: true, RootCAs: l.pool, Addresses: only1})
	if b, err := c.Get(ctx, l.a.URL+"/x", 100, nil); err != nil || string(b.Bytes) != "hook" {
		t.Fatalf("an unlisted host on any port: %v", err)
	}
	if _, err := c.Get(ctx, "http://"+l.hostport(l.a)+"/x", 100, nil); !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("plain http: %v", err)
	}
	if _, err := c.Get(ctx, "https://user:pw@"+l.hostport(l.a)+"/x", 100, nil); !errors.Is(err, ErrBadURL) {
		t.Errorf("credentials: %v", err)
	}
	_, port, _ := net.SplitHostPort(l.hostport(l.a))
	target = "https://127.0.0.2:" + port + "/x"
	if _, err := c.Get(ctx, l.a.URL+"/x", 100, nil); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("a redirect to a private address: %v", err)
	}
	target = "http://" + l.hostport(l.a) + "/x"
	if _, err := c.Get(ctx, l.a.URL+"/x", 100, nil); !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("a redirect to http: %v", err)
	}
	// The default address rule: loopback is refused even with no list.
	target = ""
	strict := New(Config{AnyHost: true, RootCAs: l.pool})
	if _, err := strict.Get(ctx, l.a.URL+"/x", 100, nil); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("loopback with the default rule: %v", err)
	}
}

// AllowHTTP is a development mode like the others.
func TestAllowHTTP_OnlyInDevelopment(t *testing.T) {
	for _, env := range []string{"production", "demo", ""} {
		if c, err := Open(Config{Env: env, AnyHost: true, AllowHTTP: true}); !errors.Is(err, ErrTestModeRefused) || c != nil {
			t.Errorf("APP_ENV=%q: %v", env, err)
		}
	}
	srv := httptest.NewServer(ok("plain"))
	t.Cleanup(srv.Close)
	c, err := Open(Config{Env: DevelopmentEnv, AnyHost: true, AllowHTTP: true, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := c.Get(context.Background(), srv.URL+"/x", 100, nil); err != nil || string(b.Bytes) != "plain" {
		t.Fatalf("development, plain http: %v", err)
	}
}
