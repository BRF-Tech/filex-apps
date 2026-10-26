// Package config reads the server's settings from the environment.
//
// Every variable the server reads is named here, and ONLY here: docs/ARCHITECTURE.md
// "Configuration" lists the same names, and .env.example carries the non-secret
// ones. Secrets (the OAuth client secret, the server's GitHub tokens, the
// session key, the signing keys) come from the environment and nowhere else -
// never from a file in the repository, never from the database.
//
// APP_URL, APP_ENV, DATABASE_URL and REDIS_URL keep the names a hosting
// platform commonly injects; the rest are FAPPS_*.
package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Config is the server's whole configuration.
type Config struct {
	// Listen is the address the HTTP server binds (FAPPS_LISTEN).
	Listen string
	// PublicURL is the store's own origin, no trailing slash (FAPPS_PUBLIC_URL,
	// else APP_URL). Every absolute URL the server writes (the index's media
	// addresses, OAuth callbacks) starts with it.
	PublicURL string
	// Env is APP_ENV: "demo", "production" or "development".
	Env string

	DatabaseURL string // DATABASE_URL (required to serve anything but /healthz)
	RedisURL    string // REDIS_URL (rate limits; optional, an in-memory limiter stands in)

	// Brand is how the store names and presents itself: its name, an
	// optional logo and who runs it (FAPPS_STORE_*, FAPPS_OPERATOR_*, ...).
	Brand Brand

	// AnalyticsOrigin is the origin of an optional page counter
	// (FAPPS_ANALYTICS_ORIGIN, e.g. https://stats.example.com): https, a host and
	// an optional port, nothing else. Set, the page's CSP lets a script load
	// from it and the page send to it (script-src, connect-src). The store
	// prints no counter script itself: the reverse proxy in front adds it.
	// Empty: the CSP allows no other origin.
	AnalyticsOrigin string

	// TrustedProxies are the reverse proxies whose X-Forwarded-For the server
	// believes (FAPPS_TRUSTED_PROXIES, comma list of addresses or CIDRs). The
	// client address feeds the rate limits and the audit log. Empty: only a
	// loopback or private peer is trusted, and only for the right-most hop.
	TrustedProxies []netip.Prefix

	// SessionKey signs the session and CSRF cookies (FAPPS_SESSION_KEY,
	// base64, at least 32 bytes). Required outside development.
	SessionKey string

	// DataKey is the secret the license-key hashes, the sealed license keys of
	// install intents and the saved filex instances are keyed with
	// (FAPPS_DATA_KEY, base64, at least 32 bytes; each use derives its own
	// key with HKDF). Without it those features answer 503 (development
	// makes a random one for the process). Changing it makes every license
	// key unknown and every saved instance unreadable.
	DataKey []byte

	GitHub GitHubConfig
	Admin  AdminConfig
	Keys   KeyConfig
	Index  IndexConfig
	Fetch  FetchConfig
	Push   PushConfig

	// Validators are the filex instances the dry run is asked of
	// (FAPPS_VALIDATORS, "<label>=<base url>,..."), signed in to with
	// ValidatorEmail / ValidatorPassword.
	Validators        []Validator
	ValidatorEmail    string
	ValidatorPassword string

	// PluginValidators run a storage plugin's conformance
	// (FAPPS_PLUGIN_VALIDATORS, "<label>=<spool directory>,..."): a
	// `filex plugin-validator` container with no network that shares the
	// directory with the store (internal/plugincheck). None: a storage
	// plugin's submission fails its dry run, and nothing else changes.
	PluginValidators []PluginValidator

	// Removed lists the variables of RemovedVariables that are still set:
	// the server ignores them and says so at start-up and in /readyz.
	Removed []string
}

