// Package safehttp is the store's one guarded outbound HTTP client. Every
// byte the store reads from outside (GitHub's API, raw files, release
// assets, media) comes through it, because the addresses come from a
// publisher's manifest and consent file: an address is data an attacker may
// write (docs/ARCHITECTURE.md "Security", SSRF).
//
// The rules, each enforced here and nowhere else:
//
//   - HTTPS only, on every hop (a redirect to http is refused).
//   - The host must be on the allow list (config.DefaultAllowHosts by
//     default); a port other than 443 must be listed as "host:port". A client
//     made with AnyHost (the notification webhooks and push services: an
//     address a person typed or a browser handed over) has no list; every
//     other rule holds for it.
//   - After DNS, the address dialled must be public: loopback, private,
//     link-local, CGNAT, documentation, benchmark, multicast, reserved and the
//     IPv6 forms that carry them (mapped, NAT64, 6to4, Teredo) are refused in
//     the dialer itself, so a name that resolves to 127.0.0.1 - or a redirect
//     to one - is caught on every connection, not only on the first look.
//   - Every redirect is re-checked, at most MaxRedirects of them.
//   - No proxy from the environment (it would dial for us and skip the
//     address check).
//   - Size caps: a response over the cap is refused (never truncated), by
//     Content-Length when the server sends one and while streaming otherwise.
//   - Large downloads are streamed into sha256 (Hash): nothing big is held in
//     memory.
//
// Three test modes exist for the end-to-end stack and local development, all
// refused by Open unless APP_ENV is "development": CAFile (an extra trusted
// CA), AllowPrivate (non-public addresses may be dialled) and AllowHTTP
// (plain http, for a webhook receiver on the developer's machine). The first
// two relax neither HTTPS nor the allow list.
package safehttp

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// Refusals. Callers switch on them with errors.Is to pick a check's code.
var (
	ErrNotHTTPS         = errors.New("only https addresses are fetched")
	ErrHostNotAllowed   = errors.New("host is not on the allow list")
	ErrPrivateAddress   = errors.New("address is not public")
	ErrTooLarge         = errors.New("response is larger than allowed")
	ErrTooManyRedirects = errors.New("too many redirects")
	ErrBadURL           = errors.New("not a valid address")
)

// StatusError is a non-2xx answer.
type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("http %d from %s", e.Code, hostOf(e.URL))
}

