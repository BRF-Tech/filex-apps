package store

import (
	"context"
	"time"
)

// InstallIntent is one install link. The token is never here (its sha256
// is the lookup key); a license key the person added is sealed.
type InstallIntent struct {
	ID                  string // the short id the payload calls token_id
	AccountID           int64
	AppID               int64
	VersionID           int64
	FilexOriginSealed   []byte // the target filex's origin, sealed
	LicenseID           string // "" when none
	LicenseKeySealed    []byte
	CreatedAt           time.Time
	ExpiresAt           time.Time
	CompletedAt         *time.Time
	Result              string
	CompletedInstanceID string
	// Source is where the intent was asked for: IntentSourceWeb (a person on
	// the store's pages) or IntentSourceInstance (a connected filex server,
	// RequestedByInstance its saved instance).
	Source              string
	RequestedByInstance string
}

// Intent sources (install_intents.source, migration 0009).
const (
	IntentSourceWeb      = "web"
	IntentSourceInstance = "instance"
)

const intentCols = `id, account_id, app_id, version_id, filex_origin_sealed, COALESCE(license_id::text, ''), license_key_sealed,
	created_at, expires_at, completed_at, COALESCE(result, ''), COALESCE(completed_instance_id, ''),
	source, COALESCE(requested_by_instance::text, '')`

func scanIntent(row scanner) (InstallIntent, error) {
	var i InstallIntent
	err := row.Scan(&i.ID, &i.AccountID, &i.AppID, &i.VersionID, &i.FilexOriginSealed, &i.LicenseID, &i.LicenseKeySealed,
		&i.CreatedAt, &i.ExpiresAt, &i.CompletedAt, &i.Result, &i.CompletedInstanceID, &i.Source, &i.RequestedByInstance)
	return i, err
}

// InsertIntent stores an intent. ErrConflict when the id or the token hash
// is taken (retry with new random values).
func InsertIntent(ctx context.Context, q Q, i InstallIntent, tokenHash []byte) error {
	source := i.Source
	if source == "" {
		source = IntentSourceWeb
	}
	_, err := q.Exec(ctx, `
		INSERT INTO install_intents (id, token_hash, account_id, app_id, version_id, filex_origin_sealed, license_id,
		                             license_key_sealed, expires_at, source, requested_by_instance)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, '')::uuid, $8, $9, $10, NULLIF($11, '')::uuid)`,
		i.ID, tokenHash, i.AccountID, i.AppID, i.VersionID, i.FilexOriginSealed, i.LicenseID, i.LicenseKeySealed, i.ExpiresAt,
		source, i.RequestedByInstance)
	if err != nil && isUnique(err) {
		return ErrConflict
	}
	return err
}

// IntentByTokenHash finds an intent by its token's sha256.
func IntentByTokenHash(ctx context.Context, q Q, tokenHash []byte) (InstallIntent, error) {
	i, err := scanIntent(q.QueryRow(ctx, `SELECT `+intentCols+` FROM install_intents WHERE token_hash = $1`, tokenHash))
	return i, notFound(err)
}

// LockIntentByTokenHash is IntentByTokenHash FOR UPDATE.
func LockIntentByTokenHash(ctx context.Context, q Q, tokenHash []byte) (InstallIntent, error) {
	i, err := scanIntent(q.QueryRow(ctx, `SELECT `+intentCols+` FROM install_intents WHERE token_hash = $1 FOR UPDATE`, tokenHash))
	return i, notFound(err)
}

// CompleteIntent spends an intent: the outcome is recorded and the sealed
// license key dropped. False when it was already completed.
func CompleteIntent(ctx context.Context, q Q, id, result, instanceID string) (time.Time, bool, error) {
	var at time.Time
	err := q.QueryRow(ctx, `
		UPDATE install_intents SET completed_at = now(), result = $2, completed_instance_id = $3, license_key_sealed = NULL
		WHERE id = $1 AND completed_at IS NULL RETURNING completed_at`, id, result, instanceID).Scan(&at)
	if err = notFound(err); err == ErrNotFound {
		return time.Time{}, false, nil
	}
	return at, err == nil, err
}

// DropExpiredIntentKeys removes the sealed license keys of intents that
// expired unused (they can never be read again).
func DropExpiredIntentKeys(ctx context.Context, q Q) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE install_intents SET license_key_sealed = NULL
		WHERE license_key_sealed IS NOT NULL AND expires_at < now()`)
	return tag.RowsAffected(), err
}

// VersionByID reads one approved version.
func VersionByID(ctx context.Context, q Q, id int64) (Version, error) {
	v, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionCols+` FROM app_versions v WHERE v.id = $1`, id))
	return v, notFound(err)
}