// GitHubConfig is the store's sign-in (a GitHub OAuth App), for everyone.
type GitHubConfig struct {
	ClientID     string // FAPPS_GITHUB_CLIENT_ID
	ClientSecret string // FAPPS_GITHUB_CLIENT_SECRET
	// Token is an optional server-side read token for background re-checks
	// (FAPPS_GITHUB_TOKEN): public read only, never a write scope.
	Token string
	// OAuthURL is GitHub's sign-in host (FAPPS_GITHUB_OAUTH_URL, default
	// https://github.com) and APIURL its REST API (FAPPS_GITHUB_API_URL,
	// default https://api.github.com), no trailing slash. Only tests and the
	// end-to-end stack's mock GitHub change them.
	OAuthURL string
	APIURL   string
	// RawURL is where files are read at a commit (FAPPS_GITHUB_RAW_URL,
	// default https://raw.githubusercontent.com), no trailing slash.
	RawURL string
}

// AdminConfig is where the store roles come from (docs/ARCHITECTURE.md
// "Roles"): a person's permission on one GitHub repository, read at every
// sign-in with the server's own token, and two lists of GitHub accounts.
// Both may be given; a person then holds the higher of the two roles.
// Outside development at least one of Repo and Admins is required: a store
// nobody can administer does not start.
type AdminConfig struct {
	// Repo is the administration repository, "owner/name" (FAPPS_ADMIN_REPO,
	// no default): admin, maintain or write on it make an administrator,
	// triage a reviewer, anything else no role. Empty: no repository is
	// asked, the lists alone decide.
	Repo string
	// Token reads that permission (FAPPS_ADMIN_TOKEN, secret): a
	// fine-grained token of the repository's owner with Metadata: read on
	// that repository only. Without it the repository grants no role.
	Token string
	// Admins and Reviewers are GitHub accounts named by their numeric user
	// id (FAPPS_ADMINS, FAPPS_REVIEWERS: comma lists of `12345` or
	// `login:12345`; the login is a label only). Never by login alone: a
	// login can be given up and registered again by someone else, the id
	// stays with the account. An account on both lists is an administrator.
	Admins    []Member
	Reviewers []Member
}

// Member is one GitHub account of FAPPS_ADMINS or FAPPS_REVIEWERS.
type Member struct {
	ID    int64  // GitHub's numeric user id: what is matched
	Login string // the label it was written with ("" when none): display only
}

// Brand is how the store names and presents itself (docs/ARCHITECTURE.md
// "Configuration"). Nothing here is a secret, and none of it changes what
// the store signs or whom it trusts.
type Brand struct {
	// Name is the store's name (FAPPS_STORE_NAME): the interface's brand,
	// the tab title's suffix, the web app manifest, the notifications'
	// titles and footers. Default: the host of PublicURL, so a store that
	// sets nothing never presents itself as another store.
	Name string
	// Logo is the image of FAPPS_STORE_LOGO (the path of a local SVG or PNG
	// file, read at start), which the store serves from its own origin
	// (GET /api/v1/store/logo); LogoType is its media type. Empty: filex's
	// mark, as before.
	Logo     []byte
	LogoType string
	// Who runs the store, shown in the page footer when set:
	// FAPPS_OPERATOR_NAME (text) and FAPPS_OPERATOR_URL (https),
	// FAPPS_CONTACT (mailto: or https), FAPPS_TERMS_URL and
	// FAPPS_PRIVACY_URL (https).
	OperatorName string
	OperatorURL  string
	Contact      string
	TermsURL     string
	PrivacyURL   string
}

// Limits of the brand settings.
const (
	MaxStoreNameRunes    = 60
	MaxOperatorNameRunes = 100
	MaxLogoBytes         = 256 << 10
	// FallbackStoreName is the name when neither FAPPS_STORE_NAME nor a
	// public URL with a host gives one.
	FallbackStoreName = "App store"
)

// RemovedVariables are settings this server no longer reads (the Keycloak
// sign-in, removed 2026-10-04: roles come from FAPPS_ADMIN_REPO). One that
// is still set is a warning, never an error, so an old environment does not
// keep the store down.
var RemovedVariables = []string{
	"FAPPS_OIDC_ISSUER", "FAPPS_OIDC_CLIENT_ID", "FAPPS_OIDC_CLIENT_SECRET",
	"FAPPS_OIDC_REVIEWER_ROLE", "FAPPS_OIDC_ADMIN_ROLE",
}

