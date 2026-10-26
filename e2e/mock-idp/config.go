package main

import (
	"os"
	"strings"
)

// Config is every MOCK_* variable. Defaults fit docker-compose.e2e.yml.
type Config struct {
	Listen    string // MOCK_LISTEN (serve, plain HTTP), default :9000
	TLSListen string // MOCK_TLS_LISTEN (serve, HTTPS with the run's certificate), default :9443
	PublicURL string // MOCK_PUBLIC_URL: how browsers and the app reach `serve`, e.g. https://mock-idp:9443
	Fixtures  string // MOCK_FIXTURES: the fixture directory (users.json, repos.json, templates/)
	CADir     string // MOCK_CA_DIR: where the throw-away CA and server certificate live
	TLSHosts  []string

	GitHubClientID     string   // MOCK_GITHUB_CLIENT_ID
	GitHubClientSecret string   // MOCK_GITHUB_CLIENT_SECRET
	GitHubRedirects    []string // MOCK_GITHUB_REDIRECT_URIS (comma list; exact match)

	// AdminToken stands for the store's FAPPS_ADMIN_TOKEN, a fine-grained
	// token with Metadata: read on the administration repository
	// (MOCK_GITHUB_ADMIN_TOKEN, the run's secret). It sees the repositories
	// marked "server_token" in repos.json and nothing else, and reads their
	// collaborators' permissions. Empty: no such token.
	AdminToken string

	// CorruptAssets appends one byte to every release asset it serves
	// (MOCK_CORRUPT_ASSETS=1): the stack's own red test - every sha256 pin
	// must then refuse the download.
	CorruptAssets bool

	EdgeListen   string // MOCK_EDGE_LISTEN, default :443
	EdgeUpstream string // MOCK_EDGE_UPSTREAM, default http://app:8080
	GHListen     string // MOCK_GH_LISTEN (github-tls), default 127.0.0.1:443

	// TokenKey signs the GitHub access tokens (MOCK_TOKEN_KEY, shared by
	// every mock process of a run; random per process when unset).
	TokenKey []byte
}

// DefaultTLSHosts are the names the server certificate carries: the store's
// own test origin and GitHub's hosts (for github-tls).
var DefaultTLSHosts = []string{
	"fapps.e2e.test",
	"mock-idp",
	"github.com",
	"api.github.com",
	"raw.githubusercontent.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
	"github-releases.githubusercontent.com",
	"codeload.github.com",
	"localhost",
}

func loadConfig() *Config {
	c := &Config{
		Listen:             env("MOCK_LISTEN", ":9000"),
		TLSListen:          env("MOCK_TLS_LISTEN", ":9443"),
		PublicURL:          strings.TrimRight(env("MOCK_PUBLIC_URL", "https://mock-idp:9443"), "/"),
		Fixtures:           env("MOCK_FIXTURES", "/fixtures"),
		CADir:              env("MOCK_CA_DIR", "/e2e-ca"),
		TLSHosts:           DefaultTLSHosts,
		GitHubClientID:     env("MOCK_GITHUB_CLIENT_ID", "e2e-github-client"),
		GitHubClientSecret: env("MOCK_GITHUB_CLIENT_SECRET", "e2e-github-secret"),
		GitHubRedirects:    list(os.Getenv("MOCK_GITHUB_REDIRECT_URIS")),
		AdminToken:         strings.TrimSpace(os.Getenv("MOCK_GITHUB_ADMIN_TOKEN")),
		CorruptAssets:      os.Getenv("MOCK_CORRUPT_ASSETS") == "1",
		EdgeListen:         env("MOCK_EDGE_LISTEN", ":443"),
		EdgeUpstream:       env("MOCK_EDGE_UPSTREAM", "http://app:8080"),
		GHListen:           env("MOCK_GH_LISTEN", "127.0.0.1:443"),
	}
	if v := list(os.Getenv("MOCK_TLS_HOSTS")); len(v) > 0 {
		c.TLSHosts = v
	}
	c.TokenKey = []byte(os.Getenv("MOCK_TOKEN_KEY"))
	if len(c.TokenKey) == 0 {
		c.TokenKey = []byte(randomHex(32))
	}
	return c
}

func env(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func list(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
