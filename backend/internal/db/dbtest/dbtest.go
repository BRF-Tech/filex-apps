//go:build integration

// Package dbtest gives each integration test its own empty PostgreSQL
// database on the server named by FAPPS_TEST_DATABASE_URL (dropped when the
// test ends), optionally migrated. Tests only.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/migrations"
)

// AdminURL is the test server's URL (a database the tests may create
// others from).
func AdminURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv("FAPPS_TEST_DATABASE_URL")
	if u == "" {
		t.Fatal("FAPPS_TEST_DATABASE_URL is not set: the integration tests need a PostgreSQL 16 server")
	}
	return u
}

// Empty creates a new empty database and returns its URL.
func Empty(t testing.TB) string {
	t.Helper()
	admin := AdminURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "fapps_test_" + hex.EncodeToString(b)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, admin)
		if err != nil {
			return
		}
		defer c.Close(ctx)
		_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// Pool opens a pool on a new empty database.
func Pool(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := Empty(t)
	pool, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, dsn
}

// Migrated opens a pool on a new database with every migration applied.
func Migrated(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, _ := Pool(t)
	if _, err := db.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return pool
}
