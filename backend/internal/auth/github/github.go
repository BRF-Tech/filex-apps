// Package github is the store's only sign-in: a GitHub OAuth App
// (authorization code, no scopes: public profile and public repositories
// are all the store reads with the person's token), the two REST reads made
// with that token - who signed in and who owns a login - and the two the
// role check makes with the server's own token (FAPPS_ADMIN_TOKEN): a
// person's permission on the administration repository, and whether the
// token sees that repository at all (internal/auth/adminrepo). The
// repository reads of a submission (also its access check) are
// internal/github's, through the guarded client.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	ghapi "github.com/brf-tech/filex-apps/backend/internal/github"
)

// Issuer is what identities record as the GitHub issuer.
const Issuer = "https://github.com"

// ErrNotFound is GitHub's 404 (or a private repository the token cannot
// see).
var ErrNotFound = errors.New("not found on GitHub")

// Provider talks to one GitHub (github.com, or the end-to-end mock).
type Provider struct {
	oauth  oauth2.Config
	api    string
	client *http.Client
}

// New builds a provider. oauthURL is GitHub's sign-in host, apiURL its REST
// API, redirectURL this store's callback.
func New(clientID, clientSecret, oauthURL, apiURL, redirectURL string) *Provider {
	return &Provider{
		oauth: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint: oauth2.Endpoint{
				AuthURL:   oauthURL + "/login/oauth/authorize",
				TokenURL:  oauthURL + "/login/oauth/access_token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
		},
		api:    apiURL,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Configured reports whether the OAuth App's id and secret are set.
func (p *Provider) Configured() bool {
	return p != nil && p.oauth.ClientID != "" && p.oauth.ClientSecret != ""
}

// AuthCodeURL is where the browser goes to sign in. No scope is asked.
func (p *Provider) AuthCodeURL(state string) string {
	return p.oauth.AuthCodeURL(state, oauth2.SetAuthURLParam("allow_signup", "false"))
}

// Exchange trades the callback's code for the person's token.
func (p *Provider) Exchange(ctx context.Context, code string) (string, error) {
	tok, err := p.oauth.Exchange(context.WithValue(ctx, oauth2.HTTPClient, p.client), code)
	if err != nil {
		return "", fmt.Errorf("github token exchange: %w", err)
	}
	if tok.AccessToken == "" {
		return "", errors.New("github token exchange: empty token")
	}
	return tok.AccessToken, nil
}

// User is the signed-in GitHub account.
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Me reads the account the token belongs to.
func (p *Provider) Me(ctx context.Context, token string) (User, error) {
	var u User
	if err := p.get(ctx, token, "/user", &u); err != nil {
		return User{}, err
	}
	if u.ID <= 0 || u.Login == "" {
		return User{}, errors.New("github /user: no id or login")
	}
	return u, nil
}

// Owner is a GitHub user or organization.
type Owner struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"` // "User" | "Organization"
}

// Owner looks a login up.
func (p *Provider) Owner(ctx context.Context, token, login string) (Owner, error) {
	var o Owner
	if err := p.get(ctx, token, "/users/"+url.PathEscape(login), &o); err != nil {
		return Owner{}, err
	}
	if o.ID <= 0 || (o.Type != "User" && o.Type != "Organization") {
		return Owner{}, fmt.Errorf("github /users/%s: unexpected answer", login)
	}
	return o, nil
}

// Permission is one account's role on a repository, as GitHub's "Get
// repository permissions for a user" answers it
// (GET /repos/{owner}/{repo}/collaborators/{username}/permission). Measured
// on 2026-10-04 against a private administration repository, with the owner's
// session and with the store's fine-grained token (Metadata: read is
// enough): a collaborator with the admin role gets {"permission":"admin",
// "role_name":"admin","user":{"id":..,"permissions":{"admin":true,...},
// "role_name":"admin"}}; an account that is not a collaborator 200
// {"permission":"none","role_name":"","user":{... all permissions false}};
// a login that is no account 404 "<login> is not a user". A token that does
// not see a private repository answers 404 (also for the repository
// itself); on a public repository outside its grant, 403 "Resource not
// accessible by personal access token" (StatusError.NotAccessible).
type Permission struct {
	// Permission is the legacy base role: admin, write, read or none.
	// GitHub folds maintain into write and triage into read here.
	Permission string `json:"permission"`
	// RoleName is the full role: admin, maintain, write, triage, read, a
	// custom role's name, or "" for none. nil when the answer has no
	// role_name at all.
	RoleName *string `json:"role_name"`
	User     *struct {
		ID          int64      `json:"id"`
		Login       string     `json:"login"`
		Permissions *RepoFlags `json:"permissions"`
	} `json:"user"`
}

// RepoFlags is what a role may do on a repository.
type RepoFlags struct {
	Admin    bool `json:"admin"`
	Maintain bool `json:"maintain"`
	Push     bool `json:"push"`
	Triage   bool `json:"triage"`
	Pull     bool `json:"pull"`
}

// RepoPermission reads login's permission on owner/repo with token (the
// server's token: the repository may be private). ErrNotFound when GitHub
// answers 404: the login is no account, or the token does not see the
// repository (RepoVisible tells the two apart).
func (p *Provider) RepoPermission(ctx context.Context, token, owner, repo, login string) (Permission, error) {
	var perm Permission
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/collaborators/" + url.PathEscape(login) + "/permission"
	if err := p.get(ctx, token, path, &perm); err != nil {
		return Permission{}, err
	}
	return perm, nil
}

// RepoVisible reports whether token sees owner/repo: nil, ErrNotFound (it
// does not, or the repository is gone), or the GitHub error.
func (p *Provider) RepoVisible(ctx context.Context, token, owner, repo string) error {
	var r struct {
		ID int64 `json:"id"`
	}
	if err := p.get(ctx, token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo), &r); err != nil {
		return err
	}
	if r.ID <= 0 {
		return fmt.Errorf("github /repos/%s/%s: no id", owner, repo)
	}
	return nil
}

// get reads one JSON answer (at most 1 MiB).
func (p *Provider) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.api+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "filex-apps-store")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("github %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("github %s: %w", path, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode != http.StatusOK:
		rateLimited := resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" ||
			resp.StatusCode == http.StatusTooManyRequests
		forbidden := resp.StatusCode == http.StatusForbidden && !rateLimited
		return &StatusError{Path: path, Status: resp.StatusCode, RateLimited: rateLimited,
			PolicyRefused: forbidden && ghapi.TokenRefusedByPolicy(body),
			NotAccessible: forbidden && notAccessible(body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("github %s: %w", path, err)
	}
	return nil
}

// StatusError is a GitHub answer other than 200 or 404.
type StatusError struct {
	Path        string
	Status      int
	RateLimited bool
	// PolicyRefused: a 403 saying the owner's policy refuses the token
	// itself (BRF-Tech: a fine-grained token living over 366 days; SAML).
	PolicyRefused bool
	// NotAccessible: a 403 "Resource not accessible by personal access
	// token": the token was not given this repository (measured 2026-10-04
	// on a public repository outside the token's grant; a private one
	// answers 404).
	NotAccessible bool
}

// notAccessible reads GitHub's 403 message for a token without access.
func notAccessible(body []byte) bool {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	return strings.Contains(strings.ToLower(m.Message), "resource not accessible by")
}

func (e *StatusError) Error() string {
	return "github " + e.Path + ": HTTP " + strconv.Itoa(e.Status)
}
