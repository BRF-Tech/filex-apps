package adminrepo_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/auth/adminrepo"
	"github.com/brf-tech/filex-apps/backend/internal/auth/github"
	"github.com/brf-tech/filex-apps/backend/internal/auth/github/githubtest"
	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

const repo = "acme/store-admins"

type fixture struct {
	gh  *githubtest.Server
	p   *github.Provider
	log *bytes.Buffer
	ids map[string]int64
}

// setup: a private administration repository with one collaborator per
// role, plus an account that is no collaborator.
func setup(t *testing.T) *fixture {
	t.Helper()
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	gh.AddRepo(repo, 1404478182)
	f := &fixture{gh: gh, p: github.New(gh.ClientID, gh.ClientSecret, gh.URL, gh.URL, "https://store.example/cb"),
		log: &bytes.Buffer{}, ids: map[string]int64{}}
	for i, who := range []string{"owner", "maint", "writer", "triager", "reader", "outsider"} {
		f.ids[who] = int64(100 + i)
		gh.AddUser(f.ids[who], who, who)
	}
	gh.SetRole(repo, "owner", "admin")
	gh.SetRole(repo, "maint", "maintain")
	gh.SetRole(repo, "writer", "write")
	gh.SetRole(repo, "triager", "triage")
	gh.SetRole(repo, "reader", "read")
	return f
}

func (f *fixture) checker(token string) *adminrepo.Checker {
	return adminrepo.New(f.p, repo, token, slog.New(slog.NewTextHandler(f.log, nil)))
}

func TestTheRule(t *testing.T) {
	f := setup(t)
	c := f.checker(f.gh.ServerToken(repo))
	for _, tc := range []struct {
		login string
		roles []domain.Role
		word  string
	}{
		{"owner", []domain.Role{domain.RoleAdmin}, "admin"},
		{"maint", []domain.Role{domain.RoleAdmin}, "maintain"},
		{"writer", []domain.Role{domain.RoleAdmin}, "write"},
		{"triager", []domain.Role{domain.RoleReviewer}, "triage"},
		{"reader", nil, "read"},
		{"outsider", nil, "none"},
	} {
		got := c.Resolve(context.Background(), f.ids[tc.login], tc.login)
		if !slices.Equal(got.Roles, tc.roles) || got.Permission != tc.word || got.Source != "github repo permission "+repo+": "+tc.word {
			t.Errorf("%s: %+v, want roles %v from %q", tc.login, got, tc.roles, tc.word)
		}
	}
	if h := c.Health(); h.Status != adminrepo.StatusOK || h.LastOKAt == nil || h.Message != "" || c.Warning() != "" {
		t.Fatalf("health after good answers: %+v", h)
	}
}

func TestNoTokenFailsClosed(t *testing.T) {
	f := setup(t)
	c := f.checker("")
	got := c.Resolve(context.Background(), f.ids["owner"], "owner")
	if len(got.Roles) != 0 || !strings.Contains(got.Source, "not checked (FAPPS_ADMIN_TOKEN is not set)") {
		t.Fatalf("no token: %+v", got)
	}
	if f.gh.Hits["/repos/"+repo+"/collaborators/owner/permission"] != 0 {
		t.Fatal("GitHub was asked without a token")
	}
	if h := c.Health(); h.Status != adminrepo.StatusUnconfigured || h.TokenConfigured || !strings.HasPrefix(c.Warning(), "roles: FAPPS_ADMIN_TOKEN is not set") {
		t.Fatalf("health: %+v / %q", h, c.Warning())
	}
	if !strings.Contains(f.log.String(), "FAPPS_ADMIN_TOKEN is not set") {
		t.Fatalf("no log line: %s", f.log)
	}
}

// A fine-grained token the organization has not approved answers 404 for
// the repository and for every permission, exactly like a wrong name: the
// sign-in gets no role, and the health says the repository is unreachable
// instead of looking like "nobody is an administrator".
func TestATokenThatDoesNotSeeTheRepositoryFailsClosedAndSaysSo(t *testing.T) {
	f := setup(t)
	c := f.checker(f.gh.ServerToken())
	got := c.Resolve(context.Background(), f.ids["owner"], "owner")
	if len(got.Roles) != 0 || !strings.Contains(got.Source, "not visible to FAPPS_ADMIN_TOKEN") {
		t.Fatalf("unapproved token: %+v", got)
	}
	h := c.Health()
	if h.Status != adminrepo.StatusRepoUnreachable || !strings.Contains(h.Message, "organization's approval") || h.LastOKAt != nil {
		t.Fatalf("health: %+v", h)
	}
	if !strings.Contains(c.Warning(), "not visible to FAPPS_ADMIN_TOKEN") || !strings.Contains(f.log.String(), "level=ERROR") {
		t.Fatalf("warning %q, log %s", c.Warning(), f.log)
	}
	if p := c.Probe(context.Background()); p.Status != adminrepo.StatusRepoUnreachable {
		t.Fatalf("probe: %+v", p)
	}
}