// repoRe is a GitHub "owner/name": owner of letters, digits and hyphens;
// name of letters, digits, '.', '_' and '-'.
var repoRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

// KeyConfig holds the signing keys: the index key signs /v1/index.json
// (and install intents), the artifact key signs each version's
// module/bundle/manifest hash, the license key signs license verdicts.
type KeyConfig struct {
	IndexKey      string // FAPPS_INDEX_KEY (ed25519 seed or private key, hex or base64)
	IndexKeyID    string // FAPPS_INDEX_KEY_ID
	ArtifactKey   string // FAPPS_ARTIFACT_KEY
	ArtifactKeyID string // FAPPS_ARTIFACT_KEY_ID
	// LicenseKey signs the verdicts of POST /v1/licenses/verify
	// (FAPPS_LICENSE_KEY + FAPPS_LICENSE_KEY_ID). Optional: without it (and
	// without demo keys) license checks answer 503.
	LicenseKey   string
	LicenseKeyID string
	// DemoKeys lets a demo generate throw-away keys when none are given
	// (FAPPS_DEMO_KEYS=1). Refused unless Env is "demo" or "development".
	DemoKeys bool
}

// IndexConfig governs the signed index's lifetime.
type IndexConfig struct {
	TTL time.Duration // FAPPS_INDEX_TTL_DAYS (default 60)
	// RefreshBefore re-signs the index (serial + 1, same content) once less
	// than this is left before expires_at (FAPPS_INDEX_REFRESH_DAYS, default 30).
	RefreshBefore time.Duration
}

// FetchConfig bounds every outbound download (the SSRF guard's allow list
// and the size caps).
type FetchConfig struct {
	AllowHosts   []string // FAPPS_FETCH_ALLOW_HOSTS (comma list; default DefaultAllowHosts)
	MaxManifest  int64    // FAPPS_MAX_MANIFEST_MB (default 16)
	MaxWasm      int64    // FAPPS_MAX_WASM_MB (default 64)
	MaxUIBundle  int64    // FAPPS_MAX_UI_MB (default 128)
	MaxMedia     int64    // FAPPS_MAX_MEDIA_MB (default 5)
	MaxBinary    int64    // FAPPS_MAX_BINARY_MB (default 256): one build of a storage plugin
	Timeout      time.Duration
	MaxRedirects int
	// CAFile is a PEM file of certificates the guarded client also trusts
	// (FAPPS_FETCH_CA_FILE), and AllowPrivate lets it dial private and
	// loopback addresses (FAPPS_FETCH_ALLOW_PRIVATE=1). Both exist for the
	// end-to-end stack's mock GitHub and are refused unless APP_ENV is
	// development: anywhere else they would open the SSRF guard.
	CAFile       string
	AllowPrivate bool
}

// PushConfig is the Web Push key pair of the browser notifications
// (internal/notify): FAPPS_VAPID_PUBLIC_KEY and FAPPS_VAPID_PRIVATE_KEY
// (base64url, the 65-byte public point and the 32-byte private scalar; set
// together or not at all; the private one is a secret) and
// FAPPS_VAPID_SUBJECT (mailto: or https:, the push services' contact;
// default FAPPS_PUBLIC_URL when it is https). Without the pair the browser
// notifications are off and the webhooks still work.
type PushConfig struct {
	PublicKey  string
	PrivateKey string
	Subject    string
}

// Validator is one filex instance the dry run is asked of.
type Validator struct {
	Label   string // "latest", "floor", ...
	BaseURL string // e.g. http://filex-latest:5212
}

// PluginValidator is one storage plugin validator: its label and the spool
// directory the store shares with it.
type PluginValidator struct {
	Label string // "latest", ...
	Dir   string // e.g. /spool/latest (absolute)
}

