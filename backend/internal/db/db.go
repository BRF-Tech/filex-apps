// Package db is the store's PostgreSQL layer: the connection pool, the
// migration runner over the embedded migrations/*.sql, transactions and
// advisory locks. Queries per aggregate live in internal/store.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is what both a pool and a transaction answer: every store
// function takes one, so the same query runs alone or inside a caller's
// transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Advisory lock keys. One namespace ("fapp" in ASCII) so they never collide
// with another program's keys on a shared server.
const (
	lockSpace   int64 = 0x66617070 << 16
	LockMigrate       = lockSpace | 1 // the migration runner (session lock)
	LockIndex         = lockSpace | 2 // the index publisher: serial = max + 1
	LockAudit         = lockSpace | 3 // the audit chain: one append at a time
	LockKeys          = lockSpace | 4 // signing key bootstrap
)

// Open connects a pool and checks the server answers.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database url: %w", err)
	}
	if cfg.MaxConns > 10 {
		cfg.MaxConns = 10
	}
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.RuntimeParams["application_name"] = "fapps"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// TxBeginner is a pool (or anything that starts transactions).
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// InTx runs fn in a transaction: committed when fn returns nil, rolled back
// otherwise (and on panic).
func InTx(ctx context.Context, b TxBeginner, fn func(tx pgx.Tx) error) (err error) {
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// XactLock takes a transaction-level advisory lock: held until the
// transaction ends, released by commit or rollback.
func XactLock(ctx context.Context, tx pgx.Tx, key int64) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", key)
	return err
}

// UniqueViolation reports whether err is a unique-constraint violation and
// names the constraint (or index).
func UniqueViolation(err error) (constraint string, ok bool) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return pg.ConstraintName, true
	}
	return "", false
}

// ForeignKeyViolation reports a foreign-key violation (23503).
func ForeignKeyViolation(err error) (constraint string, ok bool) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		return pg.ConstraintName, true
	}
	return "", false
}
