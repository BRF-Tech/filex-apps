package store

import (
	"context"
	"time"
)

// SigningKey is the public half of a signing key and its status.
type SigningKey struct {
	ID          string
	Use         string // index | artifact
	PublicHex   string
	Status      string // next | active | retired
	Demo        bool
	CreatedAt   time.Time
	ActivatedAt *time.Time
	RetiredAt   *time.Time
}

const keyCols = `id, use, public_hex, status, demo, created_at, activated_at, retired_at`

func scanKey(row scanner) (SigningKey, error) {
	var k SigningKey
	err := row.Scan(&k.ID, &k.Use, &k.PublicHex, &k.Status, &k.Demo, &k.CreatedAt, &k.ActivatedAt, &k.RetiredAt)
	return k, err
}

// ListKeys lists every key, by use then id.
func ListKeys(ctx context.Context, q Q) ([]SigningKey, error) {
	return listKeys(ctx, q, `SELECT `+keyCols+` FROM signing_keys ORDER BY use, id`)
}

// PublishedKeys are the keys a document shows: active and next ones, and
// retired ones retired after since (kept for one index lifetime).
func PublishedKeys(ctx context.Context, q Q, since time.Time) ([]SigningKey, error) {
	return listKeys(ctx, q, `SELECT `+keyCols+` FROM signing_keys
		WHERE status IN ('active', 'next') OR (status = 'retired' AND retired_at > $1)
		ORDER BY use, id`, since)
}

func listKeys(ctx context.Context, q Q, sql string, args ...any) ([]SigningKey, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SigningKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// GetKey by id.
func GetKey(ctx context.Context, q Q, id string) (SigningKey, error) {
	k, err := scanKey(q.QueryRow(ctx, `SELECT `+keyCols+` FROM signing_keys WHERE id = $1`, id))
	return k, notFound(err)
}

// ActiveKey is the active key of a use (ErrNotFound when none).
func ActiveKey(ctx context.Context, q Q, use string) (SigningKey, error) {
	k, err := scanKey(q.QueryRow(ctx, `SELECT `+keyCols+` FROM signing_keys WHERE use = $1 AND status = 'active'`, use))
	return k, notFound(err)
}

// InsertActiveKey records a key as the active one of its use.
func InsertActiveKey(ctx context.Context, q Q, k SigningKey) error {
	_, err := q.Exec(ctx, `
		INSERT INTO signing_keys (id, use, public_hex, status, demo, activated_at)
		VALUES ($1, $2, $3, 'active', $4, now())`, k.ID, k.Use, k.PublicHex, k.Demo)
	return err
}

// ActivateKey makes a known key the active one of its use.
func ActivateKey(ctx context.Context, q Q, id string) error {
	_, err := q.Exec(ctx, `UPDATE signing_keys SET status = 'active', activated_at = now() WHERE id = $1`, id)
	return err
}

// RetireKey retires a key.
func RetireKey(ctx context.Context, q Q, id string) error {
	_, err := q.Exec(ctx, `UPDATE signing_keys SET status = 'retired', retired_at = now() WHERE id = $1`, id)
	return err
}

// DemoSeed reads a demo key's seed. Callers check APP_ENV first.
func DemoSeed(ctx context.Context, q Q, keyID string) ([]byte, error) {
	var seed []byte
	err := q.QueryRow(ctx, `SELECT seed FROM demo_key_material WHERE key_id = $1`, keyID).Scan(&seed)
	return seed, notFound(err)
}

// InsertDemoSeed stores a demo key's seed.
func InsertDemoSeed(ctx context.Context, q Q, keyID string, seed []byte) error {
	_, err := q.Exec(ctx, `INSERT INTO demo_key_material (key_id, seed) VALUES ($1, $2)`, keyID, seed)
	return err
}
