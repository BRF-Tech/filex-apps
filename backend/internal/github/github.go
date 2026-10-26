// Package github reads what the store needs from GitHub - a repository and
// the caller's permission on it, a tag's commit, a file at a commit, a
// release asset's digest - through the guarded client (internal/safehttp).
//
// It never writes and asks for no scope: every call works with a scope-less
// OAuth token, a read-only server token (FAPPS_GITHUB_TOKEN) or none at all.
// Shapes measured on 2026-10-03:
//
//   - GET /repos/{o}/{r} carries `permissions` only for an authenticated
//     caller; anonymous reads have no such field.
//   - GET /repos/{o}/{r}/git/ref/tags/{tag} (SINGULAR) is an exact match or a
//     404; the plural `git/refs/tags/{prefix}` answers every tag that starts
//     with it, so it is never used.
//   - An annotated tag's ref points at a tag object (type "tag"), peeled with
//     GET /git/tags/{sha}, which also carries GitHub's `verification`.
//   - Release assets redirect once, github.com -> release-assets.githubusercontent.com.
//   - GET /repos/{o}/{r}/license?ref=<commit> honours the ref (a commit
//     without a license file is a 404), so the license is read at the tag's
//     commit, not on the default branch.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// Defaults for the two bases (config overrides them for the e2e fakes).
const (
	DefaultAPIURL = "https://api.github.com"
	DefaultRawURL = "https://raw.githubusercontent.com"
)

// Errors a caller turns into a check's code.
var (
	// ErrNotFound: GitHub answered 404 (for an anonymous or under-scoped
	// caller a private repository is also a 404).
	ErrNotFound = errors.New("not found on GitHub")
	// ErrNotATag: the ref names a branch, not a tag.
	ErrNotATag = errors.New("the ref is a branch, not a tag")
	// ErrBadRepo: "owner/name" is not a repository name.
	ErrBadRepo = errors.New("not a GitHub repository name")
	// ErrBadCredentials: GitHub refused the token itself (401, or the 403 it
	// answers for a token that has been revoked or has expired). It says
	// nothing about the repository, so a caller must never read it as
	// "private or does not exist" (BRF-Tech/filex#118).
	ErrBadCredentials = errors.New("GitHub did not accept the token")
)

// RateLimitError is GitHub's rate limit; worth retrying after Reset.
type RateLimitError struct {
	Reset time.Time
}

func (e *RateLimitError) Error() string {
	return "GitHub rate limit reached; resets at " + e.Reset.UTC().Format(time.RFC3339)
}

// APIError is an answer GitHub gave that is neither success, 404 nor a rate
// limit (a 5xx is worth retrying; Retryable says so).
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub answered %d: %s", e.Status, e.Message)
}

// Retryable reports whether err is a network failure or a GitHub failure
// worth trying again later (rate limit, 5xx, timeouts) - not an answer about
// the repository and not a refusal of an address.
func Retryable(err error) bool {
	if err == nil || safehttp.Refused(err) {
		return false
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrNotATag) || errors.Is(err, ErrBadRepo) ||
		errors.Is(err, ErrBadCredentials) {
		return false
	}
	var rl *RateLimitError
	if errors.As(err, &rl) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status >= 500 || ae.Status == http.StatusTooManyRequests
	}
	var se *safehttp.StatusError
	if errors.As(err, &se) {
		return se.Code >= 500 || se.Code == http.StatusTooManyRequests
	}
	return true
}

// Client reads GitHub. Safe for concurrent use.
type Client struct {
	http  *safehttp.Client
	api   string
	raw   string
	web   string
	token string
}

// Options builds a Client.
type Options struct {
	HTTP *safehttp.Client
	// APIURL and RawURL default to GitHub's own (FAPPS_GITHUB_API_URL; the
	// raw base follows the same pattern for the e2e fakes).
	APIURL string
	RawURL string
	// WebURL is where a release's assets are downloaded from
	// (https://github.com/<owner>/<name>/releases/download/<tag>/<file>);
	// default GitHub's own. Tests point it at a fake.
	WebURL string
	// Token is the server's own read token (FAPPS_GITHUB_TOKEN), used when a
	// call is not given the user's; empty means anonymous.
	Token string
}

