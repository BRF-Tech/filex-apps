package db

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/brf-tech/filex-apps/backend/migrations"
)

func TestMigrationsAreTheSQLFilesInNameOrder(t *testing.T) {
	fsys := fstest.MapFS{
		"0002_second.sql": {Data: []byte("SELECT 2;")},
		"0001_first.sql":  {Data: []byte("SELECT 1;")},
		"README.md":       {Data: []byte("not a migration")},
		"0010_tenth.sql":  {Data: []byte("SELECT 10;")},
		"sub/0003.sql":    {Data: []byte("SELECT 3;")},
	}
	ms, err := Migrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.Version)
	}
	want := []string{"0001_first.sql", "0002_second.sql", "0010_tenth.sql"}
	if len(got) != len(want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("versions = %v, want %v", got, want)
		}
	}
	sum := sha256.Sum256([]byte("SELECT 1;"))
	if ms[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %q", ms[0].SHA256)
	}
	if ms[0].SQL != "SELECT 1;" {
		t.Fatalf("sql = %q", ms[0].SQL)
	}
}

func TestPendingSkipsAppliedAndRefusesAChangedFile(t *testing.T) {
	all := []Migration{{Version: "0001.sql", SHA256: "aa"}, {Version: "0002.sql", SHA256: "bb"}, {Version: "0003.sql", SHA256: "cc"}}

	p, err := Pending(all, map[string]string{"0001.sql": "aa", "0002.sql": "BB"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 1 || p[0].Version != "0003.sql" {
		t.Fatalf("pending = %+v, want only 0003.sql", p)
	}

	if _, err := Pending(all, map[string]string{"0001.sql": "zz"}); !errors.Is(err, ErrMigrationChanged) {
		t.Fatalf("changed file: err = %v, want ErrMigrationChanged", err)
	}

	p, err = Pending(all, map[string]string{"0001.sql": "aa", "0099_from_a_newer_binary.sql": "ff"})
	if err != nil || len(p) != 2 {
		t.Fatalf("a newer binary's migration is ignored: pending=%d err=%v", len(p), err)
	}
}

func TestTheEmbeddedMigrationsStartWithTheInitialSchema(t *testing.T) {
	ms, err := Migrations(migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 2 || ms[0].Version != "0001_init.sql" || ms[1].Version != "0002_guards.sql" {
		t.Fatalf("embedded migrations = %+v", ms)
	}
	for _, m := range ms {
		if len(m.SHA256) != 64 || m.SQL == "" {
			t.Fatalf("migration %s: sha %q, %d bytes", m.Version, m.SHA256, len(m.SQL))
		}
	}
}