// A login GitHub does not know answers 404 too, but the repository is
// visible: no role, and nothing is wrong with the check.
func TestAnUnknownLoginIsNoRoleNotAFault(t *testing.T) {
	f := setup(t)
	c := f.checker(f.gh.ServerToken(repo))
	got := c.Resolve(context.Background(), 999, "renamed-away")
	if len(got.Roles) != 0 || got.Permission != "none" || c.Health().Status != adminrepo.StatusOK {
		t.Fatalf("unknown login: %+v %+v", got, c.Health())
	}
}

func TestARefusedTokenOrABadAnswerFailsClosed(t *testing.T) {
	f := setup(t)
	bad := f.checker("github_pat_not_issued")
	got := bad.Resolve(context.Background(), f.ids["owner"], "owner")
	if len(got.Roles) != 0 || !strings.Contains(got.Source, "HTTP 401") {
		t.Fatalf("refused token: %+v", got)
	}
	if h := bad.Health(); h.Status != adminrepo.StatusFailing || !strings.Contains(h.Message, "FAPPS_ADMIN_TOKEN refused") {
		t.Fatalf("health: %+v", h)
	}

	c := f.checker(f.gh.ServerToken(repo))
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"no role at all":     {http.StatusOK, `{"user":{"id":100,"login":"owner"}}`, "carried no role"},
		"unknown legacy":     {http.StatusOK, `{"permission":"superuser","user":{"id":100}}`, "carried no role"},
		"another account":    {http.StatusOK, `{"permission":"admin","role_name":"admin","user":{"id":7,"login":"owner"}}`, "named another account"},
		"GitHub down":        {http.StatusBadGateway, `{"message":"bad gateway"}`, "HTTP 502"},
		"not JSON":           {http.StatusOK, `<html>`, "GitHub did not answer"},
		"rate limited (429)": {http.StatusTooManyRequests, `{}`, "rate limited"},
	} {
		f.gh.AnswerPermission(tc.status, tc.body)
		got := c.Resolve(context.Background(), f.ids["owner"], "owner")
		if len(got.Roles) != 0 || !strings.Contains(got.Source, tc.want) {
			t.Errorf("%s: %+v, want no role and %q", name, got, tc.want)
		}
	}
	f.gh.AnswerPermission(0, "")
	if got := c.Resolve(context.Background(), f.ids["owner"], "owner"); !slices.Equal(got.Roles, []domain.Role{domain.RoleAdmin}) {
		t.Fatalf("after the fault: %+v", got)
	}

	// An organization may refuse a fine-grained token that lives longer than
	// 366 days (measured 2026-10-04): the repository is out of reach, said
	// as such.
	f.gh.AnswerPermission(http.StatusForbidden, `{"message":"The 'acme' organization forbids access via a fine-grained personal access tokens if the token's lifetime is greater than 366 days. Please adjust your token's lifetime at the following URL: https://github.com/settings/personal-access-tokens/1","status":"403"}`)
	got = c.Resolve(context.Background(), f.ids["owner"], "owner")
	if h := c.Health(); len(got.Roles) != 0 || !strings.Contains(got.Source, "owner's policy refuses FAPPS_ADMIN_TOKEN") ||
		h.Status != adminrepo.StatusRepoUnreachable || !strings.Contains(h.Message, "366 days") {
		t.Fatalf("policy refusal: %+v %+v", got, h)
	}
	// A repository outside the token's grant answers 403 "Resource not
	// accessible by personal access token" (measured on a public one).
	f.gh.AnswerPermission(http.StatusForbidden, `{"message":"Resource not accessible by personal access token","status":"403"}`)
	got = c.Resolve(context.Background(), f.ids["owner"], "owner")
	if h := c.Health(); len(got.Roles) != 0 || h.Status != adminrepo.StatusRepoUnreachable || !strings.Contains(h.Message, "is not visible") {
		t.Fatalf("not accessible: %+v %+v", got, h)
	}
	f.gh.AnswerPermission(0, "")
	if got := c.Resolve(context.Background(), f.ids["owner"], "owner"); !slices.Equal(got.Roles, []domain.Role{domain.RoleAdmin}) {
		t.Fatalf("after the fault: %+v", got)
	}
	if c.Health().Status != adminrepo.StatusOK {
		t.Fatalf("health recovers: %+v", c.Health())
	}
}

