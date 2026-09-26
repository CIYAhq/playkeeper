package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pragma(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRow(`PRAGMA ` + name).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenCreatesAPrivateDatabaseAndMigratesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := Open(path, []string{
		`CREATE TABLE a (x INTEGER PRIMARY KEY); CREATE TABLE b (x INTEGER REFERENCES a (x))`,
		`ALTER TABLE a ADD COLUMN y TEXT`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the database must be readable by its owner only: %v %v", st.Mode(), err)
	}
	if v := pragma(t, db, "user_version"); v != "2" {
		t.Fatalf("user_version %s, want 2", v)
	}
	if _, err := db.Exec(`INSERT INTO a (x, y) VALUES (1, 'one')`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN ('a', 'b')`); n != 2 {
		t.Fatalf("every statement of a migration runs: %d of 2 tables", n)
	}
	if _, err := db.Exec(`INSERT INTO b (x) VALUES (2)`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("foreign keys must be enforced: %v", err)
	}
	if mode := pragma(t, db, "journal_mode"); mode != "wal" {
		t.Fatalf("journal_mode %s, want wal", mode)
	}
}

func TestOpenAppliesOnlyTheMigrationsItHasNotApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	migrations := []string{`CREATE TABLE a (x INTEGER)`, `INSERT INTO a VALUES (1)`}
	db, err := Open(path, migrations)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path, append(migrations, `INSERT INTO a VALUES (2)`))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n := count(t, db, `SELECT count(*) FROM a`); n != 2 {
		t.Fatalf("%d rows, want one from each insert", n)
	}
	if v := pragma(t, db, "user_version"); v != "3" {
		t.Fatalf("user_version %s, want 3", v)
	}
}

func TestAFailedMigrationIsRolledBackAndRunAgainNextTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	first := `CREATE TABLE a (x INTEGER)`
	if _, err := Open(path, []string{first, `CREATE TABLE b (x INTEGER); INSERT INTO missing VALUES (1)`}); err == nil || !strings.Contains(err.Error(), "migration 2") {
		t.Fatalf("want the failed migration named, got %v", err)
	}
	db, err := Open(path, []string{first})
	if err != nil {
		t.Fatal(err)
	}
	if v := pragma(t, db, "user_version"); v != "1" {
		t.Fatalf("user_version %s after a failed migration 2, want 1", v)
	}
	if n := count(t, db, `SELECT count(*) FROM sqlite_master WHERE name = 'b'`); n != 0 {
		t.Fatal("a failed migration must leave nothing behind")
	}
	db.Close()
	db, err = Open(path, []string{first, `CREATE TABLE b (x INTEGER)`})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v := pragma(t, db, "user_version"); v != "2" {
		t.Fatalf("user_version %s, want 2", v)
	}
}

func TestOpenRefusesADatabaseFromANewerPlaykeeper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := Open(path, []string{`CREATE TABLE a (x INTEGER)`, `CREATE TABLE b (x INTEGER)`})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = Open(path, []string{`CREATE TABLE a (x INTEGER)`})
	if err == nil || !strings.Contains(err.Error(), "database schema version 2 is newer than this build supports (1); upgrade Playkeeper") {
		t.Fatalf("want a refusal that says to upgrade, got %v", err)
	}
}