// DefaultAllowHosts are the only hosts the server downloads from. GitHub
// serves release assets from more than one host and has moved them before;
// the list is configuration so a move is an env change, not a release.
// avatars.githubusercontent.com serves a publisher's GitHub avatar, mirrored
// once when the publisher becomes active (an image: size-capped and decoded
// like any listing image).
var DefaultAllowHosts = []string{
	"github.com",
	"api.github.com",
	"raw.githubusercontent.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
	"github-releases.githubusercontent.com",
	"codeload.github.com",
	"avatars.githubusercontent.com",
}

const mib = 1 << 20

// Load reads the environment. It fails only on values that are present and
// malformed; what is missing is reported by Validate, so /healthz can still
// come up and say what is wrong.
func Load() (*Config, error) {
	c := &Config{
		Listen:      env("FAPPS_LISTEN", "0.0.0.0:8080"),
		Env:         env("APP_ENV", "development"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		RedisURL:    os.Getenv("REDIS_URL"),
		SessionKey:  os.Getenv("FAPPS_SESSION_KEY"),
		GitHub: GitHubConfig{
			ClientID:     os.Getenv("FAPPS_GITHUB_CLIENT_ID"),
			ClientSecret: os.Getenv("FAPPS_GITHUB_CLIENT_SECRET"),
			Token:        os.Getenv("FAPPS_GITHUB_TOKEN"),
		},
		Admin: AdminConfig{
			Repo:  strings.TrimSpace(os.Getenv("FAPPS_ADMIN_REPO")),
			Token: strings.TrimSpace(os.Getenv("FAPPS_ADMIN_TOKEN")),
		},
		Keys: KeyConfig{
			IndexKey:      os.Getenv("FAPPS_INDEX_KEY"),
			IndexKeyID:    os.Getenv("FAPPS_INDEX_KEY_ID"),
			ArtifactKey:   os.Getenv("FAPPS_ARTIFACT_KEY"),
			ArtifactKeyID: os.Getenv("FAPPS_ARTIFACT_KEY_ID"),
			LicenseKey:    os.Getenv("FAPPS_LICENSE_KEY"),
			LicenseKeyID:  os.Getenv("FAPPS_LICENSE_KEY_ID"),
			DemoKeys:      os.Getenv("FAPPS_DEMO_KEYS") == "1",
		},
		ValidatorEmail:    env("FAPPS_VALIDATOR_EMAIL", "validator@local"),
		ValidatorPassword: os.Getenv("FAPPS_VALIDATOR_PASSWORD"),
	}
	c.PublicURL = strings.TrimRight(env("FAPPS_PUBLIC_URL", os.Getenv("APP_URL")), "/")
	var err error
	if c.Brand, err = loadBrand(c.PublicURL); err != nil {
		return nil, err
	}
	c.Push = PushConfig{
		PublicKey:  strings.TrimSpace(os.Getenv("FAPPS_VAPID_PUBLIC_KEY")),
		PrivateKey: strings.TrimSpace(os.Getenv("FAPPS_VAPID_PRIVATE_KEY")),
		Subject:    strings.TrimSpace(os.Getenv("FAPPS_VAPID_SUBJECT")),
	}
	if (c.Push.PublicKey == "") != (c.Push.PrivateKey == "") {
		return nil, fmt.Errorf("FAPPS_VAPID_PUBLIC_KEY and FAPPS_VAPID_PRIVATE_KEY are set together or not at all")
	}
	if c.Push.Subject == "" {
		// The push services' contact: the store's own contact when it has
		// one (mailto: or https, already checked), else its address.
		switch {
		case c.Brand.Contact != "":
			c.Push.Subject = c.Brand.Contact
		case strings.HasPrefix(c.PublicURL, "https://"):
			c.Push.Subject = c.PublicURL
		default:
			c.Push.Subject = "mailto:notifications@example.com"
		}
	}
	if c.Push.PublicKey != "" && !strings.HasPrefix(c.Push.Subject, "mailto:") && !strings.HasPrefix(c.Push.Subject, "https://") {
		return nil, fmt.Errorf("FAPPS_VAPID_SUBJECT must be a mailto: or https: address")
	}
	for _, name := range RemovedVariables {
		if _, set := os.LookupEnv(name); set {
			c.Removed = append(c.Removed, name)
		}
	}
	if c.Admin.Repo != "" && (!repoRe.MatchString(c.Admin.Repo) || strings.HasSuffix(c.Admin.Repo, ".")) {
		return nil, fmt.Errorf("FAPPS_ADMIN_REPO: %q is not a GitHub owner/name", c.Admin.Repo)
	}
	if c.Admin.Admins, err = members("FAPPS_ADMINS"); err != nil {
		return nil, err
	}
	if c.Admin.Reviewers, err = members("FAPPS_REVIEWERS"); err != nil {
		return nil, err
	}
	if c.Env != "development" && c.Admin.Repo == "" && len(c.Admin.Admins) == 0 {
		return nil, fmt.Errorf("neither FAPPS_ADMIN_REPO nor FAPPS_ADMINS is set (APP_ENV is %q): outside development the store "+
			"needs a way to have an administrator - a GitHub repository whose collaborators are its staff (FAPPS_ADMIN_REPO + "+
			"FAPPS_ADMIN_TOKEN) or a list of GitHub user ids (FAPPS_ADMINS, e.g. alice:12345)", c.Env)
	}

	if c.DataKey, err = ParseDataKey(os.Getenv("FAPPS_DATA_KEY")); err != nil {
		return nil, err
	}
	if (c.Keys.LicenseKey == "") != (c.Keys.LicenseKeyID == "") {
		return nil, fmt.Errorf("FAPPS_LICENSE_KEY and FAPPS_LICENSE_KEY_ID are set together or not at all")
	}
	if c.GitHub.OAuthURL, err = baseURL("FAPPS_GITHUB_OAUTH_URL", "https://github.com"); err != nil {
		return nil, err
	}
	if c.GitHub.APIURL, err = baseURL("FAPPS_GITHUB_API_URL", "https://api.github.com"); err != nil {
		return nil, err
	}
	if c.GitHub.RawURL, err = baseURL("FAPPS_GITHUB_RAW_URL", "https://raw.githubusercontent.com"); err != nil {
		return nil, err
	}
	if c.AnalyticsOrigin, err = httpsOrigin("FAPPS_ANALYTICS_ORIGIN"); err != nil {
		return nil, err
	}
	for _, p := range splitList(os.Getenv("FAPPS_TRUSTED_PROXIES")) {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			addr, aerr := netip.ParseAddr(p)
			if aerr != nil {
				return nil, fmt.Errorf("FAPPS_TRUSTED_PROXIES: %q is neither an address nor a CIDR", p)
			}
			addr = addr.Unmap()
			pfx = netip.PrefixFrom(addr, addr.BitLen())
		}
		c.TrustedProxies = append(c.TrustedProxies, pfx.Masked())
	}

	if c.Index.TTL, err = days("FAPPS_INDEX_TTL_DAYS", 60); err != nil {
		return nil, err
	}
	if c.Index.RefreshBefore, err = days("FAPPS_INDEX_REFRESH_DAYS", 30); err != nil {
		return nil, err
	}

	c.Fetch.AllowHosts = DefaultAllowHosts
	if v := strings.TrimSpace(os.Getenv("FAPPS_FETCH_ALLOW_HOSTS")); v != "" {
		c.Fetch.AllowHosts = splitList(v)
	}
	for _, s := range []struct {
		name string
		def  int64
		dst  *int64
	}{
		{"FAPPS_MAX_MANIFEST_MB", 16, &c.Fetch.MaxManifest},
		{"FAPPS_MAX_WASM_MB", 64, &c.Fetch.MaxWasm},
		{"FAPPS_MAX_UI_MB", 128, &c.Fetch.MaxUIBundle},
		{"FAPPS_MAX_MEDIA_MB", 5, &c.Fetch.MaxMedia},
		{"FAPPS_MAX_BINARY_MB", 256, &c.Fetch.MaxBinary},
	} {
		n, err := intEnv(s.name, s.def)
		if err != nil {
			return nil, err
		}
		*s.dst = n * mib
	}
	c.Fetch.Timeout = 60 * time.Second
	c.Fetch.MaxRedirects = 5
	c.Fetch.CAFile = strings.TrimSpace(os.Getenv("FAPPS_FETCH_CA_FILE"))
	switch v := strings.TrimSpace(os.Getenv("FAPPS_FETCH_ALLOW_PRIVATE")); v {
	case "", "0":
	case "1":
		c.Fetch.AllowPrivate = true
	default:
		return nil, fmt.Errorf("FAPPS_FETCH_ALLOW_PRIVATE: %q is not 0 or 1", v)
	}
	if (c.Fetch.CAFile != "" || c.Fetch.AllowPrivate) && c.Env != "development" {
		return nil, fmt.Errorf("FAPPS_FETCH_CA_FILE and FAPPS_FETCH_ALLOW_PRIVATE are refused outside APP_ENV=development (APP_ENV is %q): they open the outbound guard", c.Env)
	}

	if v := strings.TrimSpace(os.Getenv("FAPPS_VALIDATORS")); v != "" {
		for _, part := range splitList(v) {
			label, base, ok := strings.Cut(part, "=")
			if !ok || label == "" || base == "" {
				return nil, fmt.Errorf("FAPPS_VALIDATORS: %q is not <label>=<base url>", part)
			}
			c.Validators = append(c.Validators, Validator{Label: label, BaseURL: strings.TrimRight(base, "/")})
		}
	}
	if v := strings.TrimSpace(os.Getenv("FAPPS_PLUGIN_VALIDATORS")); v != "" {
		seen := map[string]bool{}
		for _, part := range splitList(v) {
			label, dir, ok := strings.Cut(part, "=")
			label, dir = strings.TrimSpace(label), strings.TrimSpace(dir)
			if !ok || label == "" || dir == "" || !strings.HasPrefix(dir, "/") || seen[label] {
				return nil, fmt.Errorf("FAPPS_PLUGIN_VALIDATORS: %q is not <label>=<absolute spool directory> (each label once)", part)
			}
			seen[label] = true
			c.PluginValidators = append(c.PluginValidators, PluginValidator{Label: label, Dir: strings.TrimRight(dir, "/")})
		}
	}
	return c, nil
}

