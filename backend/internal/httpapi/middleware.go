package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/session"
)

type ctxKey int

const stateKey ctxKey = 1

// reqState is what the middleware learns about a request once.
type reqState struct {
	id     string
	ip     string
	loaded bool
	p      *session.Principal
	err    error
}

func state(r *http.Request) *reqState {
	if s, ok := r.Context().Value(stateKey).(*reqState); ok {
		return s
	}
	return &reqState{}
}

func requestID(r *http.Request) string { return state(r).id }

// clientIP is the request's client address (see Config.TrustedProxies).
func clientIP(r *http.Request) string { return state(r).ip }

var ridRe = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// securityHeaders sets the headers every answer carries.
func (h *handler) securityHeaders(next http.Handler) http.Handler {
	hsts := strings.HasPrefix(h.cfg.PublicURL, "https://")
	csp := contentSecurityPolicy(h.cfg.AnalyticsOrigin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		hd.Set("Content-Security-Policy", csp)
		if hsts {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy is the page's CSP. The SPA loads nothing from
// another origin: no CDN, no inline script. The one exception is the
// optional page counter (config.Config.AnalyticsOrigin): the reverse proxy
// adds its script, which loads from that origin and sends the counts back
// to it.
func contentSecurityPolicy(analyticsOrigin string) string {
	script, connect := "script-src 'self'", "connect-src 'self'"
	if analyticsOrigin != "" {
		script += " " + analyticsOrigin
		connect += " " + analyticsOrigin
	}
	return "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " + script + "; " +
		connect + "; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
}

// statusWriter remembers the status and size for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// requestLog gives every request an id (X-Request-Id, kept when the caller
// sent a sane one), works out the client address, recovers panics, and logs
// one line per request (never the query: callbacks carry codes).
func (h *handler) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &reqState{id: r.Header.Get("X-Request-Id"), ip: h.clientAddr(r)}
		if !ridRe.MatchString(st.id) {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			st.id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", st.id)
		r = r.WithContext(context.WithValue(r.Context(), stateKey, st))
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				h.log.Error("panic", "path", logPath(r.URL.Path), "rid", st.id, "panic", v, "stack", string(debug.Stack()))
				if sw.status == 0 {
					writeError(sw, http.StatusInternalServerError, "internal_error", "Something went wrong on the server.")
				}
			}
			if r.URL.Path == "/healthz" {
				return
			}
			h.log.Info("http", "method", r.Method, "path", logPath(r.URL.Path), "status", sw.status, "bytes", sw.bytes,
				"ms", time.Since(start).Milliseconds(), "ip", st.ip, "rid", st.id)
		}()
		next.ServeHTTP(sw, r)
	})
}

// logPath is the path as the access log writes it: an install link's
// token is a secret (whoever holds it reads the intent, and the license key
// in it), so /v1/install/<token>[/complete] is written without it.
func logPath(p string) string {
	if rest, ok := strings.CutPrefix(p, "/v1/install/"); ok {
		if _, tail, found := strings.Cut(rest, "/"); found {
			return "/v1/install/{token}/" + tail
		}
		return "/v1/install/{token}"
	}
	return p
}

// clientAddr is the peer, or - when the peer is a trusted proxy - the
// address it reports in X-Forwarded-For. With FAPPS_TRUSTED_PROXIES the
// chain is walked from the right past every trusted hop; without it a
// loopback or private peer is trusted for the right-most hop only.
func (h *handler) clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	peer = peer.Unmap()
	trusted := func(a netip.Addr) bool {
		if len(h.cfg.TrustedProxies) == 0 {
			return a.IsLoopback() || a.IsPrivate()
		}
		for _, p := range h.cfg.TrustedProxies {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if !trusted(peer) {
		return peer.String()
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	addr := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			break
		}
		addr = a.Unmap()
		if len(h.cfg.TrustedProxies) == 0 || !trusted(addr) {
			break
		}
	}
	return addr.String()
}

// principal loads the request's session once (nil when signed out).
func (h *handler) principal(r *http.Request) (*session.Principal, error) {
	st := state(r)
	if !st.loaded {
		st.loaded = true
		if h.sessions != nil {
			st.p, st.err = h.sessions.Load(r.Context(), r)
		}
	}
	return st.p, st.err
}

// limit applies a rule and answers 429 when it is spent. A limiter error
// lets the request through (the fallback limiter already counts in process).
func (h *handler) limit(w http.ResponseWriter, r *http.Request, rule ratelimit.Rule, subject string) bool {
	if h.limiter == nil {
		return true
	}
	d, err := ratelimit.Check(r.Context(), h.limiter, rule, subject)
	if err != nil || d.Allowed {
		return true
	}
	secs := int(math.Ceil(d.RetryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests; try again in "+strconv.Itoa(secs)+" seconds.")
	return false
}

// api is a public JSON endpoint: the per-address API limit.
func (h *handler) api(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.limit(w, r, ratelimit.API, clientIP(r)) {
			return
		}
		fn(w, r)
	}
}

type authedFunc func(w http.ResponseWriter, r *http.Request, p *session.Principal)

// signedIn needs a session (401 unauthorized otherwise).
func (h *handler) signedIn(fn authedFunc) http.HandlerFunc {
	return h.api(func(w http.ResponseWriter, r *http.Request) {
		p, err := h.principal(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if p == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Sign in first.")
			return
		}
		fn(w, r, p)
	})
}

// mutating needs a session, a request from the store's own origin and the
// session's CSRF token in X-CSRF-Token (403 csrf_failed otherwise).
func (h *handler) mutating(fn authedFunc) http.HandlerFunc {
	return h.signedIn(func(w http.ResponseWriter, r *http.Request, p *session.Principal) {
		if !h.sameOrigin(r) || !session.CSRFValid(p, r.Header.Get("X-CSRF-Token")) {
			writeError(w, http.StatusForbidden, "csrf_failed", "The request did not come from the store's own pages.")
			return
		}
		fn(w, r, p)
	})
}

// role needs a store role (403 forbidden otherwise).
func role(rl domain.Role, fn authedFunc) authedFunc {
	return func(w http.ResponseWriter, r *http.Request, p *session.Principal) {
		if !p.Has(rl) {
			writeError(w, http.StatusForbidden, "forbidden", "This needs the "+string(rl)+" role.")
			return
		}
		fn(w, r, p)
	}
}

// sameOrigin: the Origin header (else the Referer) names this store.
func (h *handler) sameOrigin(r *http.Request) bool {
	want := h.origin
	if want == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		want = scheme + "://" + r.Host
	}
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, want)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && strings.EqualFold(u.Scheme+"://"+u.Host, want)
	}
	return false
}

// originOf is scheme://host[:port] of a URL ("" when it has none).
func originOf(public string) string {
	u, err := url.Parse(public)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
