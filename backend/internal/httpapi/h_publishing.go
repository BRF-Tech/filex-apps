package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/auth/github"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// actor is the principal as the service and the audit log see it.
func actor(r *http.Request, p *session.Principal) pipeline.Actor {
	a := pipeline.Actor{AccountID: p.AccountID, IP: clientIP(r)}
	switch {
	case p.Has(domain.RoleAdmin):
		a.Role, a.Admin = audit.RoleAdmin, true
	case p.Has(domain.RoleReviewer):
		a.Role = audit.RoleReviewer
	case p.IsGitHub():
		a.Role = audit.RolePublisher
	default:
		a.Role = audit.RoleAnonymous
	}
	return a
}

type meView struct {
	Account struct {
		ID          int64  `json:"id"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email,omitempty"`
	} `json:"account"`
	Identity struct {
		Provider string `json:"provider"`
		Login    string `json:"login"`
	} `json:"identity"`
	Roles      []string         `json:"roles"`
	RoleSource string           `json:"role_source"`
	Publishers []membershipView `json:"publishers"`
	CSRFToken  string           `json:"csrf_token"`
	Providers  providersView    `json:"providers"`
}

// providersView says which sign-ins this server can start: the session key
// is set and the provider is configured (it may still be down). The web
// interface reads it from /api/v1/me instead of probing /auth/*/login, so
// asking does not spend the sign-in rate limit. GitHub is the only one.
type providersView struct {
	GitHub bool `json:"github"`
}

func (h *handler) providers() providersView {
	return providersView{GitHub: h.sessions != nil && h.GitHub.Configured()}
}

type membershipView struct {
	publisherRefView
	Role   string `json:"role"`
	Status string `json:"status"`
}

// getMe answers the session, or - signed out - 401 with the sign-ins that
// can be started.
func (h *handler) getMe(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": "unauthorized", "message": "Sign in first.", "providers": h.providers(),
		})
		return
	}
	ms, err := store.MembershipsOf(r.Context(), h.Pool, p.AccountID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var out meView
	out.Account.ID, out.Account.DisplayName, out.Account.Email = p.AccountID, p.DisplayName, p.Email
	out.Identity.Provider, out.Identity.Login = p.Provider, p.Login
	out.Roles = []string{}
	for _, rl := range p.Roles {
		out.Roles = append(out.Roles, string(rl))
	}
	out.RoleSource = p.RoleSource
	out.Publishers = []membershipView{}
	for _, m := range ms {
		out.Publishers = append(out.Publishers, membershipView{
			publisherRefView: publisherRefView{ID: m.ID, Name: m.Name, Verified: m.Verified, Official: m.Official},
			Role:             m.Role, Status: m.Status,
		})
	}
	out.CSRFToken = p.CSRFToken
	out.Providers = h.providers()
	writeJSON(w, http.StatusOK, out)
}

// githubToken is the publisher's own GitHub token (a GitHub session only).
func (h *handler) githubToken(w http.ResponseWriter, p *session.Principal) (string, bool) {
	if !p.IsGitHub() {
		writeError(w, http.StatusForbidden, "github_required", "Sign in with GitHub to publish.")
		return "", false
	}
	token, err := h.sessions.GitHubToken(p)
	if err != nil || token == "" {
		writeError(w, http.StatusForbidden, "github_token_invalid", "Sign in with GitHub again.")
		return "", false
	}
	return token, true
}