func TestRuleReadsRoleNameFirst(t *testing.T) {
	str := func(s string) *string { return &s }
	type flags = github.RepoFlags
	withFlags := func(perm, role string, f flags) github.Permission {
		p := github.Permission{Permission: perm, RoleName: str(role)}
		p.User = &struct {
			ID          int64             `json:"id"`
			Login       string            `json:"login"`
			Permissions *github.RepoFlags `json:"permissions"`
		}{ID: 1, Permissions: &f}
		return p
	}
	for name, tc := range map[string]struct {
		in   github.Permission
		want []domain.Role
		word string
		ok   bool
	}{
		// legacy permission folds maintain into write and triage into read
		"maintain":                     {github.Permission{Permission: "write", RoleName: str("maintain")}, []domain.Role{domain.RoleAdmin}, "maintain", true},
		"triage reads read":            {github.Permission{Permission: "read", RoleName: str("triage")}, []domain.Role{domain.RoleReviewer}, "triage", true},
		"read":                         {github.Permission{Permission: "read", RoleName: str("read")}, nil, "read", true},
		"not a collaborator":           {github.Permission{Permission: "none", RoleName: str("")}, nil, "none", true},
		"custom role that may push":    {withFlags("write", "release-manager", flags{Push: true, Triage: true, Pull: true}), []domain.Role{domain.RoleAdmin}, "release-manager", true},
		"custom role that may triage":  {withFlags("read", "helpdesk", flags{Triage: true, Pull: true}), []domain.Role{domain.RoleReviewer}, "helpdesk", true},
		"custom role that may read":    {withFlags("read", "auditor", flags{Pull: true}), nil, "auditor", true},
		"custom role, no flags, write": {github.Permission{Permission: "write", RoleName: str("custom")}, []domain.Role{domain.RoleAdmin}, "custom", true},
		"no role_name, admin":          {github.Permission{Permission: "admin"}, []domain.Role{domain.RoleAdmin}, "admin", true},
		"no role_name, read (closed)":  {github.Permission{Permission: "read"}, nil, "read", true},
		"nothing":                      {github.Permission{}, nil, "", false},
	} {
		roles, word, ok := adminrepo.Rule(tc.in)
		if !slices.Equal(roles, tc.want) || word != tc.word || ok != tc.ok {
			t.Errorf("%s: %v %q %v, want %v %q %v", name, roles, word, ok, tc.want, tc.word, tc.ok)
		}
	}
}

func TestANilCheckerGivesNoRole(t *testing.T) {
	var c *adminrepo.Checker
	if got := c.Resolve(context.Background(), 1, "x"); len(got.Roles) != 0 || got.Source == "" {
		t.Fatalf("nil: %+v", got)
	}
	if c.Health().Status != adminrepo.StatusUnconfigured || c.Warning() == "" {
		t.Fatal("nil health")
	}
}

func lists(t *testing.T, f *fixture, repo, token string, admins, reviewers []config.Member) *adminrepo.Checker {
	t.Helper()
	return adminrepo.Open(adminrepo.Options{GitHub: f.p, Repo: repo, Token: token, Admins: admins, Reviewers: reviewers,
		Log: slog.New(slog.NewTextHandler(f.log, nil))})
}

// Without a repository the lists alone decide, by the account's numeric id:
// a login that matches a listed label but belongs to another id gets
// nothing, and GitHub is never asked.
func TestTheListsAloneMatchTheNumericID(t *testing.T) {
	f := setup(t)
	c := lists(t, f, "", f.gh.ServerToken(repo), []config.Member{{ID: 100, Login: "owner"}}, []config.Member{{ID: 103}, {ID: 100}})
	for _, tc := range []struct {
		id     int64
		login  string
		roles  []domain.Role
		source string
	}{
		{100, "owner", []domain.Role{domain.RoleAdmin}, "listed in FAPPS_ADMINS"},
		{103, "triager", []domain.Role{domain.RoleReviewer}, "listed in FAPPS_REVIEWERS"},
		{555, "owner", nil, "not listed in FAPPS_ADMINS or FAPPS_REVIEWERS"},
		{101, "maint", nil, "not listed in FAPPS_ADMINS or FAPPS_REVIEWERS"},
	} {
		got := c.Resolve(context.Background(), tc.id, tc.login)
		if !slices.Equal(got.Roles, tc.roles) || got.Source != tc.source || got.Unknown {
			t.Errorf("%d %s: %+v, want %v %q", tc.id, tc.login, got, tc.roles, tc.source)
		}
	}
	if len(f.gh.Hits) != 0 {
		t.Fatalf("GitHub was asked: %v", f.gh.Hits)
	}
	h := c.Probe(context.Background())
	if h.Status != adminrepo.StatusOK || h.Repo != "" || h.TokenConfigured || h.Admins != 1 || h.Reviewers != 1 || c.Warning() != "" || !c.MayGrant() {
		t.Fatalf("health: %+v %q", h, c.Warning())
	}
}