// DefaultWebURL is GitHub's own web host (release downloads).
const DefaultWebURL = "https://github.com"

// New builds a Client.
func New(o Options) *Client {
	c := &Client{http: o.HTTP, api: strings.TrimRight(o.APIURL, "/"), raw: strings.TrimRight(o.RawURL, "/"),
		web: strings.TrimRight(o.WebURL, "/"), token: o.Token}
	if c.api == "" {
		c.api = DefaultAPIURL
	}
	if c.raw == "" {
		c.raw = DefaultRawURL
	}
	if c.web == "" {
		c.web = DefaultWebURL
	}
	return c
}

// ReleaseAssetURL is the address of a file attached to the release of a tag:
// the address filex reads a storage plugin's feed from when it installs "from
// its source" at that release.
func (c *Client) ReleaseAssetURL(owner, name, tag, file string) string {
	return c.web + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(file)
}

// ReleaseAsset reads a small file attached to the release of a tag (a
// storage plugin's filex-storage.json), at most max bytes. A release or a
// file that is not there is ErrNotFound; a larger file safehttp.ErrTooLarge.
func (c *Client) ReleaseAsset(ctx context.Context, owner, name, tag, file string, max int64) (*File, error) {
	u := c.ReleaseAssetURL(owner, name, tag, file)
	b, err := c.http.Get(ctx, u, max, nil)
	if err != nil {
		var se *safehttp.StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, file)
		}
		return nil, err
	}
	return &File{URL: u, Bytes: b.Bytes, SHA256: b.SHA256}, nil
}

// DownloadTo streams an allow-listed address into w and into sha256, at most
// max bytes (a storage plugin's build, for the plugin validator). A 404 is
// ErrNotFound. Nothing is kept but what w keeps.
func (c *Client) DownloadTo(ctx context.Context, rawURL string, max int64, w io.Writer) (*Asset, error) {
	d, err := c.http.CopyTo(ctx, rawURL, max, w)
	if err != nil {
		var se *safehttp.StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, rawURL)
		}
		return nil, err
	}
	return &Asset{URL: rawURL, SHA256: d.SHA256, Size: d.Size, Hops: d.Hops}, nil
}

var (
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// ParseRepo reads "owner/name", "github.com/owner/name" or
// "https://github.com/owner/name(.git)(/)" the way filex's FetchGitHub does.
func ParseRepo(s string) (owner, name string, err error) {
	r := strings.TrimSpace(s)
	r = strings.TrimPrefix(r, "https://github.com/")
	r = strings.TrimPrefix(r, "github.com/")
	r = strings.TrimSuffix(r, "/")
	r = strings.TrimSuffix(r, ".git")
	owner, name, ok := strings.Cut(r, "/")
	if !ok || strings.Contains(name, "/") || !ownerRe.MatchString(owner) || !nameRe.MatchString(name) || name == "." || name == ".." {
		return "", "", fmt.Errorf("%w: %q", ErrBadRepo, s)
	}
	return owner, name, nil
}

// Owner is a repository's owner.
type Owner struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"` // "User" | "Organization"
	// AvatarURL and Bio come with GET /users/{login} (Bio is an
	// organization's description too); a repository's owner object carries
	// the avatar only.
	AvatarURL string `json:"avatar_url,omitempty"`
	Bio       string `json:"bio,omitempty"`
}

// Permissions is the caller's role on a repository.
type Permissions struct {
	Admin    bool `json:"admin"`
	Maintain bool `json:"maintain"`
	Push     bool `json:"push"`
	Triage   bool `json:"triage"`
	Pull     bool `json:"pull"`
}

// CanList reports whether the role may list the repository in the store:
// admin or maintain (docs/ARCHITECTURE.md "Proof that the repository may be
// listed").
func (p *Permissions) CanList() bool { return p != nil && (p.Admin || p.Maintain) }

// Repo is the part of GET /repos/{o}/{r} the store reads.
type Repo struct {
	ID            int64        `json:"id"`
	Name          string       `json:"name"`
	FullName      string       `json:"full_name"`
	Owner         Owner        `json:"owner"`
	Private       bool         `json:"private"`
	Archived      bool         `json:"archived"`
	Disabled      bool         `json:"disabled"`
	Visibility    string       `json:"visibility"`
	DefaultBranch string       `json:"default_branch"`
	HTMLURL       string       `json:"html_url"`
	Homepage      string       `json:"homepage"`
	License       *License     `json:"license"`
	Permissions   *Permissions `json:"permissions"` // nil when the caller is anonymous
}

