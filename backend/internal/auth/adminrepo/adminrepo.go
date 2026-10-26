// Package adminrepo is where the store roles come from: a GitHub account's
// permission on one repository, the administration repository
// (FAPPS_ADMIN_REPO, no default), read at every sign-in, and the lists
// FAPPS_ADMINS and FAPPS_REVIEWERS, which name GitHub accounts by their
// numeric user id (never by login: a login can be given up and registered
// again by someone else). Either source may be used alone; with both, a
// person holds the higher of the two roles.
//
// The repository is private, so the signing-in person's scope-less OAuth
// token cannot see it; the server asks with its own token
// (FAPPS_ADMIN_TOKEN, a fine-grained token of the repository's owner with
// Metadata: read on that repository only), through GitHub's "Get repository
// permissions for a user" endpoint
// (GET /repos/{owner}/{repo}/collaborators/{login}/permission). The shapes it
// answers were measured on 2026-10-04 (auth/github.Permission,
// docs/ARCHITECTURE.md "Roles").
//
// The rule (Rule): role_name admin, maintain or write -> administrator;
// triage -> reviewer; read, none, not a collaborator -> no role. Everything
// else fails CLOSED: without a token, when the token does not see the
// repository (a fine-grained token the organization has not approved yet
// answers 404 exactly like a wrong repository name), when GitHub refuses or
// does not answer, or when the answer carries no role, the person signs in
// without a store role. Each such case writes a log line and changes Health,
// which /readyz (warnings) and GET /api/v1/admin/health show, so a missing
// approval does not quietly look like "nobody is an administrator".
package adminrepo

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/auth/github"
	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// Health states (api/openapi.yaml RoleSourceHealth).
const (
	StatusOK              = "ok"
	StatusUnconfigured    = "unconfigured"
	StatusUnchecked       = "unchecked"
	StatusRepoUnreachable = "repo_unreachable"
	StatusFailing         = "failing"
)