// Refused reports whether err is one of the guard's refusals (the address,
// not the network, is the problem): not worth retrying.
func Refused(err error) bool {
	for _, e := range []error{ErrNotHTTPS, ErrHostNotAllowed, ErrPrivateAddress, ErrTooLarge, ErrTooManyRedirects, ErrBadURL} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// Config builds a Client.
type Config struct {
	// AllowHosts are host names (lower case), or "host:port" for a port
	// other than 443.
	AllowHosts []string
	// AnyHost drops the allow list: any host name, any port. HTTPS, the
	// public-address rule (in the dialer, on every hop) and the redirect
	// checks stay. For addresses that cannot be listed in advance: a
	// person's webhook, a browser's push service.
	AnyHost bool
	// Timeout bounds one request, body included (default 60 s).
	Timeout time.Duration
	// MaxRedirects is how many redirects are followed (default 5).
	MaxRedirects int
	// UserAgent is sent with every request.
	UserAgent string

	// Env is APP_ENV. The two test modes below are honoured only when it is
	// "development"; Open refuses them in any other environment, so a demo
	// or a production server with either set does not start.
	Env string
	// CAFile adds the certificates of a PEM file to the trusted roots (the
	// system pool stays): the end-to-end stack's throw-away CA
	// (FAPPS_FETCH_CA_FILE). Development only.
	CAFile string
	// AllowPrivate lets the dialer reach addresses that are not public: the
	// end-to-end mocks on 127.0.0.1 and the compose network
	// (FAPPS_FETCH_ALLOW_PRIVATE=1). HTTPS and the allow list still hold;
	// unspecified and multicast addresses are still refused. Development
	// only.
	AllowPrivate bool
	// AllowHTTP accepts plain http (and a redirect to it): a webhook
	// receiver on the developer's own machine. Development only.
	AllowHTTP bool

	// Addresses decides which dialled addresses are allowed; nil means
	// PublicOnly (or AllowPrivate's rule). Unit tests that talk to an
	// httptest server on 127.0.0.1 pass their own; the server never sets it.
	Addresses func(netip.Addr) error
	// RootCAs replaces the system pool (unit tests: the httptest server's
	// certificate). Nil means the system pool.
	RootCAs *x509.CertPool
	// HostMap dials another "host:port" for a "host:port" (integration
	// tests only: GitHub's own names served by a local server, so recorded
	// real tags keep their real addresses). The address check runs on the
	// address actually dialled. Development only; no setting reaches it.
	HostMap map[string]string
}

// ErrTestModeRefused: a test mode (CAFile, AllowPrivate) outside
// APP_ENV=development, or one that cannot be honoured.
var ErrTestModeRefused = errors.New("test mode refused")

// DevelopmentEnv is the only APP_ENV in which the test modes are honoured.
const DevelopmentEnv = "development"

// Client is the guarded client. Safe for concurrent use.
type Client struct {
	allow    map[string]bool
	anyHost  bool
	plain    bool
	ua       string
	maxRedir int
	base     *http.Client
}

// Open builds a guarded client, refusing the test modes outside
// development and a CA file that does not hold a certificate. The server
// calls it at start and stops on its error.
func Open(cfg Config) (*Client, error) {
	if cfg.CAFile != "" || cfg.AllowPrivate || cfg.AllowHTTP || len(cfg.HostMap) > 0 {
		if cfg.Env != DevelopmentEnv {
			var modes []string
			if cfg.CAFile != "" {
				modes = append(modes, "FAPPS_FETCH_CA_FILE")
			}
			if cfg.AllowPrivate {
				modes = append(modes, "FAPPS_FETCH_ALLOW_PRIVATE")
			}
			if cfg.AllowHTTP {
				modes = append(modes, "plain http")
			}
			if len(cfg.HostMap) > 0 {
				modes = append(modes, "a host map")
			}
			return nil, fmt.Errorf("%w: %s only with APP_ENV=%s (this is %q)", ErrTestModeRefused, strings.Join(modes, " and "), DevelopmentEnv, cfg.Env)
		}
	}
	roots := cfg.RootCAs
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("%w: FAPPS_FETCH_CA_FILE: %v", ErrTestModeRefused, err)
		}
		if roots == nil {
			if roots, err = x509.SystemCertPool(); err != nil || roots == nil {
				roots = x509.NewCertPool()
			}
		} else {
			roots = roots.Clone()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%w: FAPPS_FETCH_CA_FILE %s holds no PEM certificate", ErrTestModeRefused, cfg.CAFile)
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.MaxRedirects <= 0 {
		cfg.MaxRedirects = 5
	}
	check := cfg.Addresses
	switch {
	case check != nil:
	case cfg.AllowPrivate:
		check = anyUnicast
	default:
		check = PublicOnly
	}
	allow := make(map[string]bool, len(cfg.AllowHosts))
	for _, h := range cfg.AllowHosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			allow[h] = true
		}
	}
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		// ⚠ The address is checked HERE, after DNS, for every connection the
		// transport opens (each redirect hop, each retry, each address of a
		// name): checking the name before the request would let a name that
		// resolves to a private address, or changes its answer between the
		// check and the dial, through.
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrPrivateAddress, address)
			}
			return check(ap.Addr())
		},
	}
	dial := dialer.DialContext
	if len(cfg.HostMap) > 0 {
		hm := make(map[string]string, len(cfg.HostMap))
		for from, to := range cfg.HostMap {
			hm[strings.ToLower(from)] = to
		}
		dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if to, ok := hm[strings.ToLower(addr)]; ok {
				addr = to
			}
			return dialer.DialContext(ctx, network, addr)
		}
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
	}
	return &Client{
		allow:    allow,
		anyHost:  cfg.AnyHost,
		plain:    cfg.AllowHTTP,
		ua:       cfg.UserAgent,
		maxRedir: cfg.MaxRedirects,
		base:     &http.Client{Transport: tr, Timeout: cfg.Timeout},
	}, nil
}

// New builds a guarded client for a configuration without test modes (unit
// tests, tools). It panics where Open returns an error: a test mode must
// never be dropped silently. The server uses Open.
func New(cfg Config) *Client {
	c, err := Open(cfg)
	if err != nil {
		panic("safehttp: " + err.Error())
	}
	return c
}

// anyUnicast is AllowPrivate's address rule: everything but the
// unspecified and multicast addresses.
func anyUnicast(a netip.Addr) error {
	a = a.Unmap()
	if !a.IsValid() || a.IsUnspecified() || a.IsMulticast() {
		return fmt.Errorf("%w: %s", ErrPrivateAddress, a)
	}
	return nil
}

// CheckURL applies the scheme and allow-list rules to one address.
func (c *Client) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrBadURL, raw)
	}
	return u, c.checkParsed(u)
}

func (c *Client) checkParsed(u *url.URL) error {
	if u.Scheme != "https" && !(c.plain && u.Scheme == "http") {
		return fmt.Errorf("%w: %s", ErrNotHTTPS, u.Redacted())
	}
	if u.User != nil {
		return fmt.Errorf("%w: credentials in the address", ErrBadURL)
	}
	if c.anyHost {
		if u.Hostname() == "" {
			return fmt.Errorf("%w: no host", ErrBadURL)
		}
		return nil
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	key := host
	if port != "" && port != "443" {
		key = net.JoinHostPort(host, port)
	}
	if !c.allow[key] {
		return fmt.Errorf("%w: %s", ErrHostNotAllowed, key)
	}
	return nil
}

// Response is a successful answer whose body the caller reads and closes.
// Hops are the addresses of every redirect followed, in order.
type Response struct {
	*http.Response
	Hops []string
}

// Do sends req through the guard. A non-2xx answer is returned as is (the
// caller decides: the GitHub API's 404 is an answer); the caller closes the
// body.
func (c *Client) Do(req *http.Request) (*Response, error) {
	if err := c.checkParsed(req.URL); err != nil {
		return nil, err
	}
	if c.ua != "" && req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.ua)
	}
	var hops []string
	hc := *c.base
	hc.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > c.maxRedir {
			return ErrTooManyRedirects
		}
		if err := c.checkParsed(next.URL); err != nil {
			return err
		}
		hops = append(hops, redact(next.URL))
		return nil
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && ue.Err != nil {
			err = ue.Err
		}
		// A refusal inside the dialer comes back wrapped in a *net.OpError.
		var oe *net.OpError
		if errors.As(err, &oe) && errors.Is(oe.Err, ErrPrivateAddress) {
			return nil, oe.Err
		}
		return nil, err
	}
	return &Response{Response: resp, Hops: hops}, nil
}