// Validate reports the settings a full server needs and does not have. The
// skeleton serves /healthz regardless; the lanes call this before mounting
// the routes that need each piece.
func (c *Config) Validate() []string {
	var missing []string
	need := func(ok bool, what string) {
		if !ok {
			missing = append(missing, what)
		}
	}
	need(c.DatabaseURL != "", "DATABASE_URL")
	need(c.PublicURL != "", "FAPPS_PUBLIC_URL or APP_URL")
	if c.Env != "development" {
		need(c.SessionKey != "", "FAPPS_SESSION_KEY")
	}
	if c.Keys.DemoKeys && c.Env != "demo" && c.Env != "development" {
		missing = append(missing, "FAPPS_DEMO_KEYS=1 is refused outside APP_ENV=demo|development")
	}
	if !c.Keys.DemoKeys {
		need(c.Keys.IndexKey != "" && c.Keys.IndexKeyID != "", "FAPPS_INDEX_KEY + FAPPS_INDEX_KEY_ID")
		need(c.Keys.ArtifactKey != "" && c.Keys.ArtifactKeyID != "", "FAPPS_ARTIFACT_KEY + FAPPS_ARTIFACT_KEY_ID")
	}
	need(len(c.Validators) > 0, "FAPPS_VALIDATORS")
	return missing
}