// License is GitHub's license detection for the default branch.
type License struct {
	SPDXID string `json:"spdx_id"` // "NOASSERTION" when GitHub could not tell
	Name   string `json:"name"`
}

// SPDX is the detected SPDX id, or "" when there is none or GitHub could not
// tell.
func (r *Repo) SPDX() string {
	if r == nil || r.License == nil || r.License.SPDXID == "NOASSERTION" {
		return ""
	}
	return r.License.SPDXID
}

// Repository reads a repository. token is the caller's own GitHub token (the
// submitter's, for the synchronous repo_access check: `permissions` is then
// that person's role); empty uses the server token or none, and Permissions
// is then the server identity's role or nil.
func (c *Client) Repository(ctx context.Context, token, owner, name string) (*Repo, error) {
	var r Repo
	if err := c.apiGet(ctx, token, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Owner reads a GitHub user or organization by login (GET /users/{login}):
// its numeric id, the login as GitHub writes it, and its type. A missing
// account is ErrNotFound.
func (c *Client) Owner(ctx context.Context, token, login string) (*Owner, error) {
	if !ownerRe.MatchString(login) {
		return nil, fmt.Errorf("%w: %q is not a GitHub login", ErrNotFound, login)
	}
	var o Owner
	if err := c.apiGet(ctx, token, "/users/"+url.PathEscape(login), &o); err != nil {
		return nil, err
	}
	if o.ID == 0 || o.Login == "" {
		return nil, &APIError{Status: http.StatusOK, Message: "GitHub answered an account without an id or a login"}
	}
	return &o, nil
}

// LicenseFile is GitHub's license detection at one ref.
type LicenseFile struct {
	Path    string   `json:"path"`
	SHA     string   `json:"sha"`
	License *License `json:"license"`
	// Carried is set when GitHub could not tell the license at the commit and
	// the answer is the default branch's for the same path and the same blob.
	Carried bool `json:"-"`
}

// SPDX is the detected SPDX id, or "" when GitHub could not tell.
func (l *LicenseFile) SPDX() string {
	if l == nil || l.License == nil || l.License.SPDXID == "NOASSERTION" {
		return ""
	}
	return l.License.SPDXID
}

// LicenseAt is GitHub's license detection AT a commit (GET
// /repos/{o}/{r}/license?ref=<commit>), not on the default branch, whose
// license may have changed since the tag (measured 2026-10-03:
// hashicorp/terraform answers NOASSERTION on main and MPL-2.0 with
// ?ref=v1.5.5). ErrNotFound: no license file at that commit.
func (c *Client) LicenseAt(ctx context.Context, token, owner, name, commit string) (*LicenseFile, error) {
	var l LicenseFile
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	if err := c.apiGet(ctx, token, base+"/license?ref="+url.QueryEscape(commit), &l); err != nil {
		return nil, err
	}
	if l.SPDX() != "" {
		return &l, nil
	}
	// GitHub answers NOASSERTION at some commits for a file it recognises on
	// the default branch (filextext-app v0.1.0 and v0.1.1, measured
	// 2026-10-04). When that file's blob at the commit is the very blob the
	// default branch's detection judged, carry that judgement over: it is
	// GitHub's own decision for the same bytes, not a guess. At most two more
	// calls, only in this case; anything else keeps the empty answer.
	var d LicenseFile
	if err := c.apiGet(ctx, token, base+"/license", &d); err != nil || d.SPDX() == "" || d.Path == "" || d.SHA == "" {
		if err != nil && Retryable(err) {
			return nil, err
		}
		return &l, nil
	}
	var f struct {
		SHA string `json:"sha"`
	}
	err := c.apiGet(ctx, token, base+"/contents/"+escapeRefPath(d.Path)+"?ref="+url.QueryEscape(commit), &f)
	switch {
	case err != nil && Retryable(err):
		return nil, err
	case err != nil || f.SHA != d.SHA:
		return &l, nil
	}
	return &LicenseFile{Path: d.Path, SHA: d.SHA, License: d.License, Carried: true}, nil
}

// Tag is a tag resolved to its commit.
type Tag struct {
	Name      string `json:"name"`
	Commit    string `json:"commit"`
	Annotated bool   `json:"annotated"`
	// Verified is GitHub's verification of the (outermost) tag object's
	// signature; informational. Reason is GitHub's word for it ("valid",
	// "unsigned", ...). Both empty for a lightweight tag.
	Verified bool   `json:"verified"`
	Reason   string `json:"verification_reason,omitempty"`
}

type gitRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

type gitTag struct {
	Tag    string `json:"tag"`
	SHA    string `json:"sha"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
	Verification *struct {
		Verified bool   `json:"verified"`
		Reason   string `json:"reason"`
	} `json:"verification"`
}

// ResolveTag resolves a tag to the commit it names, peeling annotated tags.
// A ref that names a branch is ErrNotATag; neither is ErrNotFound.
func (c *Client) ResolveTag(ctx context.Context, token, owner, name, tag string) (*Tag, error) {
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "refs/tags/")
	if tag == "" || strings.ContainsAny(tag, " ~^:?*[\\") || strings.Contains(tag, "..") {
		return nil, fmt.Errorf("%w: %q is not a tag name", ErrNotFound, tag)
	}
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/git"
	var ref gitRef
	err := c.apiGet(ctx, token, base+"/ref/tags/"+escapeRefPath(tag), &ref)
	if errors.Is(err, ErrNotFound) {
		var head gitRef
		herr := c.apiGet(ctx, token, base+"/ref/heads/"+escapeRefPath(tag), &head)
		if herr == nil && head.Ref == "refs/heads/"+tag {
			return nil, fmt.Errorf("%w: %s", ErrNotATag, tag)
		}
		if herr != nil && !errors.Is(herr, ErrNotFound) {
			return nil, herr
		}
		return nil, fmt.Errorf("%w: tag %s", ErrNotFound, tag)
	}
	if err != nil {
		return nil, err
	}
	if ref.Ref != "refs/tags/"+tag {
		return nil, fmt.Errorf("%w: tag %s (GitHub answered %s)", ErrNotFound, tag, ref.Ref)
	}
	out := &Tag{Name: tag}
	sha, typ := ref.Object.SHA, ref.Object.Type
	for depth := 0; typ == "tag"; depth++ {
		if depth >= 5 {
			return nil, fmt.Errorf("tag %s: more than 5 nested tag objects", tag)
		}
		var obj gitTag
		if err := c.apiGet(ctx, token, base+"/tags/"+url.PathEscape(sha), &obj); err != nil {
			return nil, err
		}
		if depth == 0 {
			out.Annotated = true
			if obj.Verification != nil {
				out.Verified, out.Reason = obj.Verification.Verified, obj.Verification.Reason
			}
		}
		sha, typ = obj.Object.SHA, obj.Object.Type
	}
	if typ != "commit" {
		return nil, fmt.Errorf("tag %s names a %s, not a commit", tag, typ)
	}
	if !shaRe.MatchString(sha) {
		return nil, fmt.Errorf("tag %s: GitHub answered a commit %q that is not a sha", tag, sha)
	}
	out.Commit = sha
	return out, nil
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// escapeRefPath escapes each segment of a ref name (a tag may contain "/").
func escapeRefPath(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// RawBase is the raw-file host's base address (no trailing slash).
func (c *Client) RawBase() string { return c.raw }

// RawURL is the address of a file in the repository at a commit (or a ref).
func (c *Client) RawURL(owner, name, at, path string) string {
	return c.raw + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" + escapeRefPath(at) + "/" + escapeRefPath(strings.TrimPrefix(path, "/"))
}

// File is a file read at a commit.
type File struct {
	URL    string
	Bytes  []byte
	SHA256 string
}

// FileAt reads path at commit (raw, not an API call), at most max bytes. A
// missing file is ErrNotFound; a larger one safehttp.ErrTooLarge.
func (c *Client) FileAt(ctx context.Context, owner, name, commit, path string, max int64) (*File, error) {
	u := c.RawURL(owner, name, commit, path)
	b, err := c.http.Get(ctx, u, max, nil)
	if err != nil {
		var se *safehttp.StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, err
	}
	return &File{URL: u, Bytes: b.Bytes, SHA256: b.SHA256}, nil
}

// Asset is a streamed download (a module, an interface bundle).
type Asset struct {
	URL    string
	SHA256 string
	Size   int64
	// Hops are the redirects followed (queries dropped).
	Hops []string
}

// Download streams a release asset (or any allow-listed address) into
// sha256, at most max bytes. A 404 is ErrNotFound.
func (c *Client) Download(ctx context.Context, rawURL string, max int64) (*Asset, error) {
	d, err := c.http.Hash(ctx, rawURL, max)
	if err != nil {
		var se *safehttp.StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, rawURL)
		}
		return nil, err
	}
	return &Asset{URL: rawURL, SHA256: d.SHA256, Size: d.Size, Hops: d.Hops}, nil
}

// apiGet GETs an API path into v.
func (c *Client) apiGet(ctx context.Context, token, path string, v any) error {
	server := token == ""
	if server {
		token = c.token
	}
	status, hdr, body, err := c.apiDo(ctx, token, path)
	if err != nil {
		return err
	}
	// The store reads public data only. When the repository's owner refuses
	// the SERVER's token by policy (measured 2026-10-04: the BRF-Tech
	// organization refuses fine-grained tokens that live longer than 366
	// days, 403 on every API read), or when GitHub refuses that token
	// outright (it was revoked or it expired), the same read is made
	// anonymously: a public repository answers either way, so the store's own
	// token must not decide what a person is told about their repository. A
	// person's own token is never swapped this way: its answer (their role)
	// is the point of the call.
	if server && token != "" && !rateLimited(status, hdr) &&
		((status == http.StatusForbidden && TokenRefusedByPolicy(body)) || credentialsRefused(status, body)) {
		if status, hdr, body, err = c.apiDo(ctx, "", path); err != nil {
			return err
		}
	}
	switch {
	case status == http.StatusOK:
		if err := json.Unmarshal(body, v); err != nil {
			return &APIError{Status: status, Message: "unreadable answer: " + err.Error()}
		}
		return nil
	case status == http.StatusNotFound:
		return ErrNotFound
	case rateLimited(status, hdr):
		return &RateLimitError{Reset: resetOf(hdr)}
	case credentialsRefused(status, body):
		return fmt.Errorf("%w: GitHub answered %d: %s", ErrBadCredentials, status, apiMessage(body))
	default:
		return &APIError{Status: status, Message: apiMessage(body)}
	}
}

// apiDo GETs an API path with the token (none when empty).
func (c *Client) apiDo(ctx context.Context, token, path string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.api+path, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
}

func rateLimited(status int, h http.Header) bool {
	return (status == http.StatusForbidden || status == http.StatusTooManyRequests) &&
		(h.Get("X-RateLimit-Remaining") == "0" || h.Get("Retry-After") != "")
}

func apiMessage(body []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Message
}

// TokenRefusedByPolicy reports a 403 whose message says an owner's policy
// refuses the token itself (fine-grained token rules, SAML enforcement),
// not the data. internal/auth/adminrepo's role check reads it too: BRF-Tech
// refuses a fine-grained token that lives longer than 366 days.
// credentialsRefused reports whether GitHub refused the token itself rather
// than answering about the path: 401 for a token it does not know any more
// (revoked, expired, mistyped), and the 403 it answers for one that cannot be
// used at all. Visibility plays no part in it.
func credentialsRefused(status int, body []byte) bool {
	if status == http.StatusUnauthorized {
		return true
	}
	if status != http.StatusForbidden {
		return false
	}
	m := strings.ToLower(apiMessage(body))
	return strings.Contains(m, "bad credentials") || strings.Contains(m, "revoked") ||
		(strings.Contains(m, "token") && strings.Contains(m, "expired"))
}

func TokenRefusedByPolicy(body []byte) bool {
	m := strings.ToLower(apiMessage(body))
	return strings.Contains(m, "forbids access via a fine-grained personal access token") ||
		strings.Contains(m, "saml enforcement")
}

func resetOf(h http.Header) time.Time {
	if s := h.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return time.Now().Add(time.Duration(n) * time.Second)
		}
	}
	if s := h.Get("X-RateLimit-Reset"); s != "" {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(n, 0)
		}
	}
	return time.Now().Add(time.Minute)
}
