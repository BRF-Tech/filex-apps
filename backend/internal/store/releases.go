package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Release is one signed index.
type Release struct {
	Serial      int64
	GeneratedAt time.Time
	ExpiresAt   time.Time
	SHA256      string
	Signature   string
	KeyID       string
	Body        []byte
	Reason      string
	CreatedBy   *int64
}

const releaseCols = `serial, generated_at, expires_at, sha256, signature, key_id, body, reason, created_by`

func scanRelease(row scanner) (Release, error) {
	var r Release
	err := row.Scan(&r.Serial, &r.GeneratedAt, &r.ExpiresAt, &r.SHA256, &r.Signature, &r.KeyID, &r.Body, &r.Reason, &r.CreatedBy)
	return r, err
}

// LatestRelease is the newest signed index, nil when there is none yet.
func LatestRelease(ctx context.Context, q Q) (*Release, error) {
	r, err := scanRelease(q.QueryRow(ctx, `SELECT `+releaseCols+` FROM index_releases ORDER BY serial DESC LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LatestSerial is the newest serial, 0 when there is none.
func LatestSerial(ctx context.Context, q Q) (int64, error) {
	var s int64
	err := q.QueryRow(ctx, `SELECT COALESCE(max(serial), 0) FROM index_releases`).Scan(&s)
	return s, err
}

// GetRelease by serial.
func GetRelease(ctx context.Context, q Q, serial int64) (Release, error) {
	r, err := scanRelease(q.QueryRow(ctx, `SELECT `+releaseCols+` FROM index_releases WHERE serial = $1`, serial))
	return r, notFound(err)
}

// InsertRelease stores a signed index.
func InsertRelease(ctx context.Context, q Q, r Release) error {
	_, err := q.Exec(ctx, `
		INSERT INTO index_releases (serial, generated_at, expires_at, sha256, signature, key_id, body, reason, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		r.Serial, r.GeneratedAt, r.ExpiresAt, r.SHA256, r.Signature, r.KeyID, r.Body, r.Reason, r.CreatedBy)
	return err
}

// ListReleases lists releases newest first, without their bodies.
func ListReleases(ctx context.Context, q Q, page Page) ([]Release, int, error) {
	var total int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM index_releases`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.Query(ctx, `
		SELECT serial, generated_at, expires_at, sha256, signature, key_id, ''::bytea, reason, created_by
		FROM index_releases ORDER BY serial DESC LIMIT $1 OFFSET $2`, page.Limit(), page.Offset())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}
