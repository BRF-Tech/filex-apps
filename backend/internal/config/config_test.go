package config

import (
	"bytes"
	"encoding/base64"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubEndpointsDefaultToGitHub(t *testing.T) {
	t.Setenv("FAPPS_GITHUB_OAUTH_URL", "")
	t.Setenv("FAPPS_GITHUB_API_URL", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.GitHub.OAuthURL != "https://github.com" || c.GitHub.APIURL != "https://api.github.com" {
		t.Fatalf("defaults = %q %q", c.GitHub.OAuthURL, c.GitHub.APIURL)
	}
}

func TestGitHubEndpointsPointAtAMock(t *testing.T) {
	t.Setenv("FAPPS_GITHUB_OAUTH_URL", "http://mock-idp:9000/github/")
	t.Setenv("FAPPS_GITHUB_API_URL", "http://mock-idp:9000/github/api")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.GitHub.OAuthURL != "http://mock-idp:9000/github" || c.GitHub.APIURL != "http://mock-idp:9000/github/api" {
		t.Fatalf("overrides = %q %q", c.GitHub.OAuthURL, c.GitHub.APIURL)
	}
	for _, bad := range []string{"ftp://x", "not a url", "http://", "https://x/?q=1"} {
		t.Setenv("FAPPS_GITHUB_API_URL", bad)
		if _, err := Load(); err == nil {
			t.Errorf("FAPPS_GITHUB_API_URL=%q accepted", bad)
		}
	}
}

func TestAnalyticsOrigin(t *testing.T) {
	t.Setenv("FAPPS_ANALYTICS_ORIGIN", "")
	if c, err := Load(); err != nil || c.AnalyticsOrigin != "" {
		t.Fatalf("unset: %q %v", c.AnalyticsOrigin, err)
	}
	for in, want := range map[string]string{
		"https://stats.example.com":      "https://stats.example.com",
		" https://Stats.Example.com/ ":   "https://stats.example.com",
		"https://stats.example.org:8443": "https://stats.example.org:8443",
		"https://a-b.c-d.example.co.uk/": "https://a-b.c-d.example.co.uk",
	} {
		t.Setenv("FAPPS_ANALYTICS_ORIGIN", in)
		c, err := Load()
		if err != nil || c.AnalyticsOrigin != want {
			t.Errorf("%q = %q %v, want %q", in, c.AnalyticsOrigin, err, want)
		}
	}
	// The value goes into the CSP verbatim: anything that is not exactly an
	// https origin stops the server.
	for _, bad := range []string{
		"http://stats.example.com", "stats.example.com", "https://", "https://stats.example.com/script.js",
		"https://stats.example.com?x=1", "https://stats.example.com?", "https://stats.example.com#", "https://stats.example.com#a",
		"https://*.example.com", "https://stats.example.com;script-src *", "https://stats.example.com 'unsafe-inline'",
		"https://a,b.example.com", "https://u:p@stats.example.com", "https://localhost", "https://127.0.0.1",
		"https://[::1]", "https://stats.example.com:0", "https://stats.example.com:65536", "https://stats.example.com:",
		"https://stats.example.com:08443", "https://-bad.example.com", "javascript:alert(1)",
	} {
		t.Setenv("FAPPS_ANALYTICS_ORIGIN", bad)
		if c, err := Load(); err == nil {
			t.Errorf("FAPPS_ANALYTICS_ORIGIN=%q accepted as %q", bad, c.AnalyticsOrigin)
		}
	}
}

func TestTrustedProxies(t *testing.T) {
	t.Setenv("FAPPS_TRUSTED_PROXIES", "10.20.0.0/16, 192.0.2.7 ,2001:db8::/32,::ffff:198.51.100.1")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("10.20.0.0/16"), netip.MustParsePrefix("192.0.2.7/32"),
		netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("198.51.100.1/32"),
	}
	if len(c.TrustedProxies) != len(want) {
		t.Fatalf("proxies = %v", c.TrustedProxies)
	}
	for i := range want {
		if c.TrustedProxies[i] != want[i] {
			t.Fatalf("proxies = %v, want %v", c.TrustedProxies, want)
		}
	}
	t.Setenv("FAPPS_TRUSTED_PROXIES", "10.0.0.0/8,caddy")
	if _, err := Load(); err == nil {
		t.Fatal("a host name was accepted as a proxy")
	}
	t.Setenv("FAPPS_TRUSTED_PROXIES", "")
	if c, err := Load(); err != nil || len(c.TrustedProxies) != 0 {
		t.Fatalf("empty: %v %v", c.TrustedProxies, err)
	}
}

func TestRawURLAndTheDevelopmentOnlyFetchSettings(t *testing.T) {
	t.Setenv("APP_ENV", "demo")
	t.Setenv("FAPPS_ADMIN_REPO", "acme/store-admins")
	t.Setenv("FAPPS_GITHUB_RAW_URL", "")
	t.Setenv("FAPPS_FETCH_CA_FILE", "")
	t.Setenv("FAPPS_FETCH_ALLOW_PRIVATE", "")
	c, err := Load()
	if err != nil || c.GitHub.RawURL != "https://raw.githubusercontent.com" || c.Fetch.CAFile != "" || c.Fetch.AllowPrivate {
		t.Fatalf("defaults: %+v %v", c.GitHub, err)
	}
	t.Setenv("FAPPS_GITHUB_RAW_URL", "https://mock-idp:9443/raw/")
	if c, err := Load(); err != nil || c.GitHub.RawURL != "https://mock-idp:9443/raw" {
		t.Fatalf("raw override: %v", err)
	}

	for _, kv := range [][2]string{{"FAPPS_FETCH_CA_FILE", "/etc/mock-ca.pem"}, {"FAPPS_FETCH_ALLOW_PRIVATE", "1"}} {
		for _, env := range []string{"demo", "production"} {
			t.Setenv("APP_ENV", env)
			t.Setenv(kv[0], kv[1])
			if _, err := Load(); err == nil {
				t.Errorf("%s=%s accepted with APP_ENV=%s", kv[0], kv[1], env)
			}
		}
		t.Setenv("APP_ENV", "development")
		if _, err := Load(); err != nil {
			t.Errorf("%s=%s refused in development: %v", kv[0], kv[1], err)
		}
		t.Setenv(kv[0], "")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("FAPPS_FETCH_ALLOW_PRIVATE", "yes")
	if _, err := Load(); err == nil {
		t.Fatal("FAPPS_FETCH_ALLOW_PRIVATE=yes accepted")
	}
	t.Setenv("FAPPS_FETCH_ALLOW_PRIVATE", "1")
	t.Setenv("FAPPS_FETCH_CA_FILE", "/etc/mock-ca.pem")
	if c, err := Load(); err != nil || !c.Fetch.AllowPrivate || c.Fetch.CAFile != "/etc/mock-ca.pem" {
		t.Fatalf("development: %+v %v", c.Fetch, err)
	}
}

func TestAdminRepoAndToken(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("FAPPS_ADMIN_REPO", "")
	t.Setenv("FAPPS_ADMIN_TOKEN", "")
	t.Setenv("FAPPS_ADMINS", "")
	t.Setenv("FAPPS_REVIEWERS", "")
	c, err := Load()
	if err != nil || c.Admin.Repo != "" || c.Admin.Token != "" || len(c.Admin.Admins) != 0 {
		t.Fatalf("no default repository: %+v %v", c.Admin, err)
	}
	t.Setenv("FAPPS_ADMIN_REPO", "acme/store-staff.v2")
	t.Setenv("FAPPS_ADMIN_TOKEN", " github_pat_x ")
	if c, err := Load(); err != nil || c.Admin.Repo != "acme/store-staff.v2" || c.Admin.Token != "github_pat_x" {
		t.Fatalf("override: %+v %v", c.Admin, err)
	}
	for _, bad := range []string{"acme", "acme/", "/x", "acme/x/y", "https://github.com/acme/x", "-acme/x", "acme/x.", "acme/a b"} {
		t.Setenv("FAPPS_ADMIN_REPO", bad)
		if _, err := Load(); err == nil {
			t.Errorf("FAPPS_ADMIN_REPO=%q accepted", bad)
		}
	}
}

// Outside development a store needs a way to have an administrator: the
// repository or the list. Without either it does not start.
func TestOutsideDevelopmentAnAdministratorSourceIsRequired(t *testing.T) {
	t.Setenv("FAPPS_ADMIN_REPO", "")
	t.Setenv("FAPPS_ADMINS", "")
	t.Setenv("FAPPS_REVIEWERS", "alice:12345")
	for _, env := range []string{"production", "demo"} {
		t.Setenv("APP_ENV", env)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "neither FAPPS_ADMIN_REPO nor FAPPS_ADMINS") {
			t.Errorf("APP_ENV=%s without a source: %v", env, err)
		}
	}
	t.Setenv("APP_ENV", "production")
	t.Setenv("FAPPS_ADMINS", "12345")
	if c, err := Load(); err != nil || c.Admin.Repo != "" || len(c.Admin.Admins) != 1 {
		t.Fatalf("the list alone: %+v %v", c.Admin, err)
	}
	t.Setenv("FAPPS_ADMINS", "")
	t.Setenv("FAPPS_ADMIN_REPO", "acme/store-admins")
	if _, err := Load(); err != nil {
		t.Fatalf("the repository alone: %v", err)
	}
	t.Setenv("FAPPS_ADMIN_REPO", "")
	t.Setenv("APP_ENV", "development")
	if _, err := Load(); err != nil {
		t.Fatalf("development starts without one: %v", err)
	}
}

// The lists name accounts by their numeric GitHub id; a bare login is
// refused with the reason, since a login can change hands.
func TestAdminAndReviewerLists(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("FAPPS_ADMINS", " 583231 , alice:12345,bob-the-2nd:99 ,583231")
	t.Setenv("FAPPS_REVIEWERS", "carol:7")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []Member{{ID: 583231}, {ID: 12345, Login: "alice"}, {ID: 99, Login: "bob-the-2nd"}}
	if len(c.Admin.Admins) != len(want) {
		t.Fatalf("admins %+v", c.Admin.Admins)
	}
	for i := range want {
		if c.Admin.Admins[i] != want[i] {
			t.Fatalf("admins %+v, want %+v", c.Admin.Admins, want)
		}
	}
	if len(c.Admin.Reviewers) != 1 || c.Admin.Reviewers[0] != (Member{ID: 7, Login: "carol"}) {
		t.Fatalf("reviewers %+v", c.Admin.Reviewers)
	}
	t.Setenv("FAPPS_REVIEWERS", "")
	t.Setenv("FAPPS_ADMINS", "12345,alice")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), `"alice" is a GitHub login`) ||
		!strings.Contains(err.Error(), "alice:12345") || !strings.Contains(err.Error(), "api.github.com/users/alice") {
		t.Fatalf("a bare login: %v", err)
	}
	for _, bad := range []string{"0", "-5", "12a", "alice:", ":12", "alice:bob", "al ice:1", "-alice:1", "1:alice",
		"alice:007", "alice:1:2", "9223372036854775808"} {
		t.Setenv("FAPPS_ADMINS", bad)
		if _, err := Load(); err == nil {
			t.Errorf("FAPPS_ADMINS=%q accepted", bad)
		}
	}
	t.Setenv("FAPPS_ADMINS", "")
	t.Setenv("FAPPS_REVIEWERS", "dave")
	if _, err := Load(); err == nil || !strings.HasPrefix(err.Error(), "FAPPS_REVIEWERS:") {
		t.Fatalf("a bare login among the reviewers: %v", err)
	}
}

