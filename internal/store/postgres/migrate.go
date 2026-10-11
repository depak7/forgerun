package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationLockID is an arbitrary constant; every forgerun process uses the
// same one, so only one of them migrates at a time.
const migrationLockID = 7_413_002_001

// migrate applies every migrations/*.sql file not yet recorded in
// schema_migrations, in filename order, all in one transaction.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files) // 0001_…, 0002_…

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("migrate: taking lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	for _, file := range files {
		version := path.Base(file) // "0001_create_tasks.sql"
		var done bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&done); err != nil {
			return fmt.Errorf("migrate %s: %w", file, err)
		}
		if done {
			continue
		}
		sql, err := migrations.ReadFile(file)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migrate %s: %w", file, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return fmt.Errorf("migrate %s: %w", file, err)
		}
	}
	return tx.Commit(ctx)
}
