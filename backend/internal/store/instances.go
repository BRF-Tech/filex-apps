package store

import (
	"context"
	"time"
)

// SavedInstance is a person's filex instance as the table holds it: the
// address and the label sealed (the caller opens them), url_index a keyed
// hash of the account and the address. A connected instance (migration 0009)
// also holds the public half of the key its filex server signs requests with.
type SavedInstance struct {
	ID          string
	AccountID   int64
	URLIndex    []byte
	URLSealed   []byte
	LabelSealed []byte
	CreatedAt   time.Time
	// ServerKey is the filex's ed25519 public key (32 bytes), nil when the
	// instance is not connected; ServerKeyFingerprint its lower-hex sha256.
	ServerKey            []byte
	ServerKeyFingerprint string
	// FilexInstanceID is the filex's own opaque id, as it reported it.
	FilexInstanceID string
	ConnectedAt     *time.Time
	LastRequestAt   *time.Time
}

// Connected reports whether a filex server holds a key for the instance.
func (s SavedInstance) Connected() bool { return len(s.ServerKey) == 32 && s.ConnectedAt != nil }

const instanceCols = `id::text, account_id, url_index, url_sealed, label_sealed, created_at,
	server_key, COALESCE(server_key_fingerprint, ''), COALESCE(filex_instance_id, ''), connected_at, last_request_at`

func scanInstance(row scanner) (SavedInstance, error) {
	var s SavedInstance
	err := row.Scan(&s.ID, &s.AccountID, &s.URLIndex, &s.URLSealed, &s.LabelSealed, &s.CreatedAt,
		&s.ServerKey, &s.ServerKeyFingerprint, &s.FilexInstanceID, &s.ConnectedAt, &s.LastRequestAt)
	return s, err
}

// NewInstanceID asks the database for a fresh uuid (the sealed fields bind
// to the row's id, so it is known before the insert).
func NewInstanceID(ctx context.Context, q Q) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id)
	return id, err
}

// InsertInstance stores a saved instance. ErrConflict when the account
// already saved that address (same url_index).
func InsertInstance(ctx context.Context, q Q, s SavedInstance) error {
	_, err := q.Exec(ctx, `INSERT INTO saved_instances (id, account_id, url_index, url_sealed, label_sealed)
		VALUES ($1::uuid, $2, $3, $4, $5)`, s.ID, s.AccountID, s.URLIndex, s.URLSealed, s.LabelSealed)
	if err != nil && isUnique(err) {
		return ErrConflict
	}
	return err
}

// InstancesOf lists an account's saved instances, oldest first.
func InstancesOf(ctx context.Context, q Q, accountID int64) ([]SavedInstance, error) {
	rows, err := q.Query(ctx, `SELECT `+instanceCols+` FROM saved_instances WHERE account_id = $1 ORDER BY created_at, id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedInstance{}
	for rows.Next() {
		s, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CountInstances is how many instances an account saved.
func CountInstances(ctx context.Context, q Q, accountID int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM saved_instances WHERE account_id = $1`, accountID).Scan(&n)
	return n, err
}

// InstanceOf reads one of an account's saved instances (ErrNotFound for
// another account's).
func InstanceOf(ctx context.Context, q Q, accountID int64, id string) (SavedInstance, error) {
	s, err := scanInstance(q.QueryRow(ctx, `SELECT `+instanceCols+` FROM saved_instances WHERE account_id = $1 AND id = $2::uuid`, accountID, id))
	return s, notFound(err)
}

// InstanceByID reads a saved instance whoever owns it: what a connected
// filex's signed request is checked against (the request names the
// instance; its key decides).
func InstanceByID(ctx context.Context, q Q, id string) (SavedInstance, error) {
	s, err := scanInstance(q.QueryRow(ctx, `SELECT `+instanceCols+` FROM saved_instances WHERE id = $1::uuid`, id))
	return s, notFound(err)
}

// LockInstance is InstanceByID FOR UPDATE.
func LockInstance(ctx context.Context, q Q, id string) (SavedInstance, error) {
	s, err := scanInstance(q.QueryRow(ctx, `SELECT `+instanceCols+` FROM saved_instances WHERE id = $1::uuid FOR UPDATE`, id))
	return s, notFound(err)
}

// InstanceByIndex finds an account's saved instance by url_index.
func InstanceByIndex(ctx context.Context, q Q, accountID int64, urlIndex []byte) (SavedInstance, error) {
	s, err := scanInstance(q.QueryRow(ctx, `SELECT `+instanceCols+` FROM saved_instances WHERE account_id = $1 AND url_index = $2`, accountID, urlIndex))
	return s, notFound(err)
}

