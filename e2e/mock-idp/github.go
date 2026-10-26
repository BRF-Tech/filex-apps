package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gitHub is the GitHub side: OAuth (authorize page + token exchange), the
// REST endpoints the store reads, raw files and release downloads. The same
// value serves the HTTP mock (links under MOCK_PUBLIC_URL) and the TLS
// sidecars (links on GitHub's own host names).
type gitHub struct {
	cfg *Config
	fx  *Fixtures

	// links: where html_url, download_url and browser_download_url point.
	webBase    string // https://github.com or <public>/github
	rawBase    string // https://raw.githubusercontent.com or <public>/raw
	assetsBase string // https://release-assets.githubusercontent.com or <public>/release-assets
	apiBase    string // https://api.github.com or <public>/github-api

	mu    sync.Mutex
	codes map[string]ghCode
}

type ghCode struct {
	login    string
	redirect string
	expires  time.Time
}

func newGitHub(cfg *Config, fx *Fixtures, hostMode bool) *gitHub {
	g := &gitHub{cfg: cfg, fx: fx, codes: map[string]ghCode{}}
	if hostMode {
		g.webBase, g.rawBase = "https://github.com", "https://raw.githubusercontent.com"
		g.assetsBase, g.apiBase = "https://release-assets.githubusercontent.com", "https://api.github.com"
	} else {
		p := cfg.PublicURL
		g.webBase, g.rawBase, g.assetsBase, g.apiBase = p+"/github", p+"/raw", p+"/release-assets", p+"/github-api"
	}
	return g
}

// ---------------------------------------------------------------- OAuth

