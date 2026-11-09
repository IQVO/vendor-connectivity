//go:build integration

package postgres

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// newMigrator opens the embedded migrations against databaseURL.
func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	return migrate.NewWithSourceInstance("iofs", src, databaseURL)
}

// MigrateTo moves the database at databaseURL to exactly version (up or
// down), for the migration integration tests.
func MigrateTo(databaseURL string, version uint) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Migrate(version); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// StepMigrations applies n migration steps (negative = down).
func StepMigrations(databaseURL string, n int) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	return m.Steps(n)
}

// MigrationVersion returns the applied version and its dirty flag.
func MigrationVersion(databaseURL string) (uint, bool, error) {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return 0, false, err
	}
	defer func() { _, _ = m.Close() }()
	return m.Version()
}
