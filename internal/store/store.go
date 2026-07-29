// Package store provides the database connection and migration runner.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens the DB at dsn using the database/sql driver name (modernc.org/sqlite
// registers as "sqlite"; pgx registers as "postgres"). Pings it, returns
// the *sql.DB.
func Open(ctx context.Context, driver, dsn string) (*sql.DB, error) {
	if err := ensureDBDir(driver, dsn); err != nil {
		return nil, fmt.Errorf("ensure db dir: %w", err)
	}
	db, err := sql.Open(SQLDriverName(driver), dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s: %w", driver, err)
	}
	return db, nil
}

// SQLDriverName maps the operator-facing `APP_DB_DRIVER` value to the
// database/sql driver name. We accept "sqlite3" as an alias for
// modernc.org/sqlite (which registers as "sqlite").
func SQLDriverName(driver string) string {
	switch driver {
	case "sqlite3", "sqlite":
		return "sqlite"
	case "postgres", "pgx":
		return "postgres"
	default:
		return driver
	}
}

// ensureDBDir creates the parent directory of the SQLite database file if
// it doesn't exist. PostgreSQL DSNs don't need this; the function is a
// no-op for them.
func ensureDBDir(driver, dsn string) error {
	if driver != "sqlite3" && driver != "sqlite" {
		return nil
	}
	path := extractSQLitePath(dsn)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

// extractSQLitePath pulls the file path out of a SQLite DSN, accepting
// both the `file:path?params` URI form and the bare `path` form.
func extractSQLitePath(dsn string) string {
	if dsn == "" {
		return ""
	}
	if strings.HasPrefix(dsn, "file:") {
		rest := strings.TrimPrefix(dsn, "file:")
		if i := strings.Index(rest, "?"); i >= 0 {
			rest = rest[:i]
		}
		return rest
	}
	return dsn
}

// Migrate runs all up-migrations embedded under migrations/. Idempotent.
func Migrate(db *sql.DB, driver string) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect(gooseDialect(driver)); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// sqlDriverName maps the operator-facing `APP_DB_DRIVER` value to the
// database/sql driver name. Deprecated: use SQLDriverName (exported)
// instead. Kept as a private alias for any internal callers.
func sqlDriverName(driver string) string { return SQLDriverName(driver) }

// gooseDialect maps to the goose dialect name ("sqlite3" / "postgres").
func gooseDialect(driver string) string {
	switch driver {
	case "sqlite3", "sqlite":
		return "sqlite3"
	case "postgres", "pgx":
		return "postgres"
	default:
		return driver
	}
}
