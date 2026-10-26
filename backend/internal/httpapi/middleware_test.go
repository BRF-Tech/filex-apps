package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testHandler(cfg *config.Config) *handler {
	if cfg == nil {
		cfg = &config.Config{PublicURL: "https://store.example"}
	}
	return &handler{cfg: cfg, log: quiet(), origin: originOf(cfg.PublicURL)}
}

// withPrincipal puts a signed-in principal into the request as the
// middleware would.
func withPrincipal(r *http.Request, p *session.Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), stateKey, &reqState{id: "test", ip: "203.0.113.9", loaded: true, p: p}))
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e Error
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("not a JSON error: %q", rec.Body.String())
	}
	if e.Message == "" {
		t.Fatalf("error without message: %q", rec.Body.String())
	}
	return e.Error
}

func TestMutatingRefusesMissingOrWrongCSRFAndForeignOrigin(t *testing.T) {
	h := testHandler(nil)
	called := 0
	fn := h.mutating(func(w http.ResponseWriter, r *http.Request, p *session.Principal) {
		called++
		w.WriteHeader(http.StatusNoContent)
	})
	p := &session.Principal{AccountID: 1, CSRFToken: "the-token"}
	cases := []struct {
		name   string
		header map[string]string
		status int
	}{
		{"good", map[string]string{"Origin": "https://store.example", "X-CSRF-Token": "the-token"}, http.StatusNoContent},
		{"good by referer", map[string]string{"Referer": "https://store.example/review/7", "X-CSRF-Token": "the-token"}, http.StatusNoContent},
		{"missing token", map[string]string{"Origin": "https://store.example"}, http.StatusForbidden},
		{"wrong token", map[string]string{"Origin": "https://store.example", "X-CSRF-Token": "the-tokem"}, http.StatusForbidden},
		{"foreign origin", map[string]string{"Origin": "https://evil.example", "X-CSRF-Token": "the-token"}, http.StatusForbidden},
		{"foreign referer", map[string]string{"Referer": "https://store.example.evil.example/", "X-CSRF-Token": "the-token"}, http.StatusForbidden},
		{"no origin at all", map[string]string{"X-CSRF-Token": "the-token"}, http.StatusForbidden},
		{"http instead of https", map[string]string{"Origin": "http://store.example", "X-CSRF-Token": "the-token"}, http.StatusForbidden},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/submissions", strings.NewReader("{}"))
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		fn(rec, withPrincipal(req, p))
		if rec.Code != c.status {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.status)
		}
		if c.status == http.StatusForbidden && errCode(t, rec) != "csrf_failed" {
			t.Errorf("%s: code %s, want csrf_failed", c.name, errCode(t, rec))
		}
	}
	if called != 2 {
		t.Fatalf("handler ran %d times, want 2", called)
	}

	// Signed out: 401, the handler never runs.
	rec := httptest.NewRecorder()
	fn(rec, withPrincipal(httptest.NewRequest(http.MethodPost, "/x", nil), nil))
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthorized" || called != 2 {
		t.Fatalf("signed out: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRoleGate(t *testing.T) {
	h := testHandler(nil)
	ok := func(w http.ResponseWriter, r *http.Request, p *session.Principal) { w.WriteHeader(http.StatusOK) }
	fn := h.signedIn(role(adminRole, ok))
	rec := httptest.NewRecorder()
	fn(rec, withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), &session.Principal{AccountID: 1, Roles: []domain.Role{domain.RoleReviewer}}))
	if rec.Code != http.StatusForbidden || errCode(t, rec) != "forbidden" {
		t.Fatalf("reviewer on an admin route: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	fn(rec, withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), &session.Principal{AccountID: 1, Roles: []domain.Role{domain.RoleAdmin}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.signedIn(role(reviewerRole, ok))(rec, withPrincipal(httptest.NewRequest(http.MethodGet, "/", nil), &session.Principal{AccountID: 1, Roles: []domain.Role{domain.RoleAdmin}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin on a reviewer route: %d", rec.Code)
	}
}

