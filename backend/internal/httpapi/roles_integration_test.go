//go:build integration

package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// The store roles come from the account's permission on the administration
// repository, read with the server's token at every sign-in
// (internal/auth/adminrepo); everyone signs in with GitHub.

type meBody struct {
	Identity   struct{ Provider, Login string }
	Roles      []string
	RoleSource string `json:"role_source"`
}

func (c *client) me(t *testing.T) meBody {
	t.Helper()
	var m meBody
	r := c.get("/api/v1/me")
	if r.status != http.StatusOK {
		t.Fatalf("/api/v1/me: %d %s", r.status, r.body)
	}
	r.json(t, &m)
	return m
}

// lifetime is how long the newest session of the login lasts.
func (s *stack) lifetime(login string) time.Duration {
	s.t.Helper()
	var created, expires time.Time
	err := s.pool.QueryRow(context.Background(), `
		SELECT s.created_at, s.expires_at FROM sessions s JOIN identities i ON i.id = s.identity_id
		WHERE i.login = $1 ORDER BY s.created_at DESC LIMIT 1`, login).Scan(&created, &expires)
	if err != nil {
		s.t.Fatal(err)
	}
	return expires.Sub(created).Round(time.Hour)
}

func TestRolesComeFromTheAdministrationRepository(t *testing.T) {
	s := newStack(t)
	for i, tc := range []struct {
		login, role, want, source string
		review, admin             int
		life                      time.Duration
	}{
		{"owner", "admin", "admin", "admin", http.StatusOK, http.StatusOK, 12 * time.Hour},
		{"maint", "maintain", "admin", "maintain", http.StatusOK, http.StatusOK, 12 * time.Hour},
		{"writer", "write", "admin", "write", http.StatusOK, http.StatusOK, 12 * time.Hour},
		{"triager", "triage", "reviewer", "triage", http.StatusOK, http.StatusForbidden, 12 * time.Hour},
		{"reader", "read", "", "read", http.StatusForbidden, http.StatusForbidden, 7 * 24 * time.Hour},
		{"outsider", "", "", "none", http.StatusForbidden, http.StatusForbidden, 7 * 24 * time.Hour},
	} {
		c := s.signInWithRole(int64(100+i), tc.login, tc.role)
		m := c.me(t)
		if got := strings.Join(m.Roles, ","); got != tc.want || m.Identity.Provider != "github" ||
			m.RoleSource != "github repo permission "+adminRepo+": "+tc.source {
			t.Errorf("%s (%q): roles %q source %q, want %q from %q", tc.login, tc.role, got, m.RoleSource, tc.want, tc.source)
		}
		if r := c.get("/api/v1/review/queue"); r.status != tc.review {
			t.Errorf("%s: review queue %d, want %d", tc.login, r.status, tc.review)
		}
		if r := c.get("/api/v1/admin/publishers"); r.status != tc.admin {
			t.Errorf("%s: admin publishers %d, want %d", tc.login, r.status, tc.admin)
		}
		if got := s.lifetime(tc.login); got != tc.life {
			t.Errorf("%s: session lasts %s, want %s", tc.login, got, tc.life)
		}
	}

	// The audit log keeps who signed in, with which role, and why.
	rows, err := s.pool.Query(context.Background(), `SELECT actor_role, data FROM audit_log WHERE action = 'auth.signin' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var role string
		var data []byte
		if err := rows.Scan(&role, &data); err != nil {
			t.Fatal(err)
		}
		var d struct {
			Login      string   `json:"login"`
			Roles      []string `json:"roles"`
			RoleSource string   `json:"role_source"`
		}
		_ = json.Unmarshal(data, &d)
		got = append(got, d.Login+"|"+role+"|"+strings.Join(d.Roles, ",")+"|"+strings.TrimPrefix(d.RoleSource, "github repo permission "+adminRepo+": "))
	}
	want := "owner|admin|admin|admin maint|admin|admin|maintain writer|admin|admin|write triager|reviewer|reviewer|triage " +
		"reader|publisher||read outsider|publisher||none"
	if strings.Join(got, " ") != want {
		t.Fatalf("audit auth.signin:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
}

// Roles are read again at every sign-in and fixed for the session's life:
// taking the permission away ends the role at the next sign-in, and a role
// granted later does not reach a session that was signed in without it.
func TestRolesAreReadAtEverySignInAndKeptByTheSession(t *testing.T) {
	s := newStack(t)
	token := s.account(5, "dana", "write")
	first := s.signInWith(token)
	if m := first.me(t); strings.Join(m.Roles, ",") != "admin" {
		t.Fatalf("first: %+v", m)
	}

	s.gh.SetRole(adminRepo, "dana", "")
	second := s.signInWith(token)
	if m := second.me(t); len(m.Roles) != 0 || !strings.HasSuffix(m.RoleSource, ": none") {
		t.Fatalf("after the permission was taken away: %+v", m)
	}
	if r := second.get("/api/v1/admin/publishers"); r.status != http.StatusForbidden {
		t.Fatalf("second session on an admin route: %d", r.status)
	}
	if m := first.me(t); strings.Join(m.Roles, ",") != "admin" {
		t.Fatalf("the first session's roles changed under it: %+v", m)
	}

	s.gh.SetRole(adminRepo, "dana", "write")
	third := s.signInWith(token)
	if m := third.me(t); strings.Join(m.Roles, ",") != "admin" {
		t.Fatalf("granted again: %+v", m)
	}
	if m := second.me(t); len(m.Roles) != 0 {
		t.Fatalf("a role granted later reached a session signed in without it: %+v", m)
	}
}

// A fine-grained token the organization has not approved answers 404 like a
// wrong repository: nobody gets a role, and /readyz says why instead of the
// store quietly having no administrator.
func TestATokenThatCannotSeeTheRepositoryFailsClosedAndSaysSo(t *testing.T) {
	s := newStackWith(t, false)
	c := s.signInWithRole(6, "owner", "admin")
	m := c.me(t)
	if len(m.Roles) != 0 || !strings.Contains(m.RoleSource, "not checked (the administration repository is not visible to FAPPS_ADMIN_TOKEN)") {
		t.Fatalf("me = %+v", m)
	}
	if r := c.get("/api/v1/admin/publishers"); r.status != http.StatusForbidden {
		t.Fatalf("admin route: %d", r.status)
	}
	var ready struct {
		Warnings []string `json:"warnings"`
	}
	c.get("/readyz").json(t, &ready)
	if len(ready.Warnings) != 1 || !strings.Contains(ready.Warnings[0], "is not visible to FAPPS_ADMIN_TOKEN") {
		t.Fatalf("readyz warnings = %q", ready.Warnings)
	}
}

func TestAnAnswerWithoutARoleFailsClosed(t *testing.T) {
	s := newStack(t)
	s.gh.AnswerPermission(http.StatusOK, `{"user":{"login":"owner"}}`)
	c := s.signInWithRole(7, "owner", "admin")
	if m := c.me(t); len(m.Roles) != 0 || !strings.Contains(m.RoleSource, "carried no role") {
		t.Fatalf("me = %+v", m)
	}
}

func TestAdminHealthShowsTheRoleCheck(t *testing.T) {
	s := newStack(t)
	admin := s.signInWithRole(8, "boss", "admin")
	var h struct {
		Roles struct {
			Repo            string `json:"repo"`
			TokenConfigured bool   `json:"token_configured"`
			Status          string `json:"status"`
			LastOKAt        string `json:"last_ok_at"`
		} `json:"roles"`
	}
	r := admin.get("/api/v1/admin/health")
	if r.status != http.StatusOK {
		t.Fatalf("admin health: %d %s", r.status, r.body)
	}
	r.json(t, &h)
	if h.Roles.Repo != adminRepo || !h.Roles.TokenConfigured || h.Roles.Status != "ok" || h.Roles.LastOKAt == "" {
		t.Fatalf("health = %s", r.body)
	}
	reviewer := s.signInWithRole(9, "rev", "triage")
	if r := reviewer.get("/api/v1/admin/health"); r.status != http.StatusForbidden {
		t.Fatalf("a reviewer on admin health: %d", r.status)
	}
}

// Q9 with one GitHub account per person: an administrator who is also the
// publisher may approve their own submission (flagged self_review in the
// audit log); a reviewer who is a member of the publisher may not.
func TestFourEyesWithGitHubRoles(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	if _, err := s.pub.EnsureFresh(ctx); err != nil {
		t.Fatal(err)
	}
	alice := s.account(1, "alice", "write")
	s.res.admin = alice
	owner := s.signInWith(alice)
	if m := owner.me(t); strings.Join(m.Roles, ",") != "admin" {
		t.Fatalf("alice: %+v", m)
	}
	if r := owner.change(http.MethodPost, "/api/v1/publishers", map[string]string{"id": "alice-pub", "name": "Alice", "github_owner": "alice"}); r.status != http.StatusCreated {
		t.Fatalf("request publisher: %d %s", r.status, r.body)
	}
	if r := owner.change(http.MethodPatch, "/api/v1/admin/publishers/alice-pub", map[string]any{"status": "active"}); r.status != http.StatusOK {
		t.Fatalf("activate: %d %s", r.status, r.body)
	}
	r := owner.change(http.MethodPost, "/api/v1/submissions", map[string]string{"publisher": "alice-pub", "repo": "alice/filex-hello", "ref": "v1.0.0"})
	if r.status != http.StatusAccepted {
		t.Fatalf("submit: %d %s", r.status, r.body)
	}
	var sub struct{ ID int64 }
	r.json(t, &sub)
	for _, kind := range []string{store.JobResolve, store.JobDryRun} {
		if did, err := s.runner.Step(ctx, kind); !did || err != nil {
			t.Fatalf("%s step: %v %v", kind, did, err)
		}
	}
	approve := "/api/v1/review/submissions/" + strconv.FormatInt(sub.ID, 10) + "/approve"

	rita := s.signInWithRole(2, "rita", "triage")
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO publisher_members (publisher_id, account_id, role)
		SELECT 'alice-pub', i.account_id, 'maintainer' FROM identities i WHERE i.login = 'rita'`); err != nil {
		t.Fatal(err)
	}
	if r := rita.change(http.MethodPost, approve, map[string]any{}); r.status != http.StatusForbidden || r.code(t) != "own_publisher" {
		t.Fatalf("a reviewer approving their own publisher: %d %s", r.status, r.body)
	}
	if r := owner.change(http.MethodPost, approve, map[string]any{"note": "mine"}); r.status != http.StatusOK {
		t.Fatalf("an administrator approving their own publisher: %d %s", r.status, r.body)
	}
	var role string
	var data []byte
	if err := s.pool.QueryRow(ctx, `SELECT actor_role, data FROM audit_log WHERE action = 'submission.approve'`).Scan(&role, &data); err != nil {
		t.Fatal(err)
	}
	if role != "admin" || !strings.Contains(string(data), `"self_review": true`) && !strings.Contains(string(data), `"self_review":true`) {
		t.Fatalf("approve audit: %s %s", role, data)
	}
}