// DeleteInstance forgets one of an account's saved instances (false when
// there was none).
func DeleteInstance(ctx context.Context, q Q, accountID int64, id string) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM saved_instances WHERE account_id = $1 AND id = $2::uuid`, accountID, id)
	return tag.RowsAffected() == 1, err
}

// ---------------------------------------------------------------- connections

// ConnectCode is one connection code: its sha256 is the key, the code is
// never stored.
type ConnectCode struct {
	InstanceID string
	AccountID  int64
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UsedAt     *time.Time
}

// InsertConnectCode stores a new code's hash. ErrConflict when the hash is
// taken (retry with a new code).
func InsertConnectCode(ctx context.Context, q Q, codeHash []byte, instanceID string, accountID int64, expires time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO instance_connect_codes (code_hash, instance_id, account_id, expires_at)
		VALUES ($1, $2::uuid, $3, $4)`, codeHash, instanceID, accountID, expires)
	if err != nil && isUnique(err) {
		return ErrConflict
	}
	return err
}

// LockConnectCode reads a code by its hash, FOR UPDATE.
func LockConnectCode(ctx context.Context, q Q, codeHash []byte) (ConnectCode, error) {
	var c ConnectCode
	err := q.QueryRow(ctx, `SELECT instance_id::text, account_id, created_at, expires_at, used_at
		FROM instance_connect_codes WHERE code_hash = $1 FOR UPDATE`, codeHash).
		Scan(&c.InstanceID, &c.AccountID, &c.CreatedAt, &c.ExpiresAt, &c.UsedAt)
	return c, notFound(err)
}

// SpendConnectCodes marks every unused code of an instance used: the one a
// filex connected with, and any other still waiting (a new connection makes
// them pointless).
func SpendConnectCodes(ctx context.Context, q Q, instanceID string) error {
	_, err := q.Exec(ctx, `UPDATE instance_connect_codes SET used_at = now()
		WHERE instance_id = $1::uuid AND used_at IS NULL`, instanceID)
	return err
}

// DropExpiredConnectCodes removes the codes that expired a day ago or more
// (kept a while so a late paste is told "expired", not "unknown").
func DropExpiredConnectCodes(ctx context.Context, q Q) error {
	_, err := q.Exec(ctx, `DELETE FROM instance_connect_codes WHERE expires_at < now() - interval '1 day'`)
	return err
}

// CountLiveConnectCodes is how many unused, unexpired codes an instance has.
func CountLiveConnectCodes(ctx context.Context, q Q, instanceID string) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM instance_connect_codes
		WHERE instance_id = $1::uuid AND used_at IS NULL AND expires_at > now()`, instanceID).Scan(&n)
	return n, err
}

// SetServerKey connects an instance: its filex's public key, the key's
// fingerprint and the filex's own id. ErrConflict when another instance
// holds that key. The nonces of the key before it are dropped.
func SetServerKey(ctx context.Context, q Q, instanceID string, key []byte, fingerprint, filexInstanceID string) (time.Time, error) {
	var at time.Time
	err := q.QueryRow(ctx, `UPDATE saved_instances
		SET server_key = $2, server_key_fingerprint = $3, filex_instance_id = NULLIF($4, ''),
		    connected_at = now(), last_request_at = NULL
		WHERE id = $1::uuid RETURNING connected_at`, instanceID, key, fingerprint, filexInstanceID).Scan(&at)
	if err != nil {
		if isUnique(err) {
			return time.Time{}, ErrConflict
		}
		return time.Time{}, notFound(err)
	}
	_, err = q.Exec(ctx, `DELETE FROM instance_request_nonces WHERE instance_id = $1::uuid`, instanceID)
	return at, err
}

// ClearServerKey disconnects an instance (false when it was not connected).
func ClearServerKey(ctx context.Context, q Q, instanceID string) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE saved_instances
		SET server_key = NULL, server_key_fingerprint = NULL, filex_instance_id = NULL, connected_at = NULL, last_request_at = NULL
		WHERE id = $1::uuid AND server_key IS NOT NULL`, instanceID)
	if err != nil {
		return false, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM instance_request_nonces WHERE instance_id = $1::uuid`, instanceID); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SeeNonce records a signed request's nonce: false when the instance sent
// it before (a replay). Nonces older than window are pruned on the way.
func SeeNonce(ctx context.Context, q Q, instanceID, nonce string, window time.Duration) (bool, error) {
	if _, err := q.Exec(ctx, `DELETE FROM instance_request_nonces WHERE seen_at < now() - make_interval(secs => $1)`,
		window.Seconds()); err != nil {
		return false, err
	}
	tag, err := q.Exec(ctx, `INSERT INTO instance_request_nonces (instance_id, nonce) VALUES ($1::uuid, $2)
		ON CONFLICT DO NOTHING`, instanceID, nonce)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// TouchInstance records a connected filex's request.
func TouchInstance(ctx context.Context, q Q, instanceID string) error {
	_, err := q.Exec(ctx, `UPDATE saved_instances SET last_request_at = now() WHERE id = $1::uuid`, instanceID)
	return err
}