func TestStoreNameDefaultsToTheHost(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("FAPPS_STORE_NAME", "")
	t.Setenv("APP_URL", "")
	t.Setenv("FAPPS_PUBLIC_URL", "https://Apps.Example.com:8443/")
	c, err := Load()
	if err != nil || c.Brand.Name != "apps.example.com:8443" {
		t.Fatalf("default: %q %v", c.Brand.Name, err)
	}
	t.Setenv("FAPPS_PUBLIC_URL", "")
	if c, err := Load(); err != nil || c.Brand.Name != FallbackStoreName {
		t.Fatalf("no public URL: %q %v", c.Brand.Name, err)
	}
	t.Setenv("FAPPS_STORE_NAME", "  Acme Apps ")
	if c, err := Load(); err != nil || c.Brand.Name != "Acme Apps" {
		t.Fatalf("set: %q %v", c.Brand.Name, err)
	}
	for _, bad := range []string{"two\nlines", "tab\there", strings.Repeat("x", MaxStoreNameRunes+1), string(rune(0x202e)) + "sppA emcA"} {
		t.Setenv("FAPPS_STORE_NAME", bad)
		if _, err := Load(); err == nil {
			t.Errorf("FAPPS_STORE_NAME=%q accepted", bad)
		}
	}
	t.Setenv("FAPPS_STORE_NAME", strings.Repeat("é", MaxStoreNameRunes))
	if _, err := Load(); err != nil {
		t.Errorf("60 characters (more bytes) refused: %v", err)
	}
}