// ParseDataKey reads FAPPS_DATA_KEY: empty (not set), or at least 32 bytes
// in base64 (standard or URL alphabet, padded or not).
func ParseDataKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) < 32 {
				return nil, fmt.Errorf("FAPPS_DATA_KEY is %d bytes, at least 32 are needed", len(b))
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("FAPPS_DATA_KEY is not base64")
}

func env(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func intEnv(name string, def int64) (int64, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: %q is not a positive integer", name, v)
	}
	return n, nil
}

// baseURL reads an http(s) origin (optionally with a path), without a
// trailing slash.
func baseURL(name, def string) (string, error) {
	v := strings.TrimRight(env(name, def), "/")
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%s: %q is not an http(s) URL", name, v)
	}
	return v, nil
}

// hostRe is a DNS name in lower case: labels of letters, digits and inner
// hyphens. No IP literal, no wildcard: the value goes into a CSP source list
// verbatim, where '*', ';', ',', quotes and spaces would change its meaning.
var hostRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

// httpsOrigin reads an optional https origin, "https://host[:port]" (a
// trailing slash is dropped), lower-cased. Unset or empty is "".
func httpsOrigin(name string) (string, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return "", nil
	}
	bad := fmt.Errorf("%s: %q is not an https origin (https://host[:port], nothing else)", name, v)
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || (u.Path != "" && u.Path != "/") ||
		u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.HasSuffix(v, "#") {
		return "", bad
	}
	host := strings.ToLower(u.Hostname())
	if _, ipErr := netip.ParseAddr(host); ipErr == nil || !hostRe.MatchString(host) {
		return "", bad
	}
	origin := "https://" + host
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return "", bad
		}
		origin += ":" + p
	} else if strings.HasSuffix(u.Host, ":") {
		return "", bad
	}
	return origin, nil
}

