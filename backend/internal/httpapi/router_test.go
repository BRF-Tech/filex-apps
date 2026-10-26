package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/auth/adminrepo"
	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
)

// lazyPool is a pool that never connects unless a query runs (the routes
// below answer before touching the database).
func lazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func ready(t *testing.T) http.Handler {
	return New(Deps{Config: &config.Config{PublicURL: "https://store.example"}, Pool: lazyPool(t), Log: quiet()})
}

func TestHealthzAnswersWithoutConfiguration(t *testing.T) {
	h := New(Deps{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers missing")
	}
}

func TestUnclaimedAPIPathIsJSON404NotTheSPA(t *testing.T) {
	h := ready(t)
	for _, p := range []string{"/api/v1/nope", "/v1/nope", "/auth/nope"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("GET %s = %d %q, want a JSON 404", p, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

func TestNotReadyRefusesTheAPIButNotHealth(t *testing.T) {
	h := New(Deps{NotReady: "migrations failed"})
	for path, code := range map[string]string{
		"/api/v1/apps":       "not_ready",
		"/api/v1/me":         "not_ready",
		"/auth/github/login": "not_ready",
		"/v1/index.json":     "index_not_ready",
		"/v1/index.json.sig": "index_not_ready",
		"/v1/media/" + strings.Repeat("a", 64) + ".png": "not_ready",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"error":"`+code+`"`) {
			t.Errorf("GET %s = %d %s, want 503 %s", path, rec.Code, rec.Body.String(), code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "migrations failed") {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body.String())
	}
}

func TestIndexIsNeverAnEmptyDocumentBeforeTheFirstRelease(t *testing.T) {
	h := New(Deps{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/index.json", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "index_not_ready") {
		t.Fatalf("GET /v1/index.json = %d %s, want 503 index_not_ready", rec.Code, rec.Body.String())
	}
}

// The install and license routes are real now (they answered 501 while
// reserved): before the database is ready they wait like every route; on a
// ready server without the feature wired they say why (503) instead of
// pretending, and creating an intent needs a session.
func TestInstallAndLicenceRoutesAreNoLongerReserved(t *testing.T) {
	notReady := New(Deps{})
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/install-intents"}, {http.MethodGet, "/v1/install/abc"},
		{http.MethodPost, "/v1/licenses/verify"},
	} {
		rec := httptest.NewRecorder()
		notReady.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not_ready") {
			t.Fatalf("not ready: %s %s = %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	h := ready(t)
	for _, c := range []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodPost, "/api/v1/install-intents", http.StatusUnauthorized, "unauthorized"},
		{http.MethodGet, "/v1/install/" + strings.Repeat("a", 43), http.StatusServiceUnavailable, "data_key_missing"},
		{http.MethodPost, "/v1/install/" + strings.Repeat("a", 43) + "/complete", http.StatusServiceUnavailable, "data_key_missing"},
		{http.MethodPost, "/v1/licenses/verify", http.StatusServiceUnavailable, "data_key_missing"},
		{http.MethodGet, "/api/v1/me/instances", http.StatusUnauthorized, "unauthorized"},
		{http.MethodPut, "/api/v1/apps/hello/pricing", http.StatusUnauthorized, "unauthorized"},
		{http.MethodGet, "/v1/licenses/verify", http.StatusMethodNotAllowed, "method_not_allowed"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"error":"`+c.code+`"`) {
			t.Fatalf("%s %s = %d %s, want %d %s", c.method, c.path, rec.Code, rec.Body.String(), c.status, c.code)
		}
	}
}

// The instance connection routes exist: a session for the owner's
// doors, the feature's own 503 for a filex's doors on a server without it,
// and only their own methods.
func TestInstanceConnectionRoutesAnswer(t *testing.T) {
	h := ready(t)
	id := "00000000-0000-0000-0000-000000000000"
	for _, c := range []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodPost, "/api/v1/me/instances/" + id + "/connect-code", http.StatusUnauthorized, "unauthorized"},
		{http.MethodDelete, "/api/v1/me/instances/" + id + "/connection", http.StatusUnauthorized, "unauthorized"},
		{http.MethodPost, "/v1/instances/connect", http.StatusServiceUnavailable, "data_key_missing"},
		{http.MethodGet, "/v1/instances/" + id + "/connection", http.StatusServiceUnavailable, "data_key_missing"},
		{http.MethodDelete, "/v1/instances/" + id + "/connection", http.StatusServiceUnavailable, "data_key_missing"},
		{http.MethodPost, "/v1/instances/" + id + "/intents", http.StatusServiceUnavailable, "data_key_missing"},
		{http.MethodGet, "/v1/instances/connect", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodGet, "/v1/instances/" + id + "/intents", http.StatusMethodNotAllowed, "method_not_allowed"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), `"error":"`+c.code+`"`) {
			t.Fatalf("%s %s = %d %s, want %d %s", c.method, c.path, rec.Code, rec.Body.String(), c.status, c.code)
		}
	}
}

