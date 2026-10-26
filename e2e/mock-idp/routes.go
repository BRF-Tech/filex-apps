package main

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Paths of `serve` (HTTPS on :9443 = MOCK_PUBLIC_URL, and plain HTTP on :9000):
//
//	/github/login/oauth/authorize          GitHub's authorize page   (FAPPS_GITHUB_OAUTH_URL = <public>/github)
//	/github/login/oauth/access_token       GitHub's token exchange
//	/github/{o}/{r}/releases/download/...  release download -> 302 to /release-assets/...
//	/github-api/...                        GitHub's REST API by prefix (the e2e store reaches it as api.github.com, through its sidecar)
//	/raw/{o}/{r}/{ref}/{path}              raw.githubusercontent.com
//	/release-assets/{o}/{r}/{tag}/{file}   the release asset host
//	/__health                              liveness
//	/__mock/requests                       the last requests seen (method, host, path, status) for tests
//
// `github-tls` serves the same by host name instead of prefix:
// github.com, api.github.com, raw.githubusercontent.com and the asset hosts.

func runServe(cfg *Config) error {
	if err := ensureCerts(cfg.CADir, cfg.TLSHosts); err != nil {
		return err
	}
	fx, err := loadFixtures(cfg.Fixtures)
	if err != nil {
		return err
	}
	gh := newGitHub(cfg, fx, false)
	rec := &recorder{}
	h := rec.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/__health":
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "repos": len(fx.repos)})
		case p == "/__mock/requests":
			writeJSON(w, http.StatusOK, rec.list())
		case p == "/github/login/oauth/authorize":
			gh.authorize(w, r)
		case p == "/github/login/oauth/access_token":
			gh.accessToken(w, r)
		case strings.HasPrefix(p, "/github-api/"):
			gh.api(w, r, strings.TrimPrefix(p, "/github-api"))
		case strings.HasPrefix(p, "/github/"):
			if !gh.releaseDownload(w, r, strings.TrimPrefix(p, "/github")) {
				http.NotFound(w, r)
			}
		case strings.HasPrefix(p, "/raw/"):
			gh.raw(w, strings.TrimPrefix(p, "/raw"))
		case strings.HasPrefix(p, "/release-assets/"):
			gh.asset(w, strings.TrimPrefix(p, "/release-assets"))
		default:
			http.NotFound(w, r)
		}
	}))
	cert, err := waitCerts(cfg.CADir, 10*time.Second)
	if err != nil {
		return err
	}
	// The same handler twice: HTTPS with the run's certificate (what the
	// store and the browsers use: the store's outbound rules want HTTPS) and
	// plain HTTP (health probes, debugging).
	errc := make(chan error, 2)
	go func() {
		srv := &http.Server{
			Addr: cfg.TLSListen, Handler: h, ReadHeaderTimeout: 10 * time.Second,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		}
		errc <- srv.ListenAndServeTLS("", "")
	}()
	go func() {
		errc <- (&http.Server{Addr: cfg.Listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe()
	}()
	log.Printf("mock-idp serve on %s (TLS) and %s as %s (%d repos, %d GitHub users, server token set: %v)",
		cfg.TLSListen, cfg.Listen, cfg.PublicURL, len(fx.repos), len(fx.GitHubUsers), cfg.AdminToken != "")
	return <-errc
}

func runGitHubTLS(cfg *Config) error {
	cert, err := waitCerts(cfg.CADir, 2*time.Minute)
	if err != nil {
		return err
	}
	fx, err := loadFixtures(cfg.Fixtures)
	if err != nil {
		return err
	}
	gh := newGitHub(cfg, fx, true)
	rec := &recorder{}
	h := rec.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		p := r.URL.Path
		switch {
		case p == "/__health":
			writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		case p == "/__mock/requests":
			writeJSON(w, http.StatusOK, rec.list())
		case host == "api.github.com":
			gh.api(w, r, p)
		case host == "raw.githubusercontent.com":
			gh.raw(w, p)
		case host == "github.com":
			switch p {
			case "/login/oauth/authorize":
				gh.authorize(w, r)
			case "/login/oauth/access_token":
				gh.accessToken(w, r)
			default:
				if !gh.releaseDownload(w, r, p) {
					http.NotFound(w, r)
				}
			}
		case host == "release-assets.githubusercontent.com", host == "objects.githubusercontent.com", host == "github-releases.githubusercontent.com":
			gh.asset(w, p)
		default:
			http.NotFound(w, r)
		}
	}))
	srv := &http.Server{
		Addr: cfg.GHListen, Handler: h, ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	log.Printf("mock-idp github-tls on %s (%d repos)", cfg.GHListen, len(fx.repos))
	return srv.ListenAndServeTLS("", "")
}

// runEdge is the store's HTTPS front door, standing in for the reverse
// proxy in front of a real store: TLS ends here, the request goes on to the
// app over plain HTTP with the headers a proxy sets (X-Forwarded-Proto:
// https, X-Forwarded-Host: the public host) and X-Forwarded-For with the
// caller's address appended (httputil's default), so the store's proxy
// handling is exercised as in production.
func runEdge(cfg *Config) error {
	cert, err := waitCerts(cfg.CADir, 2*time.Minute)
	if err != nil {
		return err
	}
	up, err := url.Parse(cfg.EdgeUpstream)
	if err != nil {
		return err
	}
	rp := httputil.NewSingleHostReverseProxy(up)
	orig := rp.Director
	rp.Director = func(req *http.Request) {
		host := req.Host
		orig(req)
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", host)
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("edge: upstream %s: %v", cfg.EdgeUpstream, err)
		http.Error(w, "edge: the app does not answer", http.StatusBadGateway)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/__edge/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.Handle("/", rp)
	srv := &http.Server{
		Addr: cfg.EdgeListen, Handler: mux, ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	log.Printf("mock-idp edge on %s -> %s", cfg.EdgeListen, cfg.EdgeUpstream)
	return srv.ListenAndServeTLS("", "")
}

// recorder keeps the last requests (no query strings: codes and secrets
// travel there) so a test can assert what reached the mock.
type recorder struct {
	mu   sync.Mutex
	seen []seenRequest
}

type seenRequest struct {
	At     time.Time `json:"at"`
	Method string    `json:"method"`
	Host   string    `json:"host"`
	Path   string    `json:"path"`
	Query  []string  `json:"query_keys,omitempty"`
	Status int       `json:"status"`
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (rc *recorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/__health" || r.URL.Path == "/__mock/requests" {
			return
		}
		var keys []string
		for k := range r.URL.Query() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		rc.mu.Lock()
		rc.seen = append(rc.seen, seenRequest{At: time.Now().UTC(), Method: r.Method, Host: r.Host, Path: r.URL.Path, Query: keys, Status: sw.status})
		if len(rc.seen) > 500 {
			rc.seen = rc.seen[len(rc.seen)-500:]
		}
		rc.mu.Unlock()
		log.Printf("%s %s%s -> %d", r.Method, r.Host, r.URL.Path, sw.status)
	})
}

func (rc *recorder) list() []seenRequest {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]seenRequest{}, rc.seen...)
}
