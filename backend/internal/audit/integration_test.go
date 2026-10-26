//go:build integration

package audit_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/db/dbtest"
)

func appendOne(t *testing.T, pool *pgxpool.Pool, e audit.Entry) audit.Row {
	t.Helper()
	var r audit.Row
	err := db.InTx(context.Background(), pool, func(tx pgx.Tx) error {
		var err error
		r, err = audit.Append(context.Background(), tx, e)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTheChainVerifiesAndShowsTampering(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	for i, e := range []audit.Entry{
		{ActorRole: audit.RoleSystem, Action: "index.publish", TargetType: "index", TargetID: "1"},
		{ActorRole: audit.RoleAdmin, Action: "app.revoke", TargetType: "app", TargetID: "sign", IP: "::ffff:203.0.113.9",
			Data: map[string]any{"reason": "<b>harmful</b> & bad", "n": 3, "nested": map[string]any{"z": 1, "a": []int{1, 2}}}},
		{ActorRole: audit.RoleReviewer, Action: "submission.approve", TargetType: "submission", TargetID: "7", IP: "2001:db8::1"},
	} {
		r := appendOne(t, pool, e)
		if r.ID == 0 || len(r.Hash) != 64 || (i == 0) != (r.PrevHash == "") {
			t.Fatalf("row %d = %+v", i, r)
		}
	}
	if ok, broken, err := audit.Verify(ctx, pool); !ok || broken != 0 || err != nil {
		t.Fatalf("intact chain: ok=%v broken=%d err=%v", ok, broken, err)
	}
	rows, total, err := audit.List(ctx, pool, audit.Filter{Action: "app.revoke"})
	if err != nil || total != 1 || rows[0].IP != "203.0.113.9" || !strings.Contains(string(rows[0].Data), "harmful") {
		t.Fatalf("list = %+v %d %v", rows, total, err)
	}

	// The trigger refuses changes...
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET action = 'app.unrevoke' WHERE id = 2`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update allowed: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log WHERE id = 1`); err == nil {
		t.Fatal("delete allowed")
	}
	// ...and when a superuser goes around it, the chain says so.
	if _, err := pool.Exec(ctx, `ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_change`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_log SET data = '{"reason": "nothing to see"}' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if ok, broken, _ := audit.Verify(ctx, pool); ok || broken != 2 {
		t.Fatalf("rewritten row 2: ok=%v broken=%d", ok, broken)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if ok, broken, _ := audit.Verify(ctx, pool); ok || broken != 3 {
		t.Fatalf("removed row 2: ok=%v broken=%d", ok, broken)
	}
}

func TestConcurrentAppendsKeepOneChain(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				err := db.InTx(ctx, pool, func(tx pgx.Tx) error {
					_, err := audit.Append(ctx, tx, audit.Entry{ActorRole: audit.RoleSystem, Action: "test.append", Data: map[string]any{"w": w, "i": i}})
					return err
				})
				if err != nil {
					t.Error(err)
				}
			}
		}(w)
	}
	wg.Wait()
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n)
	if n != 80 {
		t.Fatalf("%d rows, want 80", n)
	}
	if ok, broken, err := audit.Verify(ctx, pool); !ok || err != nil {
		t.Fatalf("concurrent appends broke the chain at %d: %v", broken, err)
	}
	var forks int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT prev_hash FROM audit_log GROUP BY prev_hash HAVING count(*) > 1) x`).Scan(&forks)
	if forks != 0 {
		t.Fatalf("%d rows share a previous hash (a fork)", forks)
	}
}

func TestARolledBackActionLeavesNoAuditRow(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.Migrated(t)
	_ = db.InTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := audit.Append(ctx, tx, audit.Entry{ActorRole: audit.RoleSystem, Action: "x.y"}); err != nil {
			t.Fatal(err)
		}
		return context.Canceled
	})
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n)
	if n != 0 {
		t.Fatal("the audit row outlived the action it recorded")
	}
	if _, err := audit.Append(ctx, nil, audit.Entry{}); err == nil {
		t.Fatal("an entry without action and role was accepted")
	}
}