// Body is a small download held in memory.
type Body struct {
	Bytes       []byte
	SHA256      string
	ContentType string
	Hops        []string
}

// Get downloads raw up to max bytes. A non-2xx answer is a *StatusError; an
// answer over max is ErrTooLarge (never a truncated body).
func (c *Client) Get(ctx context.Context, raw string, max int64, hdr http.Header) (*Body, error) {
	resp, err := c.get(ctx, raw, max, hdr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	h := sha256.New()
	b, err := io.ReadAll(io.TeeReader(io.LimitReader(resp.Body, max+1), h))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w: over %d bytes", ErrTooLarge, max)
	}
	return &Body{Bytes: b, SHA256: hex.EncodeToString(h.Sum(nil)), ContentType: resp.Header.Get("Content-Type"), Hops: resp.Hops}, nil
}

// Digest is a streamed download: its sha256 and size, nothing kept.
type Digest struct {
	SHA256 string
	Size   int64
	Hops   []string
}

// Hash streams raw into sha256, refusing more than max bytes.
func (c *Client) Hash(ctx context.Context, raw string, max int64) (*Digest, error) {
	resp, err := c.get(ctx, raw, max, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if n > max {
		return nil, fmt.Errorf("%w: over %d bytes", ErrTooLarge, max)
	}
	return &Digest{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, Hops: resp.Hops}, nil
}

// CopyTo streams raw into w and into sha256, refusing more than max bytes:
// what Hash does, for a caller that keeps the bytes (a storage plugin's
// build handed to the plugin validator). On ErrTooLarge w has received up
// to max+1 bytes; the caller discards them.
func (c *Client) CopyTo(ctx context.Context, raw string, max int64, w io.Writer) (*Digest, error) {
	resp, err := c.get(ctx, raw, max, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if n > max {
		return nil, fmt.Errorf("%w: over %d bytes", ErrTooLarge, max)
	}
	return &Digest{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n, Hops: resp.Hops}, nil
}

func (c *Client) get(ctx context.Context, raw string, max int64, hdr http.Header) (*Response, error) {
	u, err := c.CheckURL(raw)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadURL, err)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, &StatusError{Code: resp.StatusCode, URL: redact(resp.Request.URL)}
	}
	if resp.ContentLength > max {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %d bytes announced, at most %d", ErrTooLarge, resp.ContentLength, max)
	}
	return resp, nil
}

// redact drops the query (release-asset redirects carry signed, short-lived
// query strings that do not belong in logs or check rows).
func redact(u *url.URL) string {
	c := *u
	c.RawQuery = ""
	c.Fragment = ""
	c.User = nil
	return c.String()
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// blocked are the ranges no download may reach, beyond what netip's own
// predicates catch (loopback, private, link-local, multicast, unspecified).
var blocked = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",          // "this network"
		"100.64.0.0/10",      // CGNAT (also the WARP mesh, 100.96.0.0/12)
		"192.0.0.0/24",       // IETF protocol assignments
		"192.0.2.0/24",       // TEST-NET-1
		"198.18.0.0/15",      // benchmarking
		"198.51.100.0/24",    // TEST-NET-2
		"203.0.113.0/24",     // TEST-NET-3
		"240.0.0.0/4",        // reserved, and the broadcast address
		"100::/64",           // discard
		"2001::/32",          // Teredo (tunnels to an IPv4 address)
		"2001:db8::/32",      // documentation
		"2002::/16",          // 6to4 (carries an IPv4 address)
		"64:ff9b::/96",       // NAT64 (carries an IPv4 address)
		"64:ff9b:1::/48",     // local-use NAT64
		"::/96",              // IPv4-compatible (deprecated)
		"fec0::/10",          // site-local (deprecated)
		"169.254.169.254/32", // cloud metadata (also link-local; named for the reader)
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// PublicOnly is the default address policy: only a public unicast address
// may be dialled.
func PublicOnly(a netip.Addr) error {
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() ||
		a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() {
		return fmt.Errorf("%w: %s", ErrPrivateAddress, a)
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return fmt.Errorf("%w: %s", ErrPrivateAddress, a)
		}
	}
	return nil
}