// A Keycloak session left in the table (migration 0005 deletes them; this
// is the belt to its braces) signs nobody in, and a session row cannot
// carry a role the store does not know.
func TestALeftoverKeycloakSessionSignsNobodyIn(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	const cookie = "a-keycloak-session-from-before-the-github-only-sign-in"
	sum := sha256.Sum256([]byte(cookie))
	var acc, ident int64
	if err := s.pool.QueryRow(ctx, `INSERT INTO accounts (display_name) VALUES ('kc') RETURNING id`).Scan(&acc); err != nil {
		t.Fatal(err)
	}
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO identities (account_id, provider, issuer, subject, login, roles)
		VALUES ($1, 'oidc', 'https://auth.example/realms/brf', 'kc-1', 'kc', '{admin}') RETURNING id`, acc).Scan(&ident); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id_hash, account_id, identity_id, csrf_token, expires_at, roles)
		VALUES ($1, $2, $3, 'c', now() + interval '1 hour', '{admin}')`, sum[:], acc, ident); err != nil {
		t.Fatal(err)
	}
	c := s.newClient()
	r := c.do(http.MethodGet, "/api/v1/me", nil, map[string]string{"Cookie": "fapps_session=" + cookie})
	if r.status != http.StatusUnauthorized {
		t.Fatalf("a Keycloak session: %d %s", r.status, r.body)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE sessions SET roles = '{superuser}' WHERE id_hash = $1`, sum[:]); err == nil {
		t.Fatal("a session took an unknown role")
	}
}

// Without an administration repository the lists alone decide, by the
// account's numeric GitHub id; the review and administration routes, the
// admin health and the administrators' notifications all follow.
func TestRolesFromTheListsAlone(t *testing.T) {
	s := newStackOpts(t, stackOpts{seesAdminRepo: true, noAdminRepo: true,
		admins: []config.Member{{ID: 10, Login: "lister"}}, reviewers: []config.Member{{ID: 11}}})
	for _, tc := range []struct {
		id            int64
		login, want   string
		source        string
		review, admin int
		life          time.Duration
	}{
		{10, "lister", "admin", "listed in FAPPS_ADMINS", http.StatusOK, http.StatusOK, 12 * time.Hour},
		{11, "helper", "reviewer", "listed in FAPPS_REVIEWERS", http.StatusOK, http.StatusForbidden, 12 * time.Hour},
		{12, "visitor", "", "not listed in FAPPS_ADMINS or FAPPS_REVIEWERS", http.StatusForbidden, http.StatusForbidden, 7 * 24 * time.Hour},
	} {
		c := s.signInWithRole(tc.id, tc.login, "")
		m := c.me(t)
		if got := strings.Join(m.Roles, ","); got != tc.want || m.RoleSource != tc.source {
			t.Errorf("%s: roles %q source %q, want %q from %q", tc.login, got, m.RoleSource, tc.want, tc.source)
		}
		if r := c.get("/api/v1/review/queue"); r.status != tc.review {
			t.Errorf("%s: review queue %d, want %d", tc.login, r.status, tc.review)
		}
		if r := c.get("/api/v1/admin/publishers"); r.status != tc.admin {
			t.Errorf("%s: admin publishers %d, want %d", tc.login, r.status, tc.admin)
		}
		if got := s.lifetime(tc.login); got != tc.life {
			t.Errorf("%s: session lasts %s, want %s", tc.login, got, tc.life)
		}
		account, err := store.AccountByGitHubLogin(context.Background(), s.pool, tc.login)
		if err != nil {
			t.Fatal(err)
		}
		if admin, known := s.nw.Admins.IsAdmin(context.Background(), account); !known || admin != (tc.want == "admin") {
			t.Errorf("%s: administrators' notifications %v (known %v)", tc.login, admin, known)
		}
	}
	if s.gh.Hits["/repos/"+adminRepo] != 0 {
		t.Fatalf("the repository was asked: %v", s.gh.Hits)
	}
	admin := s.signInWithRole(10, "lister", "")
	r := admin.get("/api/v1/admin/health")
	var h struct {
		Roles struct {
			Repo      string `json:"repo"`
			Status    string `json:"status"`
			Admins    int    `json:"admins"`
			Reviewers int    `json:"reviewers"`
		} `json:"roles"`
	}
	r.json(t, &h)
	if r.status != http.StatusOK || h.Roles.Repo != "" || h.Roles.Status != "ok" || h.Roles.Admins != 1 || h.Roles.Reviewers != 1 {
		t.Fatalf("admin health: %d %s", r.status, r.body)
	}
}

// With both, the session holds the higher role, and the source says both.
func TestTheListsRaiseTheRepositoryRole(t *testing.T) {
	s := newStackOpts(t, stackOpts{seesAdminRepo: true, admins: []config.Member{{ID: 20}}, reviewers: []config.Member{{ID: 21}, {ID: 22}}})
	for _, tc := range []struct {
		id                        int64
		login, role, want, source string
	}{
		{20, "reader", "read", "admin", "github repo permission " + adminRepo + ": read; listed in FAPPS_ADMINS"},
		{21, "outsider", "", "reviewer", "github repo permission " + adminRepo + ": none; listed in FAPPS_REVIEWERS"},
		{22, "writer", "write", "admin", "github repo permission " + adminRepo + ": write; listed in FAPPS_REVIEWERS"},
		{23, "triager", "triage", "reviewer", "github repo permission " + adminRepo + ": triage"},
	} {
		m := s.signInWithRole(tc.id, tc.login, tc.role).me(t)
		if got := strings.Join(m.Roles, ","); got != tc.want || m.RoleSource != tc.source {
			t.Errorf("%s: roles %q source %q, want %q from %q", tc.login, got, m.RoleSource, tc.want, tc.source)
		}
	}
}