var ghAuthorizePage = template.Must(template.New("gh").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Sign in to GitHub - e2e mock</title></head>
<body>
<h1>Sign in to GitHub</h1>
<p data-testid="mock-github-client">Application <code>{{.ClientID}}</code> wants to know who you are. (End-to-end test mock: nothing here is GitHub.)</p>
<form method="post" action="">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="scope" value="{{.Scope}}">
{{range .Users}}<p><button type="submit" name="login" value="{{.Login}}" data-testid="mock-github-user-{{.Login}}">Sign in as {{.Login}}</button> {{.Name}}</p>
{{end}}<p><button type="submit" name="deny" value="1" data-testid="mock-github-deny">Cancel</button></p>
</form>
</body></html>`))

func (g *gitHub) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	clientID, redirect, state := r.Form.Get("client_id"), r.Form.Get("redirect_uri"), r.Form.Get("state")
	if clientID != g.cfg.GitHubClientID {
		http.Error(w, "The client_id is not known to this mock GitHub.", http.StatusNotFound)
		return
	}
	if redirect == "" && len(g.cfg.GitHubRedirects) > 0 {
		redirect = g.cfg.GitHubRedirects[0]
	}
	if len(g.cfg.GitHubRedirects) > 0 && !slices.Contains(g.cfg.GitHubRedirects, redirect) {
		http.Error(w, "The redirect_uri MUST match the registered callback URL for this application.", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = ghAuthorizePage.Execute(w, map[string]any{
			"ClientID": clientID, "RedirectURI": redirect, "State": state, "Scope": r.Form.Get("scope"),
			"Users": g.fx.GitHubUsers,
		})
		return
	}
	target, err := url.Parse(redirect)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	q := target.Query()
	if r.PostForm.Get("deny") != "" {
		q.Set("error", "access_denied")
		q.Set("error_description", "The user has denied your application access.")
	} else {
		login := r.PostForm.Get("login")
		if g.fx.githubUser(login) == nil {
			http.Error(w, "unknown user", http.StatusBadRequest)
			return
		}
		code := randomHex(10)
		g.mu.Lock()
		g.codes[code] = ghCode{login: login, redirect: redirect, expires: time.Now().Add(10 * time.Minute)}
		g.mu.Unlock()
		q.Set("code", code)
	}
	if state != "" {
		q.Set("state", state)
	}
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// accessToken answers like GitHub: 200 even for errors, JSON when asked,
// form encoding otherwise.
func (g *gitHub) accessToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
	}
	reply := func(v map[string]string) {
		if strings.Contains(r.Header.Get("Accept"), "json") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(v)
			return
		}
		f := url.Values{}
		for k, x := range v {
			f.Set(k, x)
		}
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
		_, _ = w.Write([]byte(f.Encode()))
	}
	if id != g.cfg.GitHubClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(g.cfg.GitHubClientSecret)) != 1 {
		reply(map[string]string{"error": "incorrect_client_credentials", "error_description": "The client_id and/or client_secret passed are incorrect."})
		return
	}
	code := r.Form.Get("code")
	g.mu.Lock()
	c, found := g.codes[code]
	delete(g.codes, code)
	g.mu.Unlock()
	if !found || time.Now().After(c.expires) {
		reply(map[string]string{"error": "bad_verification_code", "error_description": "The code passed is incorrect or expired."})
		return
	}
	if ru := r.Form.Get("redirect_uri"); ru != "" && ru != c.redirect {
		reply(map[string]string{"error": "redirect_uri_mismatch", "error_description": "The redirect_uri MUST match the registered callback URL for this application."})
		return
	}
	reply(map[string]string{"access_token": mintToken(g.cfg.TokenKey, c.login), "token_type": "bearer", "scope": ""})
}

// Access tokens carry their login and an HMAC under MOCK_TOKEN_KEY, so one
// process issues them (the OAuth exchange, mock-idp) and another accepts them
// (api.github.com in the app's github-tls sidecar): the stack shares the key,
// not a table.
func mintToken(key []byte, login string) string {
	nonce := randomHex(6)
	return "gho_" + hex.EncodeToString([]byte(login)) + "_" + nonce + "_" + tokenMAC(key, login, nonce)
}

func tokenMAC(key []byte, login, nonce string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(login + "\x00" + nonce))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// caller returns the signed-in login for the request's token: "" without a
// token, ok=false for a token this stack never issued.
func (g *gitHub) caller(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", true
	}
	tok := h
	for _, p := range []string{"Bearer ", "bearer ", "token ", "Token "} {
		tok = strings.TrimPrefix(tok, p)
	}
	parts := strings.Split(tok, "_")
	if len(parts) != 4 || parts[0] != "gho" {
		return "", false
	}
	raw, err := hex.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	login := string(raw)
	if !hmac.Equal([]byte(tokenMAC(g.cfg.TokenKey, login, parts[2])), []byte(parts[3])) || g.fx.githubUser(login) == nil {
		return "", false
	}
	return login, true
}

// ---------------------------------------------------------------- REST API

func ghNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found", "documentation_url": "https://docs.github.com/rest", "status": "404"})
}

// api serves path (no prefix): /user, /users/{login}, /repos/{o}/{r}/...,
// /repositories/{id}. The store's admin-repository token is answered by
// serverAPI.
func (g *gitHub) api(w http.ResponseWriter, r *http.Request, path string) {
	if g.isServerToken(r) {
		g.serverAPI(w, path)
		return
	}
	login, ok := g.caller(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Bad credentials", "documentation_url": "https://docs.github.com/rest", "status": "401"})
		return
	}
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4999")
	seg := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(seg) == 1 && seg[0] == "user":
		if login == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Requires authentication", "status": "401"})
			return
		}
		u := g.fx.githubUser(login)
		writeJSON(w, http.StatusOK, g.userJSON(u, true))
	case len(seg) == 2 && seg[0] == "user" && seg[1] == "emails":
		if login == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Requires authentication", "status": "401"})
			return
		}
		u := g.fx.githubUser(login)
		writeJSON(w, http.StatusOK, []any{map[string]any{"email": u.Email, "primary": true, "verified": true, "visibility": "public"}})
	case len(seg) == 2 && seg[0] == "users":
		u := g.fx.githubUser(seg[1])
		if u == nil {
			ghNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, g.userJSON(u, false))
	case len(seg) == 2 && seg[0] == "repositories":
		var repo *Repo
		for _, x := range g.fx.byID {
			if seg[1] == strconv.FormatInt(x.ID, 10) {
				repo = x
			}
		}
		if repo == nil || (repo.Private && repo.Roles[login] == "") {
			ghNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, g.repoJSON(repo, login))
	case len(seg) >= 3 && seg[0] == "repos":
		repo := g.fx.repo(seg[1], seg[2])
		if repo == nil || (repo.Private && repo.Roles[login] == "") {
			ghNotFound(w)
			return
		}
		g.repoAPI(w, r, repo, login, seg[3:])
	default:
		ghNotFound(w)
	}
}

func (g *gitHub) repoAPI(w http.ResponseWriter, r *http.Request, repo *Repo, login string, rest []string) {
	switch {
	case len(rest) == 0:
		writeJSON(w, http.StatusOK, g.repoJSON(repo, login))
	case len(rest) == 1 && rest[0] == "tags":
		var out []any
		for i := len(repo.TagList) - 1; i >= 0; i-- {
			t := repo.TagList[i]
			out = append(out, map[string]any{"name": t.Name, "commit": map[string]any{"sha": t.Commit}})
		}
		writeJSON(w, http.StatusOK, out)
	case len(rest) >= 4 && rest[0] == "git" && (rest[1] == "ref" || rest[1] == "refs") && rest[2] == "tags":
		t := repo.ByName[strings.Join(rest[3:], "/")]
		if t == nil {
			ghNotFound(w)
			return
		}
		obj := map[string]any{"sha": t.Commit, "type": "commit"}
		if t.TagObject != "" {
			obj = map[string]any{"sha": t.TagObject, "type": "tag"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"ref": "refs/tags/" + t.Name, "object": obj})
	case len(rest) >= 4 && rest[0] == "git" && (rest[1] == "ref" || rest[1] == "refs") && rest[2] == "heads":
		if strings.Join(rest[3:], "/") != "main" || len(repo.TagList) == 0 {
			ghNotFound(w)
			return
		}
		t := repo.TagList[len(repo.TagList)-1]
		writeJSON(w, http.StatusOK, map[string]any{"ref": "refs/heads/main", "object": map[string]any{"sha": t.Commit, "type": "commit"}})
	case len(rest) == 3 && rest[0] == "git" && rest[1] == "tags":
		for _, t := range repo.TagList {
			if t.TagObject == strings.ToLower(rest[2]) {
				writeJSON(w, http.StatusOK, map[string]any{
					"sha": t.TagObject, "tag": t.Name, "message": t.Name + "\n",
					"object":       map[string]any{"sha": t.Commit, "type": "commit"},
					"tagger":       map[string]any{"name": repo.Owner, "email": repo.Owner + "@e2e.test", "date": fixedTime.Format(time.RFC3339)},
					"verification": map[string]any{"verified": false, "reason": "unsigned", "signature": nil, "payload": nil, "verified_at": nil},
				})
				return
			}
		}
		ghNotFound(w)
	case len(rest) >= 2 && rest[0] == "commits":
		t := repo.resolve(strings.Join(rest[1:], "/"))
		if t == nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "No commit found for SHA: " + strings.Join(rest[1:], "/"), "status": "422"})
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "vnd.github.sha") {
			w.Header().Set("Content-Type", "application/vnd.github.sha; charset=utf-8")
			_, _ = w.Write([]byte(t.Commit))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sha": t.Commit, "html_url": g.webBase + "/" + repo.FullName + "/commit/" + t.Commit,
			"commit": map[string]any{"message": "Release " + t.Name, "tree": map[string]any{"sha": fakeSHA("tree", repo.FullName, t.Name)}},
		})
	case len(rest) >= 1 && rest[0] == "contents":
		ref := r.URL.Query().Get("ref")
		t := repo.resolve(ref)
		p := strings.Join(rest[1:], "/")
		if t == nil {
			ghNotFound(w)
			return
		}
		b, ok := t.Files[p]
		if !ok {
			ghNotFound(w)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "raw") {
			w.Header().Set("Content-Type", "application/vnd.github.raw; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"type": "file", "encoding": "base64", "size": len(b), "name": p[strings.LastIndex(p, "/")+1:], "path": p,
			"sha": blobSHA(b), "content": wrapBase64(base64.StdEncoding.EncodeToString(b)),
			"download_url": g.rawBase + "/" + repo.FullName + "/" + t.Commit + "/" + p,
			"html_url":     g.webBase + "/" + repo.FullName + "/blob/" + t.Commit + "/" + p,
		})
	case len(rest) == 1 && rest[0] == "license":
		if repo.License == "" {
			ghNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": "LICENSE", "path": "LICENSE", "license": licenseJSON(repo.License)})
	case len(rest) == 3 && rest[0] == "releases" && rest[1] == "tags":
		t := repo.ByName[rest[2]]
		if t == nil || len(t.AssetList) == 0 {
			ghNotFound(w)
			return
		}
		var assets []any
		for i, n := range t.AssetList {
			assets = append(assets, map[string]any{
				"id": repo.ID*1000 + int64(i), "name": n, "size": len(t.Assets[n]), "state": "uploaded",
				"content_type":         "application/octet-stream",
				"browser_download_url": g.webBase + "/" + repo.FullName + "/releases/download/" + t.Name + "/" + n,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tag_name": t.Name, "name": t.Name, "draft": false, "prerelease": false,
			"body": "Release " + t.Name + " (e2e fixture).", "assets": assets,
			"html_url": g.webBase + "/" + repo.FullName + "/releases/tag/" + t.Name,
		})
	default:
		ghNotFound(w)
	}
}

// isServerToken: the request carries MOCK_GITHUB_ADMIN_TOKEN.
func (g *gitHub) isServerToken(r *http.Request) bool {
	if g.cfg.AdminToken == "" {
		return false
	}
	tok := strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "token ")
	return subtle.ConstantTimeCompare([]byte(tok), []byte(g.cfg.AdminToken)) == 1
}

// serverAPI is what a fine-grained token with Metadata: read on the
// administration repository may do: read that repository, and its
// collaborators' permissions. Every other repository is a 404 for it (the
// token does not see it), every other endpoint a 403, as on github.com.
func (g *gitHub) serverAPI(w http.ResponseWriter, path string) {
	w.Header().Set("X-RateLimit-Limit", "5000")
	w.Header().Set("X-RateLimit-Remaining", "4999")
	seg := strings.Split(strings.Trim(path, "/"), "/")
	if len(seg) < 3 || seg[0] != "repos" {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Resource not accessible by personal access token", "status": "403"})
		return
	}
	repo := g.fx.repo(seg[1], seg[2])
	if repo == nil || !repo.ServerToken {
		ghNotFound(w)
		return
	}
	switch {
	case len(seg) == 3:
		writeJSON(w, http.StatusOK, g.repoJSON(repo, ""))
	case len(seg) == 6 && seg[3] == "collaborators" && seg[5] == "permission":
		u := g.fx.githubUser(seg[4])
		if u == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": seg[4] + " is not a user", "status": "404"})
			return
		}
		writeJSON(w, http.StatusOK, g.permissionJSON(repo, u))
	default:
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Resource not accessible by personal access token", "status": "403"})
	}
}

// legacyRole is GitHub's base role for a role: maintain answers write,
// triage answers read, no role none.
var legacyRole = map[string]string{"admin": "admin", "maintain": "write", "write": "write", "triage": "read", "read": "read", "": "none"}

// permissionJSON answers "Get repository permissions for a user" in the
// shape github.com answered on 2026-10-04 (backend auth/github.Permission):
// permission (legacy), role_name ("" for no role) and the user with its
// permission flags and role_name.
func (g *gitHub) permissionJSON(repo *Repo, u *GitHubUser) map[string]any {
	role := ""
	for login, r := range repo.Roles {
		if strings.EqualFold(login, u.Login) {
			role = r
		}
	}
	user := g.userJSON(u, false)
	user["permissions"] = roleFlags(role)
	user["role_name"] = role
	return map[string]any{"permission": legacyRole[role], "role_name": role, "user": user}
}

// roleFlags is what a role may do on a repository.
func roleFlags(role string) map[string]bool {
	return map[string]bool{
		"admin":    role == "admin",
		"maintain": role == "admin" || role == "maintain",
		"push":     role == "admin" || role == "maintain" || role == "write",
		"triage":   role == "admin" || role == "maintain" || role == "write" || role == "triage",
		"pull":     role != "",
	}
}

func (g *gitHub) userJSON(u *GitHubUser, private bool) map[string]any {
	m := map[string]any{
		"login": u.Login, "id": u.ID, "type": "User", "name": u.Name,
		"html_url":   g.webBase + "/" + u.Login,
		"avatar_url": g.webBase + "/" + u.Login + ".png",
	}
	if private {
		m["email"] = u.Email
	}
	return m
}

func (g *gitHub) repoJSON(repo *Repo, login string) map[string]any {
	vis := "public"
	if repo.Private {
		vis = "private"
	}
	m := map[string]any{
		"id": repo.ID, "name": repo.Repo, "full_name": repo.FullName, "private": repo.Private,
		"visibility": vis, "archived": repo.Archived, "disabled": false, "fork": false,
		"default_branch": "main",
		"owner":          map[string]any{"login": repo.Owner, "id": repo.OwnerID, "type": repo.OwnerType, "html_url": g.webBase + "/" + repo.Owner},
		"html_url":       g.webBase + "/" + repo.FullName,
		"url":            g.apiBase + "/repos/" + repo.FullName,
		"license":        nil,
	}
	if repo.License != "" {
		m["license"] = licenseJSON(repo.License)
	}
	// GitHub returns permissions only to an authenticated caller.
	if login != "" {
		f := roleFlags(repo.Roles[login])
		f["pull"] = true
		m["permissions"] = f
	}
	return m
}

func licenseJSON(spdx string) map[string]any {
	return map[string]any{"key": strings.ToLower(spdx), "spdx_id": spdx, "name": spdx + " License"}
}

// ---------------------------------------------------------------- raw + releases

// raw serves /{owner}/{repo}/{ref}/{path}.
func (g *gitHub) raw(w http.ResponseWriter, path string) {
	seg := strings.SplitN(strings.Trim(path, "/"), "/", 4)
	if len(seg) < 4 {
		http.Error(w, "404: Not Found", http.StatusNotFound)
		return
	}
	repo := g.fx.repo(seg[0], seg[1])
	if repo == nil || repo.Private {
		http.Error(w, "404: Not Found", http.StatusNotFound)
		return
	}
	ref, _ := url.PathUnescape(seg[2])
	t := repo.resolve(ref)
	if t == nil {
		http.Error(w, "404: Not Found", http.StatusNotFound)
		return
	}
	b, ok := t.Files[seg[3]]
	if !ok {
		http.Error(w, "404: Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}

// releaseDownload answers github.com/{o}/{r}/releases/download/{tag}/{file}
// with a redirect to the asset host, like GitHub does.
func (g *gitHub) releaseDownload(w http.ResponseWriter, r *http.Request, path string) bool {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	if len(seg) != 6 || seg[2] != "releases" || seg[3] != "download" {
		return false
	}
	repo := g.fx.repo(seg[0], seg[1])
	if repo == nil || repo.ByName[seg[4]] == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return true
	}
	if _, ok := repo.ByName[seg[4]].Assets[seg[5]]; !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return true
	}
	target := g.assetsBase + "/" + repo.FullName + "/" + seg[4] + "/" + seg[5] + "?sig=e2e"
	http.Redirect(w, r, target, http.StatusFound)
	return true
}

// asset serves {o}/{r}/{tag}/{file} on the asset host.
func (g *gitHub) asset(w http.ResponseWriter, path string) {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	if len(seg) != 4 {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	repo := g.fx.repo(seg[0], seg[1])
	if repo == nil || repo.ByName[seg[2]] == nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	b, ok := repo.ByName[seg[2]].Assets[seg[3]]
	if !ok {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if g.cfg.CorruptAssets {
		b = append(append([]byte{}, b...), '\n')
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(b)
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// wrapBase64 breaks base64 into 60-character lines, as GitHub does.
func wrapBase64(s string) string {
	var b strings.Builder
	for len(s) > 60 {
		b.WriteString(s[:60])
		b.WriteByte('\n')
		s = s[60:]
	}
	b.WriteString(s)
	b.WriteByte('\n')
	return b.String()
}