// Health is the role check's last known state. Repo is "" when no
// administration repository is configured; Admins and Reviewers count the
// accounts of FAPPS_ADMINS and FAPPS_REVIEWERS.
type Health struct {
	Repo            string     `json:"repo"`
	TokenConfigured bool       `json:"token_configured"`
	Admins          int        `json:"admins"`
	Reviewers       int        `json:"reviewers"`
	Status          string     `json:"status"`
	Message         string     `json:"message,omitempty"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
	LastOKAt        *time.Time `json:"last_ok_at,omitempty"`
}

// Result is what one sign-in established.
type Result struct {
	Roles []domain.Role
	// Permission is GitHub's word for the account's role ("write",
	// "triage", "none", ...); "" when GitHub was not asked or did not say.
	Permission string
	// Source says why the session holds Roles, in the words the audit log
	// and /api/v1/me keep: "github repo permission <repo>: <permission>",
	// or "... : not checked (<why>)"; a list that grants a role adds
	// "; listed in FAPPS_ADMINS" (or FAPPS_REVIEWERS).
	Source string
	// Unknown: the administration repository should have been asked and
	// could not be (GitHub away, the repository out of reach, an answer
	// without a role), so Roles may be lower than the account's. A list
	// that makes the account an administrator settles it.
	Unknown bool
}

// RoleNames is Roles as strings (never nil).
func (r Result) RoleNames() []string {
	out := make([]string, 0, len(r.Roles))
	for _, rl := range r.Roles {
		out = append(out, string(rl))
	}
	return out
}

// Checker asks GitHub and reads the lists. A nil Checker answers every
// sign-in with no role.
type Checker struct {
	gh    *github.Provider
	repo  string // owner/name as configured ("": no repository is asked)
	owner string
	name  string
	token string
	log   *slog.Logger
	now   func() time.Time

	// admins and reviewers are FAPPS_ADMINS and FAPPS_REVIEWERS by GitHub
	// user id.
	admins    map[int64]bool
	reviewers map[int64]bool

	mu sync.Mutex
	h  Health
}

// Options configure a Checker.
type Options struct {
	GitHub *github.Provider
	// Repo is the administration repository ("owner/name"; config validates
	// the shape), or "" for none. Token reads it (may be empty: then the
	// repository grants no role); without Repo it is not used.
	Repo  string
	Token string
	// Admins and Reviewers are the accounts of FAPPS_ADMINS and
	// FAPPS_REVIEWERS (an account on both is an administrator).
	Admins, Reviewers []config.Member
	Log               *slog.Logger
}

// New builds a checker for repo with the server's token and no lists.
func New(gh *github.Provider, repo, token string, log *slog.Logger) *Checker {
	return Open(Options{GitHub: gh, Repo: repo, Token: token, Log: log})
}

// Open builds a checker.
func Open(o Options) *Checker {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	token := o.Token
	if o.Repo == "" {
		token = ""
	}
	owner, name, _ := strings.Cut(o.Repo, "/")
	c := &Checker{gh: o.GitHub, repo: o.Repo, owner: owner, name: name, token: token, log: log, now: time.Now,
		admins: map[int64]bool{}, reviewers: map[int64]bool{}}
	for _, m := range o.Admins {
		c.admins[m.ID] = true
	}
	for _, m := range o.Reviewers {
		if !c.admins[m.ID] {
			c.reviewers[m.ID] = true
		}
	}
	c.h = Health{Repo: o.Repo, TokenConfigured: token != "", Admins: len(c.admins), Reviewers: len(c.reviewers), Status: StatusUnchecked}
	switch {
	case o.Repo == "" && !c.hasLists():
		c.h.Status, c.h.Message = StatusUnconfigured, "Neither FAPPS_ADMIN_REPO nor FAPPS_ADMINS is set: nobody gets a store role (reviewer, administrator)."
	case o.Repo == "":
		// The lists alone: nothing to ask, nothing that can fail.
		c.h.Status = StatusOK
	case token == "" && c.hasLists():
		c.h.Status, c.h.Message = StatusUnconfigured, "FAPPS_ADMIN_TOKEN is not set: new sign-ins get no role from the administration repository "+
			o.Repo+" (FAPPS_ADMINS and FAPPS_REVIEWERS still apply)."
	case token == "":
		c.h.Status, c.h.Message = StatusUnconfigured, "FAPPS_ADMIN_TOKEN is not set: new sign-ins get no store role (reviewer, administrator)."
	}
	return c
}

func (c *Checker) hasLists() bool { return len(c.admins)+len(c.reviewers) > 0 }

// MayGrant says whether any source can give a role: the repository with a
// token, or a list. False: every sign-in gets no role, nothing is asked.
func (c *Checker) MayGrant() bool {
	return c != nil && (c.token != "" || c.hasLists())
}

// Repo is the administration repository, "owner/name".
func (c *Checker) Repo() string {
	if c == nil {
		return ""
	}
	return c.repo
}

func (c *Checker) source(what string) string {
	return "github repo permission " + c.repo + ": " + what
}

// notChecked: the repository was not asked or did not answer usefully.
// With a token that is a fault (the roles are unknown); without one it is
// the configuration.
func (c *Checker) notChecked(why string) Result {
	return Result{Source: c.source("not checked (" + why + ")"), Unknown: c.token != ""}
}

// Resolve reads the store roles of the GitHub account (userID, login) that
// is signing in: the higher of what the administration repository and the
// lists give. It never fails: a check that cannot be made answers no role
// from that source (see the package comment) and says why in Source.
func (c *Checker) Resolve(ctx context.Context, userID int64, login string) Result {
	if c == nil {
		return Result{Source: "github repo permission: not checked (no role check on this server)"}
	}
	var listed []domain.Role
	var listWord string
	switch {
	case c.admins[userID]:
		listed, listWord = []domain.Role{domain.RoleAdmin}, "listed in FAPPS_ADMINS"
	case c.reviewers[userID]:
		listed, listWord = []domain.Role{domain.RoleReviewer}, "listed in FAPPS_REVIEWERS"
	}
	if c.repo == "" {
		if !c.hasLists() {
			return Result{Source: "no role source (neither FAPPS_ADMIN_REPO nor FAPPS_ADMINS is set)"}
		}
		if listed == nil {
			c.log.Info("roles", "login", login, "user_id", userID, "roles", listed, "source", "not listed")
			return Result{Source: "not listed in FAPPS_ADMINS or FAPPS_REVIEWERS"}
		}
		c.log.Info("roles", "login", login, "user_id", userID, "roles", listed, "source", listWord)
		return Result{Roles: listed, Source: listWord}
	}
	res := c.resolveRepo(ctx, userID, login)
	if listed == nil {
		return res
	}
	c.log.Info("roles: a list grants a role", "login", login, "user_id", userID, "listed", listed, "repo_roles", res.Roles)
	if rank(listed) > rank(res.Roles) {
		res.Roles = listed
	}
	res.Source += "; " + listWord
	if listed[0] == domain.RoleAdmin {
		res.Unknown = false
	}
	return res
}

// rank orders role sets: administrator over reviewer over none.
func rank(roles []domain.Role) int {
	switch {
	case slices.Contains(roles, domain.RoleAdmin):
		return 2
	case slices.Contains(roles, domain.RoleReviewer):
		return 1
	}
	return 0
}

// resolveRepo asks the administration repository.
func (c *Checker) resolveRepo(ctx context.Context, userID int64, login string) Result {
	if c.token == "" {
		c.log.Warn("roles: FAPPS_ADMIN_TOKEN is not set; signing in without a role from the administration repository", "repo", c.repo, "login", login)
		return c.notChecked("FAPPS_ADMIN_TOKEN is not set")
	}
	perm, err := c.gh.RepoPermission(ctx, c.token, c.owner, c.name, login)
	if errors.Is(err, github.ErrNotFound) {
		// GitHub says 404 both for a login that is no account and for a
		// repository the token does not see; only the second is a fault.
		switch verr := c.gh.RepoVisible(ctx, c.token, c.owner, c.name); {
		case verr == nil:
			c.markOK()
			c.log.Info("roles: GitHub does not know the login; no store role", "repo", c.repo, "login", login)
			return Result{Permission: "none", Source: c.source("none (GitHub does not know the account)")}
		case errors.Is(verr, github.ErrNotFound):
			c.markUnreachable(notVisibleWhy)
			return c.notChecked("the administration repository is not visible to FAPPS_ADMIN_TOKEN")
		default:
			err = verr
		}
	}
	if refusedByPolicy(err) {
		c.markUnreachable(policyWhy)
		return c.notChecked("the owner's policy refuses FAPPS_ADMIN_TOKEN")
	}
	if notAccessible(err) {
		c.markUnreachable(notVisibleWhy)
		return c.notChecked("the administration repository is not visible to FAPPS_ADMIN_TOKEN")
	}
	if err != nil {
		why := describe(err)
		c.markFailing(why)
		c.log.Error("roles: the role check failed; signing in without a store role", "repo", c.repo, "login", login, "err", err)
		return c.notChecked(why)
	}
	c.markOK()
	if perm.User != nil && perm.User.ID != 0 && perm.User.ID != userID {
		c.log.Error("roles: GitHub's answer names another account; signing in without a store role",
			"repo", c.repo, "login", login, "user_id", userID, "answer_user_id", perm.User.ID)
		return c.notChecked("GitHub's answer named another account")
	}
	roles, word, ok := Rule(perm)
	if !ok {
		c.log.Error("roles: GitHub's answer carries no role; signing in without a store role",
			"repo", c.repo, "login", login, "permission", perm.Permission)
		return c.notChecked("GitHub's answer carried no role")
	}
	c.log.Info("roles", "repo", c.repo, "login", login, "permission", word, "roles", roles)
	return Result{Roles: roles, Permission: word, Source: c.source(word)}
}

// Rule maps GitHub's answer to store roles. word is what GitHub called the
// role; ok is false when the answer says nothing usable (no role_name and no
// known permission), which the caller treats as no role.
//
// role_name is read first: the legacy `permission` folds maintain into write
// and triage into read, so it alone cannot tell a reviewer from a reader. A
// custom role (any other role_name) is judged by what it may do (the user's
// permission flags), else by its legacy base role. Without role_name the
// legacy base role decides, and `read` stays no role (a triage collaborator
// is not recognised then: closed, not open).
func Rule(p github.Permission) (roles []domain.Role, word string, ok bool) {
	admin := []domain.Role{domain.RoleAdmin}
	reviewer := []domain.Role{domain.RoleReviewer}
	if p.RoleName != nil && *p.RoleName != "" {
		word = *p.RoleName
		switch word {
		case "admin", "maintain", "write":
			return admin, word, true
		case "triage":
			return reviewer, word, true
		case "read", "none":
			return nil, word, true
		}
		if p.User != nil && p.User.Permissions != nil {
			f := p.User.Permissions
			switch {
			case f.Admin || f.Maintain || f.Push:
				return admin, word, true
			case f.Triage:
				return reviewer, word, true
			}
			return nil, word, true
		}
	}
	if word == "" {
		word = p.Permission
	}
	switch p.Permission {
	case "admin", "write":
		return admin, word, true
	case "read", "none":
		return nil, word, true
	}
	return nil, word, false
}

// Probe asks GitHub whether the token still sees the repository and records
// the answer (start-up, the admin health endpoint). Without a repository or
// a token it only reports that.
func (c *Checker) Probe(ctx context.Context) Health {
	if c == nil {
		return Health{Status: StatusUnconfigured, Message: "No role check on this server."}
	}
	if c.token == "" {
		return c.Health()
	}
	switch err := c.gh.RepoVisible(ctx, c.token, c.owner, c.name); {
	case err == nil:
		c.markOK()
	case errors.Is(err, github.ErrNotFound):
		c.markUnreachable(notVisibleWhy)
	case refusedByPolicy(err):
		c.markUnreachable(policyWhy)
	case notAccessible(err):
		c.markUnreachable(notVisibleWhy)
	default:
		why := describe(err)
		c.markFailing(why)
		c.log.Error("roles: the administration repository check failed", "repo", c.repo, "err", err)
	}
	return c.Health()
}

// Health is a copy of the last known state.
func (c *Checker) Health() Health {
	if c == nil {
		return Health{Status: StatusUnconfigured, Message: "No role check on this server."}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.h
	if h.CheckedAt != nil {
		t := *h.CheckedAt
		h.CheckedAt = &t
	}
	if h.LastOKAt != nil {
		t := *h.LastOKAt
		h.LastOKAt = &t
	}
	return h
}

// Warning is the health as one /readyz warning, "" when nothing is wrong
// (ok, or not asked yet).
func (c *Checker) Warning() string {
	h := c.Health()
	if h.Status == StatusOK || h.Status == StatusUnchecked {
		return ""
	}
	return "roles: " + h.Message
}

func (c *Checker) set(status, message string, ok bool) {
	now := c.now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.h.Status, c.h.Message, c.h.CheckedAt = status, message, &now
	if ok {
		c.h.LastOKAt = &now
	}
}

func (c *Checker) markOK() { c.set(StatusOK, "", true) }

// Why the administration repository is out of reach (StatusRepoUnreachable).
const (
	notVisibleWhy = "is not visible to FAPPS_ADMIN_TOKEN (a fine-grained token without access to it or waiting " +
		"for the organization's approval, a wrong repository name, or revoked access)"
	policyWhy = "is closed to FAPPS_ADMIN_TOKEN by its owner's policy (GitHub answered 403: a fine-grained token " +
		"living longer than 366 days, which an organization may forbid, or SAML enforcement)"
)

func (c *Checker) markUnreachable(why string) {
	c.set(StatusRepoUnreachable, "The administration repository "+c.repo+" "+why+": new sign-ins get no store role.", false)
	c.log.Error("roles: the administration repository "+why+"; new sign-ins get no store role", "repo", c.repo)
}

// refusedByPolicy: GitHub's 403 says the owner refuses the token itself.
func refusedByPolicy(err error) bool {
	var se *github.StatusError
	return errors.As(err, &se) && se.PolicyRefused
}

// notAccessible: GitHub's 403 says the token was not given the repository.
func notAccessible(err error) bool {
	var se *github.StatusError
	return errors.As(err, &se) && se.NotAccessible
}

func (c *Checker) markFailing(why string) {
	c.set(StatusFailing, "The role check failed ("+why+"): new sign-ins get no store role until GitHub answers.", false)
}

// describe says what went wrong without the token or a URL in it.
func describe(err error) string {
	var se *github.StatusError
	if errors.As(err, &se) {
		s := "GitHub answered HTTP " + strconv.Itoa(se.Status)
		switch {
		case se.RateLimited:
			s += ", rate limited"
		case se.Status == 401:
			s += ", FAPPS_ADMIN_TOKEN refused"
		}
		return s
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "GitHub did not answer in time"
	}
	return "GitHub did not answer"
}