// With both, the role is the higher of the two; the source says both.
func TestTheRepositoryAndTheListsTogether(t *testing.T) {
	f := setup(t)
	c := lists(t, f, repo, f.gh.ServerToken(repo), []config.Member{{ID: 104, Login: "reader"}}, []config.Member{{ID: 102}, {ID: 105}})
	for _, tc := range []struct {
		login  string
		roles  []domain.Role
		source string
	}{
		{"reader", []domain.Role{domain.RoleAdmin}, "github repo permission " + repo + ": read; listed in FAPPS_ADMINS"},
		{"writer", []domain.Role{domain.RoleAdmin}, "github repo permission " + repo + ": write; listed in FAPPS_REVIEWERS"},
		{"outsider", []domain.Role{domain.RoleReviewer}, "github repo permission " + repo + ": none; listed in FAPPS_REVIEWERS"},
		{"owner", []domain.Role{domain.RoleAdmin}, "github repo permission " + repo + ": admin"},
		{"triager", []domain.Role{domain.RoleReviewer}, "github repo permission " + repo + ": triage"},
	} {
		got := c.Resolve(context.Background(), f.ids[tc.login], tc.login)
		if !slices.Equal(got.Roles, tc.roles) || got.Source != tc.source {
			t.Errorf("%s: %+v, want %v %q", tc.login, got, tc.roles, tc.source)
		}
	}

	// GitHub away: a listed administrator is one all the same (settled), a
	// listed reviewer is a reviewer whose repository role is unknown.
	f.gh.AnswerPermission(http.StatusBadGateway, `{"message":"bad gateway"}`)
	if got := c.Resolve(context.Background(), f.ids["reader"], "reader"); !slices.Equal(got.Roles, []domain.Role{domain.RoleAdmin}) || got.Unknown {
		t.Errorf("listed admin, GitHub away: %+v", got)
	}
	if got := c.Resolve(context.Background(), f.ids["outsider"], "outsider"); !slices.Equal(got.Roles, []domain.Role{domain.RoleReviewer}) || !got.Unknown ||
		!strings.Contains(got.Source, "not checked (GitHub answered HTTP 502)") {
		t.Errorf("listed reviewer, GitHub away: %+v", got)
	}
	if got := c.Resolve(context.Background(), f.ids["owner"], "owner"); len(got.Roles) != 0 || !got.Unknown {
		t.Errorf("not listed, GitHub away: %+v", got)
	}
	if h := c.Health(); h.Admins != 1 || h.Reviewers != 2 || h.Status != adminrepo.StatusFailing {
		t.Errorf("health: %+v", h)
	}
}

// A repository without a token grants nothing, the lists still do, and the
// health says which.
func TestListsWithARepositoryButNoToken(t *testing.T) {
	f := setup(t)
	c := lists(t, f, repo, "", []config.Member{{ID: 100}}, nil)
	if got := c.Resolve(context.Background(), 100, "owner"); !slices.Equal(got.Roles, []domain.Role{domain.RoleAdmin}) || got.Unknown {
		t.Fatalf("listed: %+v", got)
	}
	if got := c.Resolve(context.Background(), 102, "writer"); len(got.Roles) != 0 || got.Unknown || !strings.Contains(got.Source, "FAPPS_ADMIN_TOKEN is not set") {
		t.Fatalf("not listed: %+v", got)
	}
	if h := c.Health(); h.Status != adminrepo.StatusUnconfigured || !strings.Contains(h.Message, "FAPPS_ADMINS and FAPPS_REVIEWERS still apply") {
		t.Fatalf("health: %+v", h)
	}
	if !c.MayGrant() {
		t.Fatal("a list may grant")
	}
}

// Neither source: nobody gets a role, the health and the warning say so.
func TestNoSourceAtAll(t *testing.T) {
	f := setup(t)
	c := lists(t, f, "", "github_pat_unused", nil, nil)
	if got := c.Resolve(context.Background(), 100, "owner"); len(got.Roles) != 0 || got.Unknown || !strings.Contains(got.Source, "neither FAPPS_ADMIN_REPO nor FAPPS_ADMINS") {
		t.Fatalf("resolve: %+v", got)
	}
	if h := c.Health(); h.Status != adminrepo.StatusUnconfigured || h.TokenConfigured || !strings.Contains(c.Warning(), "Neither FAPPS_ADMIN_REPO nor FAPPS_ADMINS") || c.MayGrant() {
		t.Fatalf("health: %+v %q", h, c.Warning())
	}
	if len(f.gh.Hits) != 0 {
		t.Fatalf("GitHub was asked: %v", f.gh.Hits)
	}
}