func TestOperatorSettings(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	for _, name := range []string{"FAPPS_OPERATOR_NAME", "FAPPS_OPERATOR_URL", "FAPPS_CONTACT", "FAPPS_TERMS_URL", "FAPPS_PRIVACY_URL", "FAPPS_VAPID_SUBJECT"} {
		t.Setenv(name, "")
	}
	t.Setenv("FAPPS_PUBLIC_URL", "https://apps.example.com")
	c, err := Load()
	if err != nil || c.Brand.OperatorName != "" || c.Brand.OperatorURL != "" || c.Brand.Contact != "" || c.Brand.TermsURL != "" || c.Brand.PrivacyURL != "" {
		t.Fatalf("unset: %+v %v", c.Brand, err)
	}
	t.Setenv("FAPPS_OPERATOR_NAME", "Acme Corp.")
	t.Setenv("FAPPS_OPERATOR_URL", "https://www.example.com/")
	t.Setenv("FAPPS_CONTACT", "mailto:store@example.com")
	t.Setenv("FAPPS_TERMS_URL", " https://www.example.com/legal/terms?lang=en#store ")
	t.Setenv("FAPPS_PRIVACY_URL", "https://www.example.com:8443/privacy")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Brand.OperatorName != "Acme Corp." || c.Brand.OperatorURL != "https://www.example.com/" || c.Brand.Contact != "mailto:store@example.com" ||
		c.Brand.TermsURL != "https://www.example.com/legal/terms?lang=en#store" || c.Brand.PrivacyURL != "https://www.example.com:8443/privacy" {
		t.Fatalf("set: %+v", c.Brand)
	}
	// The push services' contact follows the store's contact.
	if c.Push.Subject != "mailto:store@example.com" {
		t.Fatalf("VAPID subject %q", c.Push.Subject)
	}
	t.Setenv("FAPPS_CONTACT", "https://www.example.com/contact")
	if c, err := Load(); err != nil || c.Brand.Contact != "https://www.example.com/contact" || c.Push.Subject != "https://www.example.com/contact" {
		t.Fatalf("https contact: %+v %v", c.Brand, err)
	}
	for name, bads := range map[string][]string{
		"FAPPS_OPERATOR_URL": {"http://www.example.com", "javascript:alert(1)", "https://", "https://u:p@example.com", "https://127.0.0.1/",
			"https://exa mple.com", "https://example.com/a b", `https://example.com/"x"`, "https://example.com:0", "//example.com", "www.example.com"},
		"FAPPS_TERMS_URL":   {"ftp://example.com/terms", "https://[::1]/terms", "https://example.com/<script>"},
		"FAPPS_PRIVACY_URL": {"data:text/html,hi", "https://example.com:99999/"},
		"FAPPS_CONTACT": {"store@example.com", "mailto:", "mailto:not an address", "mailto:Store <store@example.com>",
			"mailto:store@example.com?subject=hi", "http://example.com/contact", "javascript:alert(1)"},
		"FAPPS_OPERATOR_NAME": {"Acme\nCorp", strings.Repeat("a", MaxOperatorNameRunes+1)},
	} {
		for _, bad := range bads {
			t.Setenv(name, bad)
			if _, err := Load(); err == nil {
				t.Errorf("%s=%q accepted", name, bad)
			}
		}
		t.Setenv(name, "")
	}
}

