package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/auth/adminrepo"
	"github.com/brf-tech/filex-apps/backend/internal/auth/github"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// githubLogin starts a sign-in (everyone signs in with GitHub).
func (h *handler) githubLogin(w http.ResponseWriter, r *http.Request) {
	if !h.limit(w, r, ratelimit.SignIn, clientIP(r)) {
		return
	}
	if h.sessions == nil || !h.GitHub.Configured() {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "GitHub sign-in is not configured on this server.")
		return
	}
	state := session.NewToken()
	if err := h.sessions.StartFlow(w, session.Flow{Provider: "github", State: state,
		ReturnTo: session.SafeReturnTo(r.URL.Query().Get("return_to"))}); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, h.GitHub.AuthCodeURL(state), http.StatusFound)
}

// githubCallback finishes it: the code for a token, the token for the
// account, the account's permission on the administration repository for
// the store roles (internal/auth/adminrepo, read again at every sign-in,
// failing closed), then a session that keeps those roles for its life.
func (h *handler) githubCallback(w http.ResponseWriter, r *http.Request) {
	if h.sessions == nil || !h.GitHub.Configured() {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "GitHub sign-in is not configured on this server.")
		return
	}
	q := r.URL.Query()
	flow, err := h.sessions.FinishFlow(w, r, "github", q.Get("state"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "signin_failed", "The sign-in expired or did not start here; start it again.")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		writeError(w, http.StatusBadRequest, "signin_failed", "GitHub did not complete the sign-in.")
		return
	}
	token, err := h.GitHub.Exchange(r.Context(), q.Get("code"))
	if err != nil {
		h.log.Warn("github sign-in", "rid", requestID(r), "err", err)
		writeError(w, http.StatusBadRequest, "signin_failed", "GitHub did not accept the sign-in code.")
		return
	}
	u, err := h.GitHub.Me(r.Context(), token)
	if err != nil {
		h.log.Warn("github sign-in", "rid", requestID(r), "err", err)
		writeError(w, http.StatusServiceUnavailable, "github_unavailable", "GitHub could not be asked who signed in.")
		return
	}
	roles := h.Roles.Resolve(r.Context(), u.ID, u.Login)
	in := store.SignIn{Provider: "github", Issuer: github.Issuer, Subject: strconv.FormatInt(u.ID, 10),
		Login: u.Login, Email: u.Email, DisplayName: u.Name}
	if !h.signIn(w, r, in, roles, token) {
		return
	}
	http.Redirect(w, r, flow.ReturnTo, http.StatusFound)
}

// signIn records the identity, replaces any session the browser had and
// sets the new one. The audit entry says who signed in, with which roles,
// and why (Q9: the role's source travels with every decision's actor). It
// answers the error itself and reports false.
func (h *handler) signIn(w http.ResponseWriter, r *http.Request, in store.SignIn, roles adminrepo.Result, token string) bool {
	auditRole := audit.RolePublisher
	switch {
	case slices.Contains(roles.Roles, domain.RoleAdmin):
		auditRole = audit.RoleAdmin
	case slices.Contains(roles.Roles, domain.RoleReviewer):
		auditRole = audit.RoleReviewer
	}
	var acc store.Account
	var ident store.Identity
	run := func() error {
		return db.InTx(r.Context(), h.Pool, func(tx pgx.Tx) error {
			var err error
			acc, ident, err = store.SignInIdentity(r.Context(), tx, in)
			if err != nil {
				return err
			}
			id := acc.ID
			_, err = audit.Append(r.Context(), tx, audit.Entry{ActorAccountID: &id, ActorRole: auditRole, Action: "auth.signin",
				TargetType: "account", TargetID: strconv.FormatInt(acc.ID, 10), IP: clientIP(r),
				Data: map[string]any{"provider": in.Provider, "login": in.Login, "roles": roles.RoleNames(), "role_source": roles.Source}})
			return err
		})
	}
	err := run()
	if errors.Is(err, store.ErrSignInRace) {
		err = run()
	}
	if errors.Is(err, store.ErrAccountDisabled) {
		writeError(w, http.StatusForbidden, "account_disabled", "This account is disabled.")
		return false
	}
	if err != nil {
		h.fail(w, r, err)
		return false
	}
	if old, _ := h.principal(r); old != nil {
		_ = store.DeleteSession(r.Context(), h.Pool, old.IDHash)
	}
	if err := h.sessions.Create(r.Context(), w, session.New{AccountID: acc.ID, IdentityID: ident.ID, Roles: roles.Roles,
		RoleSource: roles.Source, GitHubToken: token, IP: clientIP(r), UserAgent: r.UserAgent()}); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}

// logout deletes the session row and clears the cookie (CSRF-checked when
// there is a session).
func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	p, err := h.principal(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if p == nil {
		if h.sessions != nil {
			h.sessions.ClearCookie(w)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !h.sameOrigin(r) || !session.CSRFValid(p, r.Header.Get("X-CSRF-Token")) {
		writeError(w, http.StatusForbidden, "csrf_failed", "The request did not come from the store's own pages.")
		return
	}
	if err := h.sessions.Destroy(context.WithoutCancel(r.Context()), w, p); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
