package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
)

// PutFileTypes writes a version's file types (migration 0007), replacing
// the row an older extractor wrote.
func PutFileTypes(ctx context.Context, q Q, versionID int64, s filetypes.Set) error {
	rules := s.Rules
	if rules == nil {
		rules = []filetypes.Rule{}
	}
	b, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO app_version_file_types (version_id, extractor, exts, mimes, any_file, rules)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (version_id) DO UPDATE SET extractor = EXCLUDED.extractor, exts = EXCLUDED.exts,
			mimes = EXCLUDED.mimes, any_file = EXCLUDED.any_file, rules = EXCLUDED.rules, extracted_at = now()`,
		versionID, s.Extractor, s.Exts(), s.Mimes(), s.AnyFile(), json.RawMessage(b))
	return err
}

// FileTypesOf reads the file types of these versions, with the conversions
// their listings declare (migration 0008); a version without a file-types
// row is absent from the map.
func FileTypesOf(ctx context.Context, q Q, versionIDs []int64) (map[int64]filetypes.Set, error) {
	out := map[int64]filetypes.Set{}
	if len(versionIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT f.version_id, f.extractor, f.rules, c.conversions
		FROM app_version_file_types f
		LEFT JOIN app_version_conversions c ON c.version_id = f.version_id
		WHERE f.version_id = ANY($1)`, versionIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var s filetypes.Set
		var raw, convs []byte
		if err := rows.Scan(&id, &s.Extractor, &raw, &convs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &s.Rules); err != nil {
			return nil, fmt.Errorf("file types of version %d: %w", id, err)
		}
		if convs != nil {
			if err := json.Unmarshal(convs, &s.Conversions); err != nil {
				return nil, fmt.Errorf("conversions of version %d: %w", id, err)
			}
		}
		out[id] = s
	}
	return out, rows.Err()
}

// Conversion sources (app_version_conversions.source).
const (
	ConversionsFromListing  = "listing"
	ConversionsFromOperator = "operator"
)

// Conversions is a version's declared conversions and where they come from.
type Conversions struct {
	VersionID   int64
	Conversions []filetypes.Conversion
	Source      string
	Origin      string
	DeclaredAt  time.Time
}

// PutConversions writes a version's declared conversions, replacing what it
// had; none removes them. The caller has held them to the schema and to the
// manifest's engines (internal/consent).
func PutConversions(ctx context.Context, q Q, versionID int64, convs []filetypes.Conversion, source, origin string) error {
	if len(convs) == 0 {
		_, err := q.Exec(ctx, `DELETE FROM app_version_conversions WHERE version_id = $1`, versionID)
		return err
	}
	b, err := json.Marshal(convs)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO app_version_conversions (version_id, conversions, source, origin)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (version_id) DO UPDATE SET conversions = EXCLUDED.conversions, source = EXCLUDED.source,
			origin = EXCLUDED.origin, declared_at = now()`,
		versionID, json.RawMessage(b), source, origin)
	return err
}

// ConversionsOf reads a version's declared conversions; ErrNotFound when it
// declares none.
func ConversionsOf(ctx context.Context, q Q, versionID int64) (Conversions, error) {
	c := Conversions{VersionID: versionID}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT conversions, source, origin, declared_at FROM app_version_conversions WHERE version_id = $1`, versionID).
		Scan(&raw, &c.Source, &c.Origin, &c.DeclaredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c.Conversions); err != nil {
		return c, fmt.Errorf("conversions of version %d: %w", versionID, err)
	}
	return c, nil
}

// BackfillResult says what BackfillFileTypes did.
type BackfillResult struct {
	Extracted int
	// Failed names the versions whose manifest could not be read; they are
	// tried again at the next start.
	Failed []int64
}

// BackfillFileTypes extracts the file types of every approved version that
// has none yet (approved before migration 0007) or whose row an older
// extractor wrote, from the manifest the version keeps. It is idempotent:
// a second run finds nothing to do.
func BackfillFileTypes(ctx context.Context, q Q) (BackfillResult, error) {
	return backfillFileTypes(ctx, q, filetypes.Extractor)
}

// backfillFileTypes is BackfillFileTypes with the current extractor's
// version given (a test plays a newer one).
func backfillFileTypes(ctx context.Context, q Q, extractor int) (BackfillResult, error) {
	var res BackfillResult
	rows, err := q.Query(ctx, `
		SELECT v.id, v.manifest FROM app_versions v
		LEFT JOIN app_version_file_types f ON f.version_id = v.id
		WHERE f.version_id IS NULL OR f.extractor < $1
		ORDER BY v.id`, extractor)
	if err != nil {
		return res, err
	}
	type pending struct {
		id       int64
		manifest []byte
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.manifest); err != nil {
			rows.Close()
			return res, err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, p := range todo {
		s, err := filetypes.Extract(p.manifest)
		if err != nil {
			res.Failed = append(res.Failed, p.id)
			continue
		}
		if err := PutFileTypes(ctx, q, p.id, s); err != nil {
			return res, err
		}
		res.Extracted++
	}
	return res, nil
}