// loginRe is a GitHub login: letters, digits and hyphens, at most 39.
var loginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// members reads FAPPS_ADMINS or FAPPS_REVIEWERS: a comma list of GitHub
// accounts, each its numeric user id (`12345`) or a login and the id
// (`alice:12345`). A login alone is refused: logins can be released and
// registered again by someone else, so only the id names an account.
func members(name string) ([]Member, error) {
	var out []Member
	seen := map[int64]bool{}
	for _, part := range splitList(os.Getenv(name)) {
		label, idText, hasLabel := strings.Cut(part, ":")
		if !hasLabel {
			label, idText = "", part
		}
		label, idText = strings.TrimSpace(label), strings.TrimSpace(idText)
		id, err := strconv.ParseInt(idText, 10, 64)
		switch {
		case !hasLabel && err != nil && loginRe.MatchString(part):
			return nil, fmt.Errorf("%s: %q is a GitHub login; write the account's numeric user id, as 12345 or %s:12345 "+
				"(a login can be given up and registered again by someone else, the id cannot; "+
				"https://api.github.com/users/%s answers it as \"id\")", name, part, part, part)
		case err != nil || id <= 0 || strconv.FormatInt(id, 10) != idText || (hasLabel && !loginRe.MatchString(label)):
			return nil, fmt.Errorf("%s: %q is not <GitHub user id> or <login>:<GitHub user id> (e.g. 12345 or alice:12345)", name, part)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Member{ID: id, Login: label})
	}
	return out, nil
}

// loadBrand reads the store's name, logo and operator settings.
func loadBrand(publicURL string) (Brand, error) {
	var b Brand
	var err error
	if b.Name, err = plainText("FAPPS_STORE_NAME", MaxStoreNameRunes); err != nil {
		return b, err
	}
	if b.Name == "" {
		b.Name = FallbackStoreName
		if u, perr := url.Parse(publicURL); perr == nil && u.Host != "" {
			b.Name = strings.ToLower(u.Host)
		}
	}
	if b.OperatorName, err = plainText("FAPPS_OPERATOR_NAME", MaxOperatorNameRunes); err != nil {
		return b, err
	}
	for _, s := range []struct {
		name string
		dst  *string
	}{
		{"FAPPS_OPERATOR_URL", &b.OperatorURL},
		{"FAPPS_TERMS_URL", &b.TermsURL},
		{"FAPPS_PRIVACY_URL", &b.PrivacyURL},
	} {
		if *s.dst, err = httpsURL(s.name, os.Getenv(s.name)); err != nil {
			return b, err
		}
	}
	if b.Contact, err = contact("FAPPS_CONTACT"); err != nil {
		return b, err
	}
	if path := strings.TrimSpace(os.Getenv("FAPPS_STORE_LOGO")); path != "" {
		if b.Logo, b.LogoType, err = readLogo(path); err != nil {
			return b, fmt.Errorf("FAPPS_STORE_LOGO: %w", err)
		}
	}
	return b, nil
}

