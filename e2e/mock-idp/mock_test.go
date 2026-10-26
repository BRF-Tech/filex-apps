package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const fixtureDir = "../fixtures"

func testConfig(public string) *Config {
	return &Config{
		PublicURL: public, Fixtures: fixtureDir, TokenKey: []byte("e2e-test-key"),
		GitHubClientID: "gh-id", GitHubClientSecret: "gh-secret",
		GitHubRedirects: []string{"https://fapps.e2e.test/auth/github/callback"},
	}
}

// Every fixture loads, every placeholder is filled, every manifest pins the
// bundle it ships with, and the bytes are the same on every load.
func TestFixturesResolveAndAreStable(t *testing.T) {
	a, err := loadFixtures(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := loadFixtures(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.repos) == 0 {
		t.Fatal("no repositories in fixtures")
	}
	for key, r := range a.repos {
		for _, tag := range r.TagList {
			for name, body := range tag.Files {
				if bytes.Contains(body, []byte("${")) && strings.HasSuffix(name, ".json") {
					t.Errorf("%s@%s %s: unresolved placeholder: %s", key, tag.Name, name, body)
				}
				if strings.HasSuffix(name, ".json") && !json.Valid(body) {
					t.Errorf("%s@%s %s: not valid JSON", key, tag.Name, name)
				}
				if !bytes.Equal(body, b.repos[key].ByName[tag.Name].Files[name]) {
					t.Errorf("%s@%s %s: bytes differ between loads", key, tag.Name, name)
				}
			}
			for name, body := range tag.Assets {
				if !bytes.Equal(body, b.repos[key].ByName[tag.Name].Assets[name]) {
					t.Errorf("%s@%s asset %s: bytes differ between loads", key, tag.Name, name)
				}
			}
			var m struct {
				UI *struct {
					Bundle struct{ SHA256 string } `json:"bundle"`
				} `json:"ui"`
			}
			if err := json.Unmarshal(tag.Files["filex-app.json"], &m); err != nil {
				t.Fatalf("%s@%s: %v", key, tag.Name, err)
			}
			if m.UI != nil {
				sum := sha256.Sum256(tag.Assets["ui.zip"])
				if m.UI.Bundle.SHA256 != hex.EncodeToString(sum[:]) {
					t.Errorf("%s@%s: manifest pins %s, ui.zip is %x", key, tag.Name, m.UI.Bundle.SHA256, sum)
				}
				zr, err := zip.NewReader(bytes.NewReader(tag.Assets["ui.zip"]), int64(len(tag.Assets["ui.zip"])))
				if err != nil || len(zr.File) == 0 {
					t.Errorf("%s@%s: ui.zip unreadable: %v", key, tag.Name, err)
				}
			}
		}
	}
}

func TestGitHubOAuthAndPermissions(t *testing.T) {
	fx, err := loadFixtures(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	mux := http.NewServeMux()
	srv = httptest.NewServer(mux)
	defer srv.Close()
	cfg := testConfig(srv.URL)
	gh := newGitHub(cfg, fx, false)
	mux.HandleFunc("/github/login/oauth/authorize", gh.authorize)
	mux.HandleFunc("/github/login/oauth/access_token", gh.accessToken)
	mux.HandleFunc("/github-api/", func(w http.ResponseWriter, r *http.Request) {
		gh.api(w, r, strings.TrimPrefix(r.URL.Path, "/github-api"))
	})

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"client_id": {"gh-id"}, "redirect_uri": {cfg.GitHubRedirects[0]}, "state": {"s1"}, "login": {"e2e-pub-chromium"}}
	resp, err := noFollow.PostForm(srv.URL+"/github/login/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || loc.Query().Get("state") != "s1" || loc.Query().Get("code") == "" {
		t.Fatalf("authorize: %d %s", resp.StatusCode, loc)
	}

	exchange := func(secret, code string) map[string]string {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/github/login/oauth/access_token",
			strings.NewReader(url.Values{"client_id": {"gh-id"}, "client_secret": {secret}, "code": {code}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var out map[string]string
		_ = json.NewDecoder(r.Body).Decode(&out)
		return out
	}
	if got := exchange("wrong", loc.Query().Get("code")); got["error"] != "incorrect_client_credentials" {
		t.Fatalf("wrong secret accepted: %v", got)
	}
	tok := exchange("gh-secret", loc.Query().Get("code"))["access_token"]
	if !strings.HasPrefix(tok, "gho_") {
		t.Fatalf("no token")
	}
	if again := exchange("gh-secret", loc.Query().Get("code")); again["error"] != "bad_verification_code" {
		t.Fatalf("a code was accepted twice: %v", again)
	}

	get := func(path, token string) (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/github-api"+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(r.Body).Decode(&out)
		return r.StatusCode, out
	}
	if code, u := get("/user", tok); code != 200 || u["login"] != "e2e-pub-chromium" || u["id"].(float64) != 910101 {
		t.Fatalf("/user: %d %v", code, u)
	}
	_, repo := get("/repos/e2e-pub-chromium/hello", tok)
	perms, _ := repo["permissions"].(map[string]any)
	if perms["admin"] != true || repo["id"].(float64) != 920101 {
		t.Fatalf("repo as owner: %v", repo)
	}
	if _, anon := get("/repos/e2e-pub-chromium/hello", ""); anon["permissions"] != nil {
		t.Fatalf("permissions shown to an anonymous caller: %v", anon)
	}
	if code, _ := get("/user", "gho_forged"); code != http.StatusUnauthorized {
		t.Fatalf("forged token: %d", code)
	}
	// Another process of the stack (the app's api.github.com sidecar) with the
	// same key accepts the token; a different key, or a token whose login was
	// swapped, is refused.
	same := newGitHub(cfg, fx, true)
	other := newGitHub(&Config{TokenKey: []byte("another-key")}, fx, true)
	ask := func(g *gitHub, token string) (string, bool) {
		req := httptest.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
		req.Header.Set("Authorization", "token "+token)
		return g.caller(req)
	}
	if login, ok := ask(same, tok); !ok || login != "e2e-pub-chromium" {
		t.Fatalf("sidecar with the same key: %q %v", login, ok)
	}
	if _, ok := ask(other, tok); ok {
		t.Fatal("a different key accepted the token")
	}
	parts := strings.Split(tok, "_")
	swapped := "gho_" + hex.EncodeToString([]byte("e2e-outsider")) + "_" + parts[2] + "_" + parts[3]
	if _, ok := ask(same, swapped); ok {
		t.Fatal("a token with a swapped login was accepted")
	}
	_, ref := get("/repos/e2e-pub-chromium/hello/git/ref/tags/v1.0.0", "")
	obj := ref["object"].(map[string]any)
	if obj["type"] != "tag" {
		t.Fatalf("annotated tag: %v", ref)
	}
	_, tagObj := get("/repos/e2e-pub-chromium/hello/git/tags/"+obj["sha"].(string), "")
	if tagObj["object"].(map[string]any)["sha"] != fakeSHA("commit", "e2e-pub-chromium/hello", "v1.0.0") {
		t.Fatalf("peeled tag: %v", tagObj)
	}
	_, light := get("/repos/e2e-pub-chromium/lang-e2e/git/ref/tags/v0.1.0", "")
	if light["object"].(map[string]any)["type"] != "commit" {
		t.Fatalf("lightweight tag: %v", light)
	}
	_, contents := get("/repos/e2e-pub-chromium/hello/contents/.filex-apps.json?ref=v1.0.0", "")
	raw, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(contents["content"].(string), "\n", ""))
	if !bytes.Contains(raw, []byte(`"publisher": "e2e-chromium"`)) {
		t.Fatalf("contents: %s", raw)
	}
}

// The store's admin-repository token (MOCK_GITHUB_ADMIN_TOKEN) sees the
// private administration repository and reads its collaborators'
// permissions in github.com's shape; it sees no other repository, and a
// person who is no collaborator does not see the administration repository.
func TestServerTokenReadsTheAdministrationRepository(t *testing.T) {
	fx, err := loadFixtures(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig("https://mock-idp:9443")
	cfg.AdminToken = "github_pat_e2e_test"
	g := newGitHub(cfg, fx, true)
	get := func(path, token string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, "https://api.github.com"+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		g.api(rec, req, path)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	const admin = "/repos/e2e-org/store-admins"
	if code, repo := get(admin, cfg.AdminToken); code != 200 || repo["private"] != true {
		t.Fatalf("administration repository with the server token: %d %v", code, repo)
	}
	for login, want := range map[string][2]string{
		"e2e-admin": {"write", "write"}, "e2e-reviewer": {"read", "triage"}, "e2e-nobody": {"read", "read"},
		"e2e-outsider": {"none", ""}, "e2e-pub-chromium": {"none", ""},
	} {
		code, p := get(admin+"/collaborators/"+login+"/permission", cfg.AdminToken)
		user, _ := p["user"].(map[string]any)
		if code != 200 || p["permission"] != want[0] || p["role_name"] != want[1] || user["login"] != login || user["role_name"] != want[1] {
			t.Errorf("%s: %d %v, want permission %q role_name %q", login, code, p, want[0], want[1])
		}
	}
	if code, p := get(admin+"/collaborators/no-such-account/permission", cfg.AdminToken); code != 404 || p["message"] != "no-such-account is not a user" {
		t.Fatalf("unknown login: %d %v", code, p)
	}
	if code, _ := get("/repos/e2e-pub-chromium/hello", cfg.AdminToken); code != 404 {
		t.Fatalf("the server token sees a repository it was not given: %d", code)
	}
	if code, _ := get("/user", cfg.AdminToken); code != 403 {
		t.Fatalf("the server token on /user: %d", code)
	}
	// A person sees a private repository only as its collaborator, as on
	// github.com; the store never relies on that (its users' tokens have
	// no scope), it asks with the server token.
	if code, _ := get(admin, mintToken(cfg.TokenKey, "e2e-outsider")); code != 404 {
		t.Fatalf("a non-collaborator's token sees the administration repository: %d", code)
	}
	if code, _ := get(admin, ""); code != 404 {
		t.Fatalf("anonymous sees the administration repository: %d", code)
	}
}
