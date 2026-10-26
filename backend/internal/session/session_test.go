package session

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

var secret = bytes.Repeat([]byte{0x42}, 32)

func manager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(nil, secret, true)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSafeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/apps/sign":                   "/apps/sign",
		"/review?x=1":                  "/review?x=1",
		"":                             "/",
		"//evil.example/":              "/",
		"/\\evil.example":              "/",
		"https://evil.example":         "/",
		"apps":                         "/",
		"/a\r\nSet-Cookie: x":          "/",
		"/" + strings.Repeat("a", 600): "/",
	} {
		if got := SafeReturnTo(in); got != want {
			t.Errorf("SafeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSecret(t *testing.T) {
	raw := bytes.Repeat([]byte{1}, 48)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		got, err := ParseSecret(enc.EncodeToString(raw))
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("ParseSecret: %v", err)
		}
	}
	if _, err := ParseSecret(base64.StdEncoding.EncodeToString(make([]byte, 16))); err == nil {
		t.Fatal("a 16-byte key was accepted")
	}
	if _, err := ParseSecret("not base64 !!"); err == nil {
		t.Fatal("garbage was accepted")
	}
	if _, err := NewManager(nil, make([]byte, 31), false); err == nil {
		t.Fatal("NewManager took a short secret")
	}
}

func TestSealedTokenOpensOnlyInItsContext(t *testing.T) {
	s, err := newSealer(secret, purposeGitHubToken)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.seal([]byte("gho_secret"), []byte("session-a"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("gho_secret")) {
		t.Fatal("the token is readable in the sealed bytes")
	}
	if got, err := s.open(sealed, []byte("session-a")); err != nil || string(got) != "gho_secret" {
		t.Fatalf("open = %q %v", got, err)
	}
	if _, err := s.open(sealed, []byte("session-b")); !errors.Is(err, ErrSealed) {
		t.Fatal("a sealed token opened for another session")
	}
	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := s.open(tampered, []byte("session-a")); !errors.Is(err, ErrSealed) {
		t.Fatal("a tampered token opened")
	}
	other, _ := newSealer(secret, purposeFlow)
	if _, err := other.open(sealed, []byte("session-a")); !errors.Is(err, ErrSealed) {
		t.Fatal("a key derived for another purpose opened it")
	}
	again, _ := s.seal([]byte("gho_secret"), []byte("session-a"))
	if bytes.Equal(again, sealed) {
		t.Fatal("two seals of the same token are equal (nonce reuse)")
	}
}

// roundTrip carries the cookies one response set into a new request.
func roundTrip(rec *httptest.ResponseRecorder, target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestFlowCookieRoundTrip(t *testing.T) {
	m := manager(t)
	rec := httptest.NewRecorder()
	if err := m.StartFlow(rec, Flow{Provider: "github", State: "st", ReturnTo: "/review"}); err != nil {
		t.Fatal(err)
	}
	c := rec.Result().Cookies()[0]
	if c.Name != "fapps_flow_github" || c.Path != "/auth/" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("flow cookie = %+v", c)
	}
	if strings.Contains(c.Value, "/review") {
		t.Fatal("the flow cookie is readable")
	}

	out := httptest.NewRecorder()
	f, err := m.FinishFlow(out, roundTrip(rec, "/auth/github/callback"), "github", "st")
	if err != nil || f.State != "st" || f.ReturnTo != "/review" {
		t.Fatalf("FinishFlow = %+v %v", f, err)
	}
	if cleared := out.Result().Cookies(); len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("the flow cookie was not cleared: %+v", cleared)
	}

	for name, fn := range map[string]func() error{
		"wrong state": func() error {
			_, err := m.FinishFlow(httptest.NewRecorder(), roundTrip(rec, "/"), "github", "other")
			return err
		},
		"no state": func() error {
			_, err := m.FinishFlow(httptest.NewRecorder(), roundTrip(rec, "/"), "github", "")
			return err
		},
		"other provider": func() error {
			_, err := m.FinishFlow(httptest.NewRecorder(), roundTrip(rec, "/"), "other", "st")
			return err
		},
		"no cookie": func() error {
			_, err := m.FinishFlow(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "github", "st")
			return err
		},
		"other key": func() error {
			other, _ := NewManager(nil, bytes.Repeat([]byte{9}, 32), true)
			_, err := other.FinishFlow(httptest.NewRecorder(), roundTrip(rec, "/"), "github", "st")
			return err
		},
	} {
		if err := fn(); !errors.Is(err, ErrFlow) {
			t.Errorf("%s: err = %v, want ErrFlow", name, err)
		}
	}
}

func TestFlowExpires(t *testing.T) {
	m := manager(t)
	f := Flow{Provider: "github", State: "s", Expires: time.Now().Add(-time.Minute).Unix()}
	b := []byte(`{"p":"github","s":"s","e":` + strconv.FormatInt(f.Expires, 10) + `}`)
	sealed, _ := m.flow.seal(b, []byte("github"))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "fapps_flow_github", Value: base64.RawURLEncoding.EncodeToString(sealed)})
	if _, err := m.FinishFlow(httptest.NewRecorder(), req, "github", "s"); !errors.Is(err, ErrFlow) {
		t.Fatalf("an expired flow was accepted: %v", err)
	}
}

func TestCSRFAndRoles(t *testing.T) {
	p := &Principal{CSRFToken: "abc"}
	if !CSRFValid(p, "abc") || CSRFValid(p, "abd") || CSRFValid(p, "") || CSRFValid(nil, "abc") || CSRFValid(&Principal{}, "") {
		t.Fatal("CSRFValid")
	}
	admin := &Principal{Roles: []domain.Role{domain.RoleAdmin}}
	reviewer := &Principal{Roles: []domain.Role{domain.RoleReviewer}}
	plain := &Principal{}
	if !admin.Has(domain.RoleReviewer) || !admin.Has(domain.RoleAdmin) {
		t.Fatal("an administrator holds the reviewer's rights")
	}
	if !reviewer.Has(domain.RoleReviewer) || reviewer.Has(domain.RoleAdmin) {
		t.Fatal("a reviewer is not an administrator")
	}
	if plain.Has(domain.RoleReviewer) || (*Principal)(nil).Has(domain.RoleReviewer) {
		t.Fatal("no role, no rights")
	}
	if len(NewToken()) != 43 || NewToken() == NewToken() {
		t.Fatal("NewToken")
	}
	if len(HashID("x")) != 32 || bytes.Equal(HashID("x"), HashID("y")) {
		t.Fatal("HashID")
	}
}
