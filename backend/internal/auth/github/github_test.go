package github_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/auth/github"
	"github.com/brf-tech/filex-apps/backend/internal/auth/github/githubtest"
)

func setup(t *testing.T) (*githubtest.Server, *github.Provider) {
	t.Helper()
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	return gh, github.New(gh.ClientID, gh.ClientSecret, gh.URL, gh.URL, "https://store.example/auth/github/callback")
}

func TestAuthCodeURLAsksNoScope(t *testing.T) {
	_, p := setup(t)
	u, err := url.Parse(p.AuthCodeURL("the-state"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Path != "/login/oauth/authorize" || q.Get("client_id") != "gh-client" || q.Get("state") != "the-state" ||
		q.Get("redirect_uri") != "https://store.example/auth/github/callback" || q.Get("response_type") != "code" {
		t.Fatalf("authorize URL = %s", u)
	}
	if _, has := q["scope"]; has {
		t.Fatalf("a scope was asked: %q", q.Get("scope"))
	}
	if !p.Configured() || github.New("", "", "x", "x", "x").Configured() || (*github.Provider)(nil).Configured() {
		t.Fatal("Configured")
	}
}

func TestSignInReadsTheAccount(t *testing.T) {
	gh, p := setup(t)
	token := gh.AddUser(4242, "octo", "Octo Cat")
	ctx := context.Background()

	got, err := p.Exchange(ctx, gh.Code(token))
	if err != nil || got != token {
		t.Fatalf("Exchange = %q %v", got, err)
	}
	if _, err := p.Exchange(ctx, "no-such-code"); err == nil {
		t.Fatal("a bad code was exchanged")
	}
	u, err := p.Me(ctx, token)
	if err != nil || u.ID != 4242 || u.Login != "octo" || u.Name != "Octo Cat" {
		t.Fatalf("Me = %+v %v", u, err)
	}
	var se *github.StatusError
	if _, err := p.Me(ctx, "gho_wrong"); !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("Me with a bad token: %v", err)
	}
}

func TestOwner(t *testing.T) {
	gh, p := setup(t)
	alice := gh.AddUser(1, "alice", "Alice")
	gh.AddOrg(77, "Acme")
	ctx := context.Background()

	o, err := p.Owner(ctx, alice, "ACME")
	if err != nil || o.ID != 77 || o.Type != "Organization" {
		t.Fatalf("Owner(ACME) = %+v %v", o, err)
	}
	if o, err := p.Owner(ctx, alice, "alice"); err != nil || o.ID != 1 || o.Type != "User" {
		t.Fatalf("Owner(alice) = %+v %v", o, err)
	}
	if _, err := p.Owner(ctx, alice, "nobody"); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("Owner(nobody): %v", err)
	}
}

// The two reads the role check makes with the server's token, in the shapes
// measured against github.com (Permission's comment).
func TestRepoPermissionAndVisibility(t *testing.T) {
	gh, p := setup(t)
	ctx := context.Background()
	gh.AddUser(5, "triager", "T")
	gh.AddUser(6, "outsider", "O")
	gh.AddRepo("Acme/staff", 77)
	gh.SetRole("Acme/staff", "triager", "triage")
	sees, blind := gh.ServerToken("acme/staff"), gh.ServerToken()

	perm, err := p.RepoPermission(ctx, sees, "Acme", "staff", "triager")
	if err != nil || perm.Permission != "read" || perm.RoleName == nil || *perm.RoleName != "triage" ||
		perm.User == nil || perm.User.ID != 5 || perm.User.Permissions == nil || !perm.User.Permissions.Triage || perm.User.Permissions.Push {
		t.Fatalf("triager = %+v %v", perm, err)
	}
	if perm, err := p.RepoPermission(ctx, sees, "Acme", "staff", "outsider"); err != nil || perm.Permission != "none" || *perm.RoleName != "" {
		t.Fatalf("outsider = %+v %v", perm, err)
	}
	if _, err := p.RepoPermission(ctx, sees, "Acme", "staff", "nobody"); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("no such account: %v", err)
	}
	if _, err := p.RepoPermission(ctx, blind, "Acme", "staff", "triager"); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("a token that does not see the repository: %v", err)
	}
	if err := p.RepoVisible(ctx, sees, "Acme", "staff"); err != nil {
		t.Fatalf("visible: %v", err)
	}
	if err := p.RepoVisible(ctx, blind, "Acme", "staff"); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("blind: %v", err)
	}
	var se *github.StatusError
	if err := p.RepoVisible(ctx, "github_pat_revoked", "Acme", "staff"); !errors.As(err, &se) || se.Status != http.StatusUnauthorized {
		t.Fatalf("revoked token: %v", err)
	}
}
