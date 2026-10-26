package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Account is a person.
type Account struct {
	ID          int64
	DisplayName string
	Email       string
	DisabledAt  *time.Time
}

// Identity is one way an account signs in.
type Identity struct {
	ID        int64
	AccountID int64
	Provider  string // "github" (old rows may say "oidc": the removed Keycloak sign-in)
	Issuer    string
	Subject   string
	Login     string
	Email     string
}

// SignIn is what GitHub established about the person signing in. The
// store roles are not the identity's: each session keeps its own
// (sessions.roles, migration 0005).
type SignIn struct {
	Provider    string
	Issuer      string
	Subject     string
	Login       string
	Email       string
	DisplayName string
}

// ErrAccountDisabled is returned for a sign-in to a disabled account.
var ErrAccountDisabled = errors.New("account disabled")

// ErrSignInRace is returned when another request created the same identity
// at the same moment; the caller retries its transaction once.
var ErrSignInRace = errors.New("identity created concurrently")

// SignInIdentity finds the identity (provider, issuer, subject) and
// refreshes what may change (login, e-mail), or creates a new account
// for it. Identities are never linked by e-mail: a new identity is a new
// account. Run it in a transaction.
func SignInIdentity(ctx context.Context, tx pgx.Tx, in SignIn) (Account, Identity, error) {
	var (
		acc Account
		id  Identity
	)
	err := tx.QueryRow(ctx, `
		SELECT i.id, i.account_id, a.display_name, COALESCE(a.email, ''), a.disabled_at
		FROM identities i JOIN accounts a ON a.id = i.account_id
		WHERE i.provider = $1 AND i.issuer = $2 AND i.subject = $3
		FOR UPDATE OF i`, in.Provider, in.Issuer, in.Subject).
		Scan(&id.ID, &id.AccountID, &acc.DisplayName, &acc.Email, &acc.DisabledAt)
	switch {
	case err == nil:
		if acc.DisabledAt != nil {
			return Account{}, Identity{}, ErrAccountDisabled
		}
		if _, err := tx.Exec(ctx, `
			UPDATE identities SET login = $2, email = NULLIF($3, ''), last_login_at = now()
			WHERE id = $1`, id.ID, in.Login, in.Email); err != nil {
			return Account{}, Identity{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET last_login_at = now() WHERE id = $1`, id.AccountID); err != nil {
			return Account{}, Identity{}, err
		}
		acc.ID = id.AccountID
	case errors.Is(err, pgx.ErrNoRows):
		name := in.DisplayName
		if name == "" {
			name = in.Login
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO accounts (display_name, email, last_login_at) VALUES ($1, NULLIF($2, ''), now())
			RETURNING id, display_name, COALESCE(email, '')`, name, in.Email).
			Scan(&acc.ID, &acc.DisplayName, &acc.Email); err != nil {
			return Account{}, Identity{}, err
		}
		err := tx.QueryRow(ctx, `
			INSERT INTO identities (account_id, provider, issuer, subject, login, email, last_login_at)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), now())
			ON CONFLICT (provider, issuer, subject) DO NOTHING
			RETURNING id`, acc.ID, in.Provider, in.Issuer, in.Subject, in.Login, in.Email).Scan(&id.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Account{}, Identity{}, ErrSignInRace
		}
		if err != nil {
			return Account{}, Identity{}, err
		}
		id.AccountID = acc.ID
	default:
		return Account{}, Identity{}, err
	}
	id.Provider, id.Issuer, id.Subject, id.Login, id.Email = in.Provider, in.Issuer, in.Subject, in.Login, in.Email
	return acc, id, nil
}

// GitHubLogin is the account's GitHub login, or "" when it has none.
func GitHubLogin(ctx context.Context, q Q, accountID int64) (string, error) {
	var login string
	err := q.QueryRow(ctx, `
		SELECT login FROM identities WHERE account_id = $1 AND provider = 'github'
		ORDER BY last_login_at DESC NULLS LAST LIMIT 1`, accountID).Scan(&login)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return login, err
}
