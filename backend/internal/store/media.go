package store

import (
	"context"
	"time"
)

// Media is a mirrored image.
type Media struct {
	SHA256      string
	ContentType string
	Bytes       int64
	Width       int
	Height      int
	SourceURL   string
	Blob        []byte
	CreatedAt   time.Time
}

// PutMedia stores an image by its sha256; the same image twice is one row.
func PutMedia(ctx context.Context, q Q, m Media) error {
	_, err := q.Exec(ctx, `
		INSERT INTO media (sha256, content_type, bytes, width, height, source_url, blob)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (sha256) DO NOTHING`,
		m.SHA256, m.ContentType, int64(len(m.Blob)), m.Width, m.Height, m.SourceURL, m.Blob)
	return err
}

// GetMedia returns an image with its bytes.
func GetMedia(ctx context.Context, q Q, sha string) (Media, error) {
	var m Media
	err := q.QueryRow(ctx, `
		SELECT sha256, content_type, bytes, width, height, source_url, COALESCE(blob, ''::bytea), created_at
		FROM media WHERE sha256 = $1`, sha).
		Scan(&m.SHA256, &m.ContentType, &m.Bytes, &m.Width, &m.Height, &m.SourceURL, &m.Blob, &m.CreatedAt)
	return m, notFound(err)
}

// MediaTypes returns the content type of each known sha256.
func MediaTypes(ctx context.Context, q Q, shas []string) (map[string]string, error) {
	out := map[string]string{}
	if len(shas) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT sha256, content_type FROM media WHERE sha256 = ANY($1)`, shas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s, t string
		if err := rows.Scan(&s, &t); err != nil {
			return nil, err
		}
		out[s] = t
	}
	return out, rows.Err()
}
