//go:build integration

package db_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/db/dbtest"
	"github.com/brf-tech/filex-apps/backend/migrations"
)

func count(t *testing.T, q db.Querier, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrationsApplyFromEmptyAndAreIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, _ := dbtest.Pool(t)
	all, _ := db.Migrations(migrations.FS)

	applied, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != len(all) || applied[0] != "0001_init.sql" {
		t.Fatalf("applied %v from empty, want all %d", applied, len(all))
	}
	if n := count(t, pool, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name <> 'schema_migrations'`); n != 33 {
		t.Fatalf("%d tables, want 33 (0006 added licenses, license_activations, install_intents, saved_instances; 0007 app_version_file_types; 0008 app_version_conversions; 0009 instance_connect_codes, instance_request_nonces; 0011 notification_webhooks, push_subscriptions, notification_preferences, notification_events, notification_deliveries, notification_state)", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM schema_migrations`); n != len(all) {
		t.Fatalf("schema_migrations has %d rows", n)
	}

	again, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil || len(again) != 0 {
		t.Fatalf("second run applied %v (%v), want nothing", again, err)
	}
	// The eight of 0001 and storage (0010).
	if n := count(t, pool, `SELECT count(*) FROM categories`); n != 9 {
		t.Fatalf("seed ran twice or not at all: %d categories", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM pg_trigger WHERE tgname IN ('audit_log_no_change', 'app_versions_immutable', 'index_releases_no_change')`); n != 3 {
		t.Fatalf("guard triggers: %d", n)
	}
}

func TestAChangedOrFailingMigrationIsRefused(t *testing.T) {
	ctx := context.Background()
	pool, _ := dbtest.Pool(t)
	v1 := fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE a (x int); CREATE FUNCTION f() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$;")}}
	if _, err := db.Migrate(ctx, pool, v1); err != nil {
		t.Fatal(err)
	}
	edited := fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE a (x bigint);")}}
	if _, err := db.Migrate(ctx, pool, edited); !errors.Is(err, db.ErrMigrationChanged) {
		t.Fatalf("an edited applied file: err = %v, want ErrMigrationChanged", err)
	}

	broken := fstest.MapFS{
		"0001_a.sql": v1["0001_a.sql"],
		"0002_b.sql": {Data: []byte("CREATE TABLE b (y int); INSERT INTO nowhere VALUES (1);")},
	}
	if _, err := db.Migrate(ctx, pool, broken); err == nil {
		t.Fatal("a failing migration succeeded")
	}
	if n := count(t, pool, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'b'`); n != 0 {
		t.Fatal("a failing migration left half its work (it must run in one transaction)")
	}
	if n := count(t, pool, `SELECT count(*) FROM schema_migrations WHERE version = '0002_b.sql'`); n != 0 {
		t.Fatal("a failing migration was recorded")
	}
}

func TestTwoStartingServersMigrateOnce(t *testing.T) {
	ctx := context.Background()
	pool, dsn := dbtest.Pool(t)
	other, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	applied := make([][]string, 2)
	wg.Add(2)
	go func() { defer wg.Done(); applied[0], errs[0] = db.Migrate(ctx, pool, migrations.FS) }()
	go func() { defer wg.Done(); applied[1], errs[1] = db.Migrate(ctx, other, migrations.FS) }()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errors: %v %v", errs[0], errs[1])
	}
	all, _ := db.Migrations(migrations.FS)
	if len(applied[0])+len(applied[1]) != len(all) {
		t.Fatalf("applied %v and %v: each file once in total", applied[0], applied[1])
	}
}
