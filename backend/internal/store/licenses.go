package store

import (
	"context"
	"time"
)

// License is one license of an app. The key is never here: the table keeps
// an HMAC of it (looked up by LockLicenseByHash) and its first 8 characters.
type License struct {
	ID              string
	AppID           int64
	AppName         string
	KeyPrefix       string
	LicenseeName    string
	LicenseeContact string
	Seats           int
	SeatsUsed       int    // activations not released
	ValidUntil      string // YYYY-MM-DD, "" = no end
	UpdatesUntil    string
	Status          string // active | revoked
	Note            string
	CreatedBy       *int64
	CreatedByLogin  string
	CreatedAt       time.Time
	RevokedAt       *time.Time
}

const licenseCols = `l.id::text, l.app_id, a.name, l.key_prefix, l.licensee_name, COALESCE(l.licensee_contact, ''),
	l.seats, (SELECT count(*) FROM license_activations x WHERE x.license_id = l.id AND x.released_at IS NULL),
	COALESCE(to_char(l.valid_until, 'YYYY-MM-DD'), ''), COALESCE(to_char(l.updates_until, 'YYYY-MM-DD'), ''),
	l.status, COALESCE(l.note, ''), l.created_by,
	COALESCE((SELECT i.login FROM identities i WHERE i.account_id = l.created_by AND i.provider = 'github'
	          ORDER BY i.id LIMIT 1), ''),
	l.created_at, l.revoked_at`

const licenseFrom = ` FROM licenses l JOIN apps a ON a.id = l.app_id`

func scanLicense(row scanner) (License, error) {
	var l License
	err := row.Scan(&l.ID, &l.AppID, &l.AppName, &l.KeyPrefix, &l.LicenseeName, &l.LicenseeContact,
		&l.Seats, &l.SeatsUsed, &l.ValidUntil, &l.UpdatesUntil, &l.Status, &l.Note, &l.CreatedBy, &l.CreatedByLogin,
		&l.CreatedAt, &l.RevokedAt)
	return l, err
}