func TestRateLimitAnswers429WithRetryAfter(t *testing.T) {
	h := testHandler(nil)
	h.limiter = ratelimit.NewMemory()
	fn := h.api(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	var last *httptest.ResponseRecorder
	for i := 0; i <= ratelimit.API.Limit; i++ {
		last = httptest.NewRecorder()
		fn(last, withPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/apps", nil), nil))
		if i < ratelimit.API.Limit && last.Code != http.StatusOK {
			t.Fatalf("request %d refused", i+1)
		}
	}
	if last.Code != http.StatusTooManyRequests || errCode(t, last) != "rate_limited" {
		t.Fatalf("over the limit: %d %s", last.Code, last.Body.String())
	}
	if ra := last.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After = %q", ra)
	}
}

func TestClientAddr(t *testing.T) {
	req := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		for _, x := range xff {
			r.Header.Add("X-Forwarded-For", x)
		}
		return r
	}
	def := testHandler(nil)
	for _, c := range []struct {
		r    *http.Request
		want string
	}{
		{req("203.0.113.5:4000"), "203.0.113.5"},
		{req("203.0.113.5:4000", "198.51.100.1"), "203.0.113.5"},
		{req("172.18.0.3:4000", "198.51.100.1, 198.51.100.2"), "198.51.100.2"},
		{req("127.0.0.1:4000", "198.51.100.7"), "198.51.100.7"},
		{req("[::1]:4000", "2001:db8::1"), "2001:db8::1"},
		{req("10.0.0.2:4000"), "10.0.0.2"},
		{req("10.0.0.2:4000", "not-an-ip"), "10.0.0.2"},
	} {
		if got := def.clientAddr(c.r); got != c.want {
			t.Errorf("default: %s %v -> %s, want %s", c.r.RemoteAddr, c.r.Header.Values("X-Forwarded-For"), got, c.want)
		}
	}
	cfg := &config.Config{PublicURL: "https://store.example", TrustedProxies: []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("10.9.0.0/16")}}
	chain := testHandler(cfg)
	for _, c := range []struct {
		r    *http.Request
		want string
	}{
		{req("192.0.2.10:1", "198.51.100.9, 10.9.1.1"), "198.51.100.9"},
		{req("192.0.2.10:1", "198.51.100.9, 203.0.113.1"), "203.0.113.1"},
		{req("172.18.0.3:1", "198.51.100.9"), "172.18.0.3"},
		{req("192.0.2.10:1"), "192.0.2.10"},
	} {
		if got := chain.clientAddr(c.r); got != c.want {
			t.Errorf("configured: %s %v -> %s, want %s", c.r.RemoteAddr, c.r.Header.Values("X-Forwarded-For"), got, c.want)
		}
	}
}

func TestDecodeIsStrict(t *testing.T) {
	type body struct {
		Reason string `json:"reason"`
	}
	for name, c := range map[string]struct {
		body string
		ct   string
		ok   bool
	}{
		"good":          {`{"reason":"x"}`, "application/json", true},
		"empty":         {``, "application/json", true},
		"unknown field": {`{"reason":"x","extra":1}`, "application/json", false},
		"two values":    {`{"reason":"x"}{"reason":"y"}`, "application/json", false},
		"nul escape":    {`{"reason":"a\u0000b"}`, "application/json", false},
		"not json":      {`reason=x`, "application/json", false},
		"form":          {`{"reason":"x"}`, "application/x-www-form-urlencoded", false},
		"too large":     {`{"reason":"` + strings.Repeat("a", maxBody) + `"}`, "application/json", false},
	} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(c.body))
		r.Header.Set("Content-Type", c.ct)
		var b body
		err := decode(httptest.NewRecorder(), r, &b)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", name, err)
		}
		var bad *badRequest
		if err != nil && !errors.As(err, &bad) {
			t.Errorf("%s: %T is not a bad request", name, err)
		}
	}
	if _, err := cleanText("reason", "  fine\nline  ", 1, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := cleanText("reason", "bell\a", 1, 10); err == nil {
		t.Fatal("a control character passed")
	}
	if _, err := cleanText("reason", "   ", 1, 10); err == nil {
		t.Fatal("blank passed a required field")
	}
}

func TestFailMapsRefusals(t *testing.T) {
	h := testHandler(nil)
	for kind, status := range map[pipeline.RefusalKind]int{
		pipeline.Invalid: 400, pipeline.Forbidden: 403, pipeline.NotFound: 404, pipeline.Conflict: 409, pipeline.Unavailable: 503,
	} {
		rec := httptest.NewRecorder()
		h.fail(rec, httptest.NewRequest(http.MethodGet, "/", nil), &pipeline.Refusal{Kind: kind, Code: "some_code", Message: "m"})
		if rec.Code != status || errCode(t, rec) != "some_code" {
			t.Errorf("kind %d: %d %s", kind, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	sub := store.Submission{ID: 5, Repo: "a/b", Ref: "v1", Status: "queued", Attempt: 1}
	h.fail(rec, httptest.NewRequest(http.MethodGet, "/", nil), &pipeline.Refusal{Kind: pipeline.Conflict, Code: "submission_open", Message: "open", Submission: &sub})
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"submission":{"id":5`) {
		t.Fatalf("submission_open carries the submission: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.fail(rec, httptest.NewRequest(http.MethodGet, "/", nil), errors.New("pq: secret internal detail"))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "secret internal") || errCode(t, rec) != "internal_error" {
		t.Fatalf("internal error leaked: %s", rec.Body.String())
	}
}

func TestEtagMatches(t *testing.T) {
	sha := strings.Repeat("a", 64)
	for header, want := range map[string]bool{
		`"` + sha + `"`: true, `W/"` + sha + `"`: true, `"x", "` + sha + `"`: true, "*": true, `"other"`: false, "": false,
	} {
		if etagMatches(header, sha) != want {
			t.Errorf("etagMatches(%q) != %v", header, want)
		}
	}
}

// An install link's token never reaches the access log: whoever holds it
// reads the intent and the license key in it.
func TestLogPathDropsTheInstallToken(t *testing.T) {
	tok := strings.Repeat("A", 43)
	for in, want := range map[string]string{
		"/v1/install/" + tok:               "/v1/install/{token}",
		"/v1/install/" + tok + "/complete": "/v1/install/{token}/complete",
		"/v1/index.json":                   "/v1/index.json",
		"/api/v1/apps/hello":               "/api/v1/apps/hello",
	} {
		if got := logPath(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}
