// Package migrate applies versioned SQL migrations at service startup.
//
// ADR-018 retires the informal "idempotent CREATE TABLE IF NOT EXISTS at
// startup" approach for the lineage schema, and requires every schema in
// the platform -- investigation's included -- to adopt the same real
// migration tool from here on. This package is the one place that wraps
// golang-migrate, so every service configures it identically.
package migrate

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Run applies every pending migration to dsn, tracked in a migrations table
// private to this schema. Distinct schemas sharing one Postgres instance
// (ADR-017 section 1.4 allows lineage and investigation to, initially) must
// pass distinct table names, or one schema's migration history would
// collide with the other's.
//
// migrationsFS is the raw result of a caller's own
// `//go:embed migrations/*.sql`: go:embed preserves the "migrations/" path
// component in the resulting fs.FS, and Run looks for files there rather
// than requiring every caller to re-root it with fs.Sub first.
func Run(migrationsFS fs.FS, dsn, migrationsTable string) error {
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrate: source: %w", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("migrate: open: %w", err)
	}
	defer db.Close()

	driver, err := postgres.WithInstance(db, &postgres.Config{MigrationsTable: migrationsTable})
	if err != nil {
		return fmt.Errorf("migrate: driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", source, "pgx", driver)
	if err != nil {
		return fmt.Errorf("migrate: new: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}