// requestPublisher asks for a publisher bound to a GitHub owner.
func (h *handler) requestPublisher(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	var body struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		GitHubOwner string `json:"github_owner"`
		Homepage    string `json:"homepage"`
	}
	if err := decode(w, r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	if !publisherIDRe.MatchString(body.ID) {
		h.fail(w, r, invalid("id must be 3 to 40 lower-case letters, digits and hyphens, not starting or ending with a hyphen."))
		return
	}
	name, err := cleanText("name", body.Name, 1, 80)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if !githubLoginRe.MatchString(body.GitHubOwner) {
		h.fail(w, r, invalid("github_owner must be a GitHub login."))
		return
	}
	if body.Homepage != "" {
		u, err := url.Parse(body.Homepage)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(body.Homepage) > 300 {
			h.fail(w, r, invalid("homepage must be an http(s) address."))
			return
		}
	}
	token, ok := h.githubToken(w, p)
	if !ok {
		return
	}
	owner, err := h.GitHub.Owner(r.Context(), token, body.GitHubOwner)
	switch {
	case errors.Is(err, github.ErrNotFound):
		h.fail(w, r, invalid("GitHub has no account or organization %q.", body.GitHubOwner))
		return
	case err != nil:
		h.log.Warn("github owner lookup", "rid", requestID(r), "err", err)
		writeError(w, http.StatusServiceUnavailable, "github_unavailable", "GitHub could not be asked about that owner.")
		return
	}
	subject, _ := strconv.ParseInt(p.Subject, 10, 64)
	pub, err := h.Service.RequestPublisher(r.Context(), pipeline.PublisherRequest{
		Actor: actor(r, p), GitHubUserID: subject, ID: body.ID, Name: name, Homepage: body.Homepage,
		Owner: pipeline.Owner{ID: owner.ID, Login: owner.Login, Type: owner.Type},
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pubView(pub))
}

// createSubmission submits a repository at a tag.
func (h *handler) createSubmission(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	var body struct {
		Publisher string `json:"publisher"`
		Repo      string `json:"repo"`
		Ref       string `json:"ref"`
		Note      string `json:"note"`
		// Kind: "app" (default) or "storage" (a storage plugin).
		Kind string `json:"kind"`
	}
	if err := decode(w, r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	if body.Kind != "" && !domain.ValidSubmissionKind(body.Kind) {
		h.fail(w, r, invalid("kind must be app or storage."))
		return
	}
	if !publisherIDRe.MatchString(body.Publisher) {
		h.fail(w, r, invalid("publisher is not a publisher id."))
		return
	}
	if len(body.Repo) > 300 {
		h.fail(w, r, invalid("repo is too long."))
		return
	}
	note, err := cleanText("note", body.Note, 0, 4000)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	token, ok := h.githubToken(w, p)
	if !ok {
		return
	}
	if !h.limit(w, r, ratelimit.SubmitPerAccount, strconv.FormatInt(p.AccountID, 10)) ||
		!h.limit(w, r, ratelimit.SubmitPerPublisher, body.Publisher) {
		return
	}
	sub, err := h.Service.Submit(r.Context(), pipeline.SubmitRequest{
		Actor: actor(r, p), Token: token, Publisher: body.Publisher, Repo: body.Repo, Ref: body.Ref, Note: note, Kind: body.Kind,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, h.submissionView(sub))
}

// listSubmissions: a publisher sees its publishers', a reviewer all.
func (h *handler) listSubmissions(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	pg, pp, err := page(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && !validStatus(status) {
		h.fail(w, r, invalid("status is not a submission status."))
		return
	}
	f := store.SubmissionFilter{Status: status, Publisher: q.Get("publisher")}
	if !p.Has(domain.RoleReviewer) {
		id := p.AccountID
		f.MemberOf = &id
	}
	h.writeSubmissionPage(w, r, f, store.Page{Page: pg, PerPage: pp})
}

func (h *handler) writeSubmissionPage(w http.ResponseWriter, r *http.Request, f store.SubmissionFilter, pg store.Page) {
	subs, total, err := store.ListSubmissions(r.Context(), h.Pool, f, pg)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := []submissionView{}
	for _, s := range subs {
		items = append(items, h.submissionView(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func validStatus(s string) bool {
	switch domain.SubmissionStatus(s) {
	case domain.SubQueued, domain.SubResolving, domain.SubValidating, domain.SubNeedsReview,
		domain.SubApproved, domain.SubRejected, domain.SubFailed, domain.SubWithdrawn:
		return true
	}
	return false
}

func submissionID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

type submissionDetailView struct {
	submissionView
	Note       string          `json:"note,omitempty"`
	Resolution json.RawMessage `json:"resolution,omitempty"`
	Checks     []checkView     `json:"checks"`
	DryRuns    []dryRunView    `json:"dry_runs"`
	Reviews    []reviewView    `json:"reviews"`
	Diff       *pipeline.Diff  `json:"diff,omitempty"`
}

// getSubmission: one submission with its checks, dry runs and reviews.
func (h *handler) getSubmission(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	id, ok := submissionID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "No such submission.")
		return
	}
	sub, err := store.GetSubmission(r.Context(), h.Pool, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "No such submission.")
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if ok, err := h.Service.CanSee(r.Context(), sub, p.AccountID, p.Has(domain.RoleReviewer)); err != nil {
		h.fail(w, r, err)
		return
	} else if !ok {
		writeError(w, http.StatusNotFound, "not_found", "No such submission.")
		return
	}
	checks, err := store.ChecksOf(r.Context(), h.Pool, sub.ID, sub.Attempt)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	reviews, err := store.ReviewsOf(r.Context(), h.Pool, sub.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := submissionDetailView{submissionView: h.submissionView(sub), Note: sub.Note,
		Checks: checkViews(checks), DryRuns: []dryRunView{}, Reviews: []reviewView{}}
	if len(sub.Resolution) > 0 && string(sub.Resolution) != "null" {
		out.Resolution = sub.Resolution
	}
	for _, d := range pipeline.DryRuns(checks) {
		out.DryRuns = append(out.DryRuns, dryRunView{DryRunResult: d})
	}
	for _, rv := range reviews {
		out.Reviews = append(out.Reviews, reviewViewOf(rv))
	}
	var res domain.Resolution
	if out.Resolution != nil && json.Unmarshal(sub.Resolution, &res) == nil && res.RepoID != 0 {
		if app, err := store.GetAppByRepoID(r.Context(), h.Pool, res.RepoID); err == nil {
			versions, err := store.VersionsOf(r.Context(), h.Pool, app.ID)
			if err != nil {
				h.fail(w, r, err)
				return
			}
			index.SortVersions(versions)
			for _, v := range versions {
				if v.SubmissionID != sub.ID {
					d := pipeline.MakeDiff(v, &res)
					out.Diff = &d
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) withdrawSubmission(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	id, ok := submissionID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "No such submission.")
		return
	}
	sub, err := h.Service.Withdraw(r.Context(), id, actor(r, p))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.submissionView(sub))
}

func (h *handler) retrySubmission(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	id, ok := submissionID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "No such submission.")
		return
	}
	token, ok := h.githubToken(w, p)
	if !ok {
		return
	}
	if !h.limit(w, r, ratelimit.SubmitPerAccount, strconv.FormatInt(p.AccountID, 10)) {
		return
	}
	sub, err := h.Service.Retry(r.Context(), id, actor(r, p), token)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, h.submissionView(sub))
}