// InsertLicense stores a new license with its key's HMAC. ErrConflict when
// the hash is already taken (a key generated twice: retry with a new one).
func InsertLicense(ctx context.Context, q Q, l License, keyHash []byte) (License, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO licenses (app_id, key_hash, key_prefix, licensee_name, licensee_contact, seats,
		                      valid_until, updates_until, note, created_by)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, NULLIF($7, '')::date, NULLIF($8, '')::date, NULLIF($9, ''), $10)
		RETURNING id::text`,
		l.AppID, keyHash, l.KeyPrefix, l.LicenseeName, l.LicenseeContact, l.Seats, l.ValidUntil, l.UpdatesUntil,
		l.Note, l.CreatedBy).Scan(&id)
	if err != nil {
		if isUnique(err) {
			return License{}, ErrConflict
		}
		return License{}, err
	}
	return GetLicense(ctx, q, id)
}

// GetLicense by id (a uuid in text).
func GetLicense(ctx context.Context, q Q, id string) (License, error) {
	l, err := scanLicense(q.QueryRow(ctx, `SELECT `+licenseCols+licenseFrom+` WHERE l.id = $1::uuid`, id))
	return l, notFound(err)
}

// LockLicense reads a license FOR UPDATE (the activation rules count seats
// under this lock).
func LockLicense(ctx context.Context, q Q, id string) (License, error) {
	l, err := scanLicense(q.QueryRow(ctx, `SELECT `+licenseCols+licenseFrom+` WHERE l.id = $1::uuid FOR UPDATE OF l`, id))
	return l, notFound(err)
}

// LockLicenseByHash finds a license by its key's HMAC, FOR UPDATE.
func LockLicenseByHash(ctx context.Context, q Q, keyHash []byte) (License, error) {
	l, err := scanLicense(q.QueryRow(ctx, `SELECT `+licenseCols+licenseFrom+` WHERE l.key_hash = $1 FOR UPDATE OF l`, keyHash))
	return l, notFound(err)
}

// LicenseByHash finds a license by its key's HMAC (no lock).
func LicenseByHash(ctx context.Context, q Q, keyHash []byte) (License, error) {
	l, err := scanLicense(q.QueryRow(ctx, `SELECT `+licenseCols+licenseFrom+` WHERE l.key_hash = $1`, keyHash))
	return l, notFound(err)
}

// ListLicenses lists an app's licenses, newest first.
func ListLicenses(ctx context.Context, q Q, appID int64, page Page) ([]License, int, error) {
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM licenses WHERE app_id = $1`, appID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `SELECT `+licenseCols+licenseFrom+` WHERE l.app_id = $1
		ORDER BY l.created_at DESC, l.id LIMIT $2 OFFSET $3`, appID, page.Limit(), page.Offset())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []License{}
	for rows.Next() {
		l, err := scanLicense(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// RevokeLicense marks a license revoked (once; false when it already was).
func RevokeLicense(ctx context.Context, q Q, id string, by int64) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE licenses SET status = 'revoked', revoked_at = now(), revoked_by = $2
		WHERE id = $1::uuid AND status = 'active'`, id, by)
	return tag.RowsAffected() == 1, err
}

// Activation is one filex instance that checked a license in.
type Activation struct {
	LicenseID    string
	InstanceID   string
	FirstSeen    time.Time
	LastSeen     time.Time
	ReleasedAt   *time.Time
	FilexVersion string
}

const activationCols = `license_id::text, instance_id, first_seen, last_seen, released_at, COALESCE(filex_version, '')`

func scanActivation(row scanner) (Activation, error) {
	var a Activation
	err := row.Scan(&a.LicenseID, &a.InstanceID, &a.FirstSeen, &a.LastSeen, &a.ReleasedAt, &a.FilexVersion)
	return a, err
}

// Activations lists a license's instances: active ones first, then by
// the last check.
func Activations(ctx context.Context, q Q, licenseID string) ([]Activation, error) {
	rows, err := q.Query(ctx, `SELECT `+activationCols+` FROM license_activations WHERE license_id = $1::uuid
		ORDER BY released_at IS NOT NULL, last_seen DESC, instance_id`, licenseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activation{}
	for rows.Next() {
		a, err := scanActivation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetActivation reads one activation.
func GetActivation(ctx context.Context, q Q, licenseID, instanceID string) (Activation, error) {
	a, err := scanActivation(q.QueryRow(ctx, `SELECT `+activationCols+` FROM license_activations
		WHERE license_id = $1::uuid AND instance_id = $2`, licenseID, instanceID))
	return a, notFound(err)
}

// TouchActivation records a check of an active activation.
func TouchActivation(ctx context.Context, q Q, licenseID, instanceID, filexVersion string) error {
	_, err := q.Exec(ctx, `UPDATE license_activations SET last_seen = now(), filex_version = NULLIF($3, '')
		WHERE license_id = $1::uuid AND instance_id = $2`, licenseID, instanceID, filexVersion)
	return err
}

// Activate takes a seat for an instance: a new row, or a released one made
// active again. Callers check the seats under LockLicense first.
func Activate(ctx context.Context, q Q, licenseID, instanceID, filexVersion string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO license_activations (license_id, instance_id, filex_version) VALUES ($1::uuid, $2, NULLIF($3, ''))
		ON CONFLICT (license_id, instance_id) DO UPDATE
		SET released_at = NULL, last_seen = now(), filex_version = EXCLUDED.filex_version`,
		licenseID, instanceID, filexVersion)
	return err
}

// ReleaseActivation frees an instance's seat (false when it was not active).
func ReleaseActivation(ctx context.Context, q Q, licenseID, instanceID string) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE license_activations SET released_at = now()
		WHERE license_id = $1::uuid AND instance_id = $2 AND released_at IS NULL`, licenseID, instanceID)
	return tag.RowsAffected() == 1, err
}

// SetAppPricing writes an app's price (display only).
func SetAppPricing(ctx context.Context, q Q, appID int64, model, priceText, purchaseURL string) error {
	_, err := q.Exec(ctx, `UPDATE apps SET pricing_model = $2, price_text = NULLIF($3, ''), purchase_url = NULLIF($4, '')
		WHERE id = $1`, appID, model, priceText, purchaseURL)
	return err
}
