package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration is one embedded SQL file.
type Migration struct {
	Version string // the file name, e.g. "0001_init.sql"
	SHA256  string // of the file's bytes
	SQL     string
}

// ErrMigrationChanged means a file that was already applied has different
// bytes now. Applied files are never edited (a change is a new file); the
// runner refuses rather than guess.
var ErrMigrationChanged = errors.New("an applied migration has changed")

// Migrations lists the *.sql files at the root of fsys in name order.
func Migrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		b, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, Migration{Version: e.Name(), SHA256: hex.EncodeToString(sum[:]), SQL: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Pending returns the migrations not yet applied, in order. applied maps a
// version to the sha256 recorded when it ran; a recorded version whose file
// changed is ErrMigrationChanged. Versions recorded but no longer embedded
// (an older binary after a newer one ran) are ignored.
func Pending(all []Migration, applied map[string]string) ([]Migration, error) {
	var out []Migration
	for _, m := range all {
		sum, ok := applied[m.Version]
		if !ok {
			out = append(out, m)
			continue
		}
		if !strings.EqualFold(sum, m.SHA256) {
			return nil, fmt.Errorf("%w: %s (recorded %s, embedded %s)", ErrMigrationChanged, m.Version, short(sum), short(m.SHA256))
		}
	}
	return out, nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// Migrate applies every pending migration of fsys, each in its own
// transaction, and records it in schema_migrations. A session-level
// advisory lock keeps two starting servers from racing. It returns the
// versions it applied (none when the schema is current).
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]string, error) {
	all, err := Migrations(fsys)
	if err != nil {
		return nil, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", LockMigrate); err != nil {
		return nil, err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", LockMigrate)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text        PRIMARY KEY,
		sha256     text        NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, "SELECT version, sha256 FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	applied := map[string]string{}
	for rows.Next() {
		var v, s string
		if err := rows.Scan(&v, &s); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = s
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	pending, err := Pending(all, applied)
	if err != nil {
		return nil, err
	}
	var done []string
	for _, m := range pending {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return done, err
		}
		// No arguments: pgx sends the file with the simple protocol, so a
		// file may hold many statements (and $$-quoted function bodies).
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return done, fmt.Errorf("migration %s: %w", m.Version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, sha256) VALUES ($1, $2)", m.Version, m.SHA256); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return done, fmt.Errorf("migration %s: %w", m.Version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return done, fmt.Errorf("migration %s: %w", m.Version, err)
		}
		done = append(done, m.Version)
	}
	return done, nil
}
