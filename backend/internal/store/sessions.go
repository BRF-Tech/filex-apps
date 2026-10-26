package store

import (
	"context"
	"time"
)

// Session is a server-side session joined with the identity it signed in
// with and that identity's account. Roles and RoleSource are the session's
// own: what its sign-in read from GitHub, fixed for its life.
type Session struct {
	IDHash       []byte
	AccountID    int64
	IdentityID   int64
	CSRFToken    string
	GitHubSealed []byte
	CreatedAt    time.Time
	LastSeenAt   time.Time
	ExpiresAt    time.Time

	Roles      []string
	RoleSource string

	Provider    string
	Login       string
	Subject     string
	DisplayName string
	Email       string
}

// InsertSession stores a new session row.
func InsertSession(ctx context.Context, q Q, s Session, ip, userAgent string) error {
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	_, err := q.Exec(ctx, `
		INSERT INTO sessions (id_hash, account_id, identity_id, csrf_token, github_token_sealed, expires_at, ip, user_agent,
		                      roles, role_source)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::inet, NULLIF($8, ''), $9, $10)`,
		s.IDHash, s.AccountID, s.IdentityID, s.CSRFToken, s.GitHubSealed, s.ExpiresAt, ip, userAgent,
		emptyIfNil(s.Roles), s.RoleSource)
	return err
}

// GetSession returns a live session: not expired, its account not disabled.
func GetSession(ctx context.Context, q Q, idHash []byte) (Session, error) {
	var s Session
	err := q.QueryRow(ctx, `
		SELECT s.id_hash, s.account_id, s.identity_id, s.csrf_token, s.github_token_sealed,
		       s.created_at, s.last_seen_at, s.expires_at, s.roles, s.role_source,
		       i.provider, i.login, i.subject, a.display_name, COALESCE(a.email, '')
		FROM sessions s
		JOIN identities i ON i.id = s.identity_id
		JOIN accounts a ON a.id = s.account_id
		WHERE s.id_hash = $1 AND s.expires_at > now() AND a.disabled_at IS NULL`, idHash).
		Scan(&s.IDHash, &s.AccountID, &s.IdentityID, &s.CSRFToken, &s.GitHubSealed,
			&s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.Roles, &s.RoleSource,
			&s.Provider, &s.Login, &s.Subject, &s.DisplayName, &s.Email)
	if err != nil {
		return Session{}, notFound(err)
	}
	return s, nil
}

// TouchSession records activity, at most once a minute per session.
func TouchSession(ctx context.Context, q Q, idHash []byte) error {
	_, err := q.Exec(ctx, `
		UPDATE sessions SET last_seen_at = now()
		WHERE id_hash = $1 AND last_seen_at < now() - interval '1 minute'`, idHash)
	return err
}

// DeleteSession ends one session.
func DeleteSession(ctx context.Context, q Q, idHash []byte) error {
	_, err := q.Exec(ctx, `DELETE FROM sessions WHERE id_hash = $1`, idHash)
	return err
}

// DeleteSessionsOf ends every session of the accounts.
func DeleteSessionsOf(ctx context.Context, q Q, accountIDs []int64) (int64, error) {
	if len(accountIDs) == 0 {
		return 0, nil
	}
	tag, err := q.Exec(ctx, `DELETE FROM sessions WHERE account_id = ANY($1)`, accountIDs)
	return tag.RowsAffected(), err
}

// DeleteExpiredSessions removes sessions past their lifetime.
func DeleteExpiredSessions(ctx context.Context, q Q) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return tag.RowsAffected(), err
}
