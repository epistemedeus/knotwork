// Package db opens Knotwork's relational store and applies embedded migrations.
//
// SQLite is the default (WAL, busy_timeout=5s) for appliance installs; PostgreSQL
// is selected by a postgres:// URL for team instances. Queries are written with
// `?` placeholders and rebound for PostgreSQL at call time.
package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// TimeLayout is a fixed-width UTC layout so stored timestamps sort
// lexicographically and can be compared in SQL.
const TimeLayout = "2006-01-02T15:04:05.000000000Z"

//go:embed migrations/*.sql
var migrationsFS embed.FS

type DB struct {
	*sql.DB
	Driver string // "sqlite" or "postgres"
	DSN    string
	Path   string // SQLite file path, empty for PostgreSQL
}

// Open connects, pings, and applies pending migrations.
func Open(ctx context.Context, databaseURL string) (*DB, error) {
	driver, dsn, err := parseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		if path := sqlitePath(databaseURL); path != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				return nil, fmt.Errorf("create database directory: %w", err)
			}
		}
	}
	sqlDB, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}
	if driver == "sqlite" {
		// One writer avoids SQLITE_BUSY; WAL still allows concurrent readers.
		sqlDB.SetMaxOpenConns(1)
	} else {
		sqlDB.SetMaxOpenConns(20)
	}
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	d := &DB{DB: sqlDB, Driver: driver, DSN: dsn}
	if driver == "sqlite" {
		d.Path = sqlitePath(databaseURL)
	}
	if err := d.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping %s: %w", driver, err)
	}
	if err := d.migrate(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

func parseURL(raw string) (driver, dsn string, err error) {
	switch {
	case strings.HasPrefix(raw, "postgres://"), strings.HasPrefix(raw, "postgresql://"):
		return "postgres", raw, nil
	case strings.HasPrefix(raw, "sqlite://"):
		return "sqlite", sqliteDSN(strings.TrimPrefix(raw, "sqlite://")), nil
	case strings.HasPrefix(raw, "sqlite:"):
		return "sqlite", sqliteDSN(strings.TrimPrefix(raw, "sqlite:")), nil
	case raw == "":
		return "", "", fmt.Errorf("empty database URL")
	default:
		return "sqlite", sqliteDSN(raw), nil
	}
}

func sqliteDSN(path string) string {
	if path == "" || path == "memory" || path == ":memory:" {
		return "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
	}
	return "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
}

func sqlitePath(raw string) string {
	path := raw
	path = strings.TrimPrefix(path, "sqlite://")
	path = strings.TrimPrefix(path, "sqlite:")
	if path == "memory" || path == ":memory:" {
		return ""
	}
	return path
}

// q rebinds `?` placeholders for PostgreSQL.
func (d *DB) q(query string) string {
	if d.Driver != "postgres" {
		return query
	}
	var b strings.Builder
	n := 1
	for _, r := range query {
		if r == '?' {
			fmt.Fprintf(&b, "$%d", n)
			n++
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists int
		err := d.QueryRowContext(ctx, d.q(`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`), name).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range splitStatements(string(body)) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		if _, err := tx.ExecContext(ctx, d.q(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`), name, Now()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func splitStatements(body string) []string {
	parts := strings.Split(body, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || onlyComments(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func onlyComments(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return false
		}
	}
	return true
}

// Now returns the fixed-width timestamp used for every stored time value.
func Now() string { return time.Now().UTC().Format(TimeLayout) }