func TestStoreLogo(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("FAPPS_STORE_LOGO", "")
	if c, err := Load(); err != nil || c.Brand.Logo != nil || c.Brand.LogoType != "" {
		t.Fatalf("unset: %+v %v", c.Brand, err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	svg := []byte("\ufeff<?xml version=\"1.0\"?>\n<!-- the logo -->\n<!DOCTYPE svg>\n<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 32 32\"><rect width=\"32\" height=\"32\"/></svg>\n")
	for _, tc := range []struct {
		file string
		body []byte
		typ  string
	}{{"logo.png", png, "image/png"}, {"logo.svg", svg, "image/svg+xml"}, {"no-extension", []byte("<svg/>"), "image/svg+xml"}} {
		t.Setenv("FAPPS_STORE_LOGO", write(tc.file, tc.body))
		c, err := Load()
		if err != nil || c.Brand.LogoType != tc.typ || !bytes.Equal(c.Brand.Logo, tc.body) {
			t.Errorf("%s: %q %v", tc.file, c.Brand.LogoType, err)
		}
	}
	for name, body := range map[string][]byte{
		"logo.gif":   []byte("GIF89a"),
		"page.html":  []byte("<html><svg></svg></html>"),
		"svgish.svg": []byte("<svgfoo/>"),
		"big.png":    append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxLogoBytes)...),
		"empty.svg":  {},
	} {
		t.Setenv("FAPPS_STORE_LOGO", write(name, body))
		if _, err := Load(); err == nil || !strings.HasPrefix(err.Error(), "FAPPS_STORE_LOGO:") {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	for _, p := range []string{filepath.Join(dir, "missing.svg"), dir} {
		t.Setenv("FAPPS_STORE_LOGO", p)
		if _, err := Load(); err == nil {
			t.Errorf("%s accepted", p)
		}
	}
}

// The Keycloak settings are gone; one still set in an old environment is
// reported, never an error.
func TestRemovedKeycloakSettingsAreReportedNotRefused(t *testing.T) {
	t.Setenv("FAPPS_ADMIN_REPO", "")
	for _, name := range RemovedVariables {
		t.Setenv(name, "")
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Removed) != len(RemovedVariables) {
		t.Fatalf("set (even empty) = %v, want all of %v", c.Removed, RemovedVariables)
	}
	for _, name := range RemovedVariables {
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FAPPS_OIDC_CLIENT_SECRET", "left-over")
	c, err = Load()
	if err != nil || len(c.Removed) != 1 || c.Removed[0] != "FAPPS_OIDC_CLIENT_SECRET" {
		t.Fatalf("removed = %v %v", c.Removed, err)
	}
}

func TestDataKey(t *testing.T) {
	t.Setenv("FAPPS_DATA_KEY", "")
	c, err := Load()
	if err != nil || c.DataKey != nil {
		t.Fatalf("unset: %v %v", c.DataKey, err)
	}
	t.Setenv("FAPPS_DATA_KEY", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	c, err = Load()
	if err != nil || len(c.DataKey) != 32 || c.DataKey[31] != 31 {
		t.Fatalf("32 bytes: %v %v", c.DataKey, err)
	}
	for _, bad := range []string{"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwd", "not base64 !!"} {
		t.Setenv("FAPPS_DATA_KEY", bad)
		if _, err := Load(); err == nil {
			t.Errorf("FAPPS_DATA_KEY=%q accepted", bad)
		}
	}
}

func TestLicenseKeyAndItsIDGoTogether(t *testing.T) {
	t.Setenv("FAPPS_LICENSE_KEY", "")
	t.Setenv("FAPPS_LICENSE_KEY_ID", "lic-1")
	if _, err := Load(); err == nil {
		t.Fatal("an id without a key was accepted")
	}
	t.Setenv("FAPPS_LICENSE_KEY", "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	c, err := Load()
	if err != nil || c.Keys.LicenseKeyID != "lic-1" {
		t.Fatalf("%v %v", c, err)
	}
}

// A typical secret generator writes 32 random bytes as STANDARD base64:
// padded, 44 characters, '+' and '/' allowed. That form must load as is
// (a value that does not parse stops the server).
func TestDataKeyInStandardBase64(t *testing.T) {
	raw := bytes.Repeat([]byte{0xfb, 0xff, 0xbf}, 11)[:32]
	v := base64.StdEncoding.EncodeToString(raw)
	if len(v) != 44 || !strings.Contains(v, "+") || !strings.Contains(v, "/") || !strings.HasSuffix(v, "=") {
		t.Fatalf("test value %q is not that shape", v)
	}
	t.Setenv("FAPPS_DATA_KEY", v)
	c, err := Load()
	if err != nil || !bytes.Equal(c.DataKey, raw) {
		t.Fatalf("FAPPS_DATA_KEY=%q: %v %x", v, err, c.DataKey)
	}
	t.Setenv("FAPPS_DATA_KEY", " "+v+"\n")
	if c, err := Load(); err != nil || !bytes.Equal(c.DataKey, raw) {
		t.Fatalf("with surrounding space: %v", err)
	}
}

func TestPluginValidatorsAndTheBinaryCap(t *testing.T) {
	t.Setenv("FAPPS_PLUGIN_VALIDATORS", "latest=/spool/latest/ , arm=/spool/arm")
	t.Setenv("FAPPS_MAX_BINARY_MB", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.PluginValidators) != 2 || c.PluginValidators[0] != (PluginValidator{Label: "latest", Dir: "/spool/latest"}) ||
		c.PluginValidators[1].Label != "arm" {
		t.Fatalf("plugin validators %+v", c.PluginValidators)
	}
	if c.Fetch.MaxBinary != 256*mib {
		t.Fatalf("binary cap %d", c.Fetch.MaxBinary)
	}
	for _, bad := range []string{"latest", "latest=spool", "=/spool", "a=/x,a=/y"} {
		t.Setenv("FAPPS_PLUGIN_VALIDATORS", bad)
		if _, err := Load(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	t.Setenv("FAPPS_PLUGIN_VALIDATORS", "")
	if c, err := Load(); err != nil || len(c.PluginValidators) != 0 {
		t.Fatalf("none configured: %v %+v", err, c)
	}
}

func TestVAPIDPairAndSubject(t *testing.T) {
	t.Setenv("FAPPS_PUBLIC_URL", "https://apps.example")
	t.Setenv("FAPPS_VAPID_SUBJECT", "")
	t.Setenv("FAPPS_VAPID_PUBLIC_KEY", "")
	t.Setenv("FAPPS_VAPID_PRIVATE_KEY", "")
	c, err := Load()
	if err != nil || c.Push.PublicKey != "" || c.Push.Subject != "https://apps.example" {
		t.Fatalf("no pair: %+v %v", c.Push, err)
	}
	t.Setenv("FAPPS_VAPID_PUBLIC_KEY", "BPub")
	if _, err := Load(); err == nil {
		t.Fatal("a public key without its private key was accepted")
	}
	t.Setenv("FAPPS_VAPID_PRIVATE_KEY", "priv")
	t.Setenv("FAPPS_VAPID_SUBJECT", "ops@example.com")
	if _, err := Load(); err == nil {
		t.Fatal("a subject that is neither mailto: nor https: was accepted")
	}
	t.Setenv("FAPPS_VAPID_SUBJECT", "mailto:ops@example.com")
	if c, err := Load(); err != nil || c.Push.Subject != "mailto:ops@example.com" || c.Push.PrivateKey != "priv" {
		t.Fatalf("pair: %+v %v", c.Push, err)
	}
}