func TestWrongMethodIsAJSON405WithAllow(t *testing.T) {
	h := ready(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/submissions", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD, POST" ||
		!strings.Contains(rec.Body.String(), "method_not_allowed") {
		t.Fatalf("DELETE /api/v1/submissions = %d %q %s", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
	}
}

func TestSignedOutIs401AndSignInIsOffWithoutAKey(t *testing.T) {
	h := ready(t)
	for _, c := range []struct{ method, path, want string }{
		{http.MethodGet, "/api/v1/me", "unauthorized"},
		{http.MethodPost, "/api/v1/submissions", "unauthorized"},
		{http.MethodGet, "/api/v1/review/queue", "unauthorized"},
		{http.MethodGet, "/api/v1/admin/apps", "unauthorized"},
		{http.MethodPatch, "/api/v1/admin/publishers/brf-tech", "unauthorized"},
		{http.MethodGet, "/api/v1/admin/health", "unauthorized"},
		{http.MethodGet, "/auth/github/login", "not_configured"},
		// The Keycloak sign-in is gone: its routes are plain 404s.
		{http.MethodGet, "/auth/oidc/login", "not_found"},
		{http.MethodGet, "/auth/oidc/callback", "not_found"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if !strings.Contains(rec.Body.String(), `"error":"`+c.want+`"`) {
			t.Errorf("%s %s = %d %s, want %s", c.method, c.path, rec.Code, rec.Body.String(), c.want)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/auth/logout", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout without a session = %d, want 204", rec.Code)
	}
}

func TestRequestIDAndSecurityHeaders(t *testing.T) {
	h := New(Deps{Config: &config.Config{PublicURL: "https://store.example"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-Id", "abcdef0123456789")
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Request-Id") != "abcdef0123456789" {
		t.Fatal("a sane request id was not kept")
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-Id", "bad id\nwith newline")
	h.ServeHTTP(rec, req)
	if id := rec.Header().Get("X-Request-Id"); len(id) != 16 || strings.Contains(id, " ") {
		t.Fatalf("request id = %q", id)
	}
	hd := rec.Header()
	if !strings.Contains(hd.Get("Content-Security-Policy"), "frame-ancestors 'none'") || hd.Get("X-Frame-Options") != "DENY" ||
		hd.Get("Strict-Transport-Security") == "" {
		t.Fatalf("headers = %v", hd)
	}
}

// The CSP without a page counter is the store's own, byte for byte; with
// FAPPS_ANALYTICS_ORIGIN that origin joins script-src and connect-src and
// nothing else changes.
func TestContentSecurityPolicyAndTheAnalyticsOrigin(t *testing.T) {
	const own = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; " +
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	const counted = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self' https://stats.example.org; connect-src 'self' https://stats.example.org; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	for origin, want := range map[string]string{"": own, "https://stats.example.org": counted} {
		h := New(Deps{Config: &config.Config{PublicURL: "https://store.example", AnalyticsOrigin: origin}})
		for _, path := range []string{"/healthz", "/", "/api/v1/nope"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if got := rec.Header().Get("Content-Security-Policy"); got != want {
				t.Errorf("origin %q, GET %s: CSP = %q, want %q", origin, path, got, want)
			}
			if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Strict-Transport-Security") == "" {
				t.Errorf("origin %q, GET %s: headers = %v", origin, path, rec.Header())
			}
		}
	}
}

func TestBadPathValuesAre404WithoutTheDatabase(t *testing.T) {
	h := ready(t)
	for _, p := range []string{"/api/v1/apps/Not_A_Name!", "/api/v1/publishers/x", "/v1/media/abc.png",
		"/v1/media/" + strings.Repeat("a", 64) + ".svg", "/api/v1/submissions/0"} {
		rec := httptest.NewRecorder()
		req := withPrincipal(httptest.NewRequest(http.MethodGet, p, nil), nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d %s", p, rec.Code, rec.Body.String())
		}
	}
}

func TestSignedOutMeSaysWhichSignInsExist(t *testing.T) {
	h := ready(t)
	for i := 0; i < 15; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
		var body struct {
			Error     string           `json:"error"`
			Message   string           `json:"message"`
			Providers *map[string]bool `json:"providers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("not JSON: %s", rec.Body.String())
		}
		if rec.Code != http.StatusUnauthorized || body.Error != "unauthorized" || body.Message == "" || body.Providers == nil {
			t.Fatalf("GET /api/v1/me signed out = %d %s, want 401 with providers", rec.Code, rec.Body.String())
		}
		p := *body.Providers
		if v, ok := p["github"]; !ok || v {
			t.Fatalf("providers.github = %v (present %v), want false without a session key", v, ok)
		}
		if len(p) != 1 {
			t.Fatalf("providers = %v, want github only", p)
		}
	}
}

// /readyz stays what it was (ready or not) and carries what someone must
// fix: no role check token, and removed settings still set.
func TestReadyzWarnsAboutTheRoleCheckAndRemovedSettings(t *testing.T) {
	cfg := &config.Config{PublicURL: "https://store.example", Removed: []string{"FAPPS_OIDC_CLIENT_SECRET"}}
	h := New(Deps{Config: cfg, Log: quiet(), Roles: adminrepo.New(nil, "acme/store-admins", "", quiet())})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "not_ready" || len(body.Warnings) != 2 ||
		!strings.Contains(body.Warnings[0], "FAPPS_ADMIN_TOKEN is not set") ||
		!strings.Contains(body.Warnings[1], "FAPPS_OIDC_CLIENT_SECRET") {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body.String())
	}
}

// GET /api/v1/store says how the store presents itself, from the
// configuration alone: before the database is ready too.
func TestStoreInfoAndLogo(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><rect width="8" height="8"/></svg>`)
	branded := &config.Config{PublicURL: "https://apps.example.com", Brand: config.Brand{Name: "Acme Apps", Logo: svg, LogoType: "image/svg+xml",
		OperatorName: "Acme Corp.", OperatorURL: "https://www.example.com/", Contact: "mailto:store@example.com",
		TermsURL: "https://www.example.com/terms", PrivacyURL: "https://www.example.com/privacy"}}
	for name, h := range map[string]http.Handler{
		"ready":     New(Deps{Config: branded, Pool: lazyPool(t), Log: quiet()}),
		"not ready": New(Deps{Config: branded, Log: quiet(), NotReady: "starting"}),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/store", nil))
		var info map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		if info["name"] != "Acme Apps" || !strings.HasPrefix(info["logo_url"], "/api/v1/store/logo?v=") || info["operator_name"] != "Acme Corp." ||
			info["operator_url"] != "https://www.example.com/" || info["contact"] != "mailto:store@example.com" ||
			info["terms_url"] != "https://www.example.com/terms" || info["privacy_url"] != "https://www.example.com/privacy" {
			t.Errorf("%s: %v", name, info)
		}
		if rec.Header().Get("Cache-Control") != "public, max-age=300" {
			t.Errorf("%s: Cache-Control %q", name, rec.Header().Get("Cache-Control"))
		}

		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, info["logo_url"], nil))
		hd := rec.Header()
		if rec.Code != http.StatusOK || rec.Body.String() != string(svg) || hd.Get("Content-Type") != "image/svg+xml" ||
			!strings.Contains(hd.Get("Content-Security-Policy"), "sandbox") || hd.Get("X-Content-Type-Options") != "nosniff" ||
			hd.Get("Cache-Control") != "public, max-age=86400" || hd.Get("ETag") == "" {
			t.Errorf("%s: logo %d %v %q", name, rec.Code, hd, rec.Body.String())
		}
		again := httptest.NewRequest(http.MethodGet, "/api/v1/store/logo", nil)
		again.Header.Set("If-None-Match", hd.Get("ETag"))
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, again)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("%s: If-None-Match: %d", name, rec.Code)
		}
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/store", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: POST = %d", name, rec.Code)
		}
	}

	// Nothing configured: the name alone, and no logo of its own.
	plain := New(Deps{Config: &config.Config{PublicURL: "https://apps.example.com", Brand: config.Brand{Name: "apps.example.com"}}, Pool: lazyPool(t), Log: quiet()})
	rec := httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/store", nil))
	if rec.Body.String() != `{"name":"apps.example.com"}`+"\n" && rec.Body.String() != `{"name":"apps.example.com"}` {
		t.Errorf("plain: %q", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/store/logo", nil))
	if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("no logo: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// Every page load asks GET /api/v1/store, so it does not spend the
// per-address API budget: with that budget gone it still answers, while an
// endpoint that does work is refused.
func TestStoreInfoOutsideTheAPILimit(t *testing.T) {
	cfg := &config.Config{PublicURL: "https://apps.example.com", Brand: config.Brand{Name: "Acme Apps"}}
	h := New(Deps{Config: cfg, Pool: lazyPool(t), Log: quiet(), Limiter: refuseAll{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/store", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("store info with the API budget spent: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/categories", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("categories with the API budget spent: %d, want 429", rec.Code)
	}
}

// refuseAll is a limiter whose every budget is spent.
type refuseAll struct{}

func (refuseAll) Allow(context.Context, string, int, time.Duration) (ratelimit.Decision, error) {
	return ratelimit.Decision{Allowed: false, RetryAfter: time.Minute}, nil
}