// plainText reads a short one-line text (a name): valid UTF-8, no control
// character and no bidirectional control (which could make a name read as
// another), at most max characters. Unset or blank is "".
func plainText(name string, max int) (string, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return "", nil
	}
	if !utf8.ValidString(v) || strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) }) >= 0 {
		return "", fmt.Errorf("%s: %q is not one line of text", name, v)
	}
	if n := utf8.RuneCountInString(v); n > max {
		return "", fmt.Errorf("%s is %d characters, at most %d", name, n, max)
	}
	return v, nil
}

// httpsURL reads an optional https address shown as a link (an operator's
// page, terms, privacy): https, a DNS host (no IP literal, no user info), an
// optional port, any path, query and fragment, no space or control
// character. Unset or blank is "".
func httpsURL(name, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", nil
	}
	bad := fmt.Errorf("%s: %q is not an https address (https://host/path)", name, v)
	if len(v) > 2048 || strings.IndexFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 ||
		strings.ContainsAny(v, `"<>\^`+"`{|}") {
		return "", bad
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" {
		return "", bad
	}
	host := strings.ToLower(u.Hostname())
	if _, ipErr := netip.ParseAddr(host); ipErr == nil || !hostRe.MatchString(host) {
		return "", bad
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != p {
			return "", bad
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", bad
	}
	return v, nil
}

// contact reads FAPPS_CONTACT: a `mailto:` address (one plain address, no
// display name, no query) or an https address. Unset or blank is "".
func contact(name string) (string, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return "", nil
	}
	if !strings.HasPrefix(strings.ToLower(v), "mailto:") {
		if _, err := httpsURL(name, v); err != nil {
			return "", fmt.Errorf("%s: %q is neither mailto:<address> nor an https address", name, v)
		}
		return v, nil
	}
	addr := v[len("mailto:"):]
	parsed, err := mail.ParseAddress(addr)
	if err != nil || parsed.Name != "" || parsed.Address != addr || strings.ContainsAny(addr, "?&%<> ") {
		return "", fmt.Errorf("%s: %q is neither mailto:<address> nor an https address", name, v)
	}
	return "mailto:" + addr, nil
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// readLogo reads the store's logo: a regular file of at most MaxLogoBytes,
// a PNG (by its signature) or an SVG (UTF-8 text whose first element is
// <svg>, after an optional XML declaration, comments and a doctype).
func readLogo(path string) ([]byte, string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if !st.Mode().IsRegular() {
		return nil, "", fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > MaxLogoBytes {
		return nil, "", fmt.Errorf("%s is %d bytes, at most %d", path, st.Size(), MaxLogoBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if bytes.HasPrefix(b, pngMagic) {
		return b, "image/png", nil
	}
	if isSVG(b) {
		return b, "image/svg+xml", nil
	}
	return nil, "", fmt.Errorf("%s is neither a PNG nor an SVG image", path)
}

// isSVG: UTF-8 text whose first element is <svg ...>.
func isSVG(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	s := strings.TrimPrefix(string(b), "\ufeff")
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "<?"):
			end := strings.Index(s, "?>")
			if end < 0 {
				return false
			}
			s = s[end+2:]
		case strings.HasPrefix(s, "<!--"):
			end := strings.Index(s, "-->")
			if end < 0 {
				return false
			}
			s = s[end+3:]
		case strings.HasPrefix(s, "<!DOCTYPE") || strings.HasPrefix(s, "<!doctype"):
			end := strings.Index(s, ">")
			if end < 0 {
				return false
			}
			s = s[end+1:]
		default:
			return strings.HasPrefix(s, "<svg") && len(s) > 4 && strings.ContainsRune(" \t\r\n>/", rune(s[4]))
		}
	}
}

func days(name string, def int64) (time.Duration, error) {
	n, err := intEnv(name, def)
	return time.Duration(n) * 24 * time.Hour, err
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
