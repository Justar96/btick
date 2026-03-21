package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justar9/btick/internal/config"
)

// DB wraps a pgx connection pool.
type DB struct {
	Pool   *pgxpool.Pool
	logger *slog.Logger
}

// New creates a new DB connection pool.
func New(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) (*DB, error) {
	maxConns, _ := cfg.PoolMaxConns()
	return newDB(ctx, cfg, maxConns, cfg.RunMigrations, logger, "storage")
}

// NewPools creates separate ingest and query pools from the same DSN.
func NewPools(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) (*DB, *DB, error) {
	ingestMaxConns, queryMaxConns := cfg.PoolMaxConns()

	ingestDB, err := newDB(ctx, cfg, ingestMaxConns, cfg.RunMigrations, logger, "storage_ingest")
	if err != nil {
		return nil, nil, err
	}

	if queryMaxConns <= 0 {
		return ingestDB, nil, nil
	}

	queryDB, err := newDB(ctx, cfg, queryMaxConns, false, logger, "storage_query")
	if err != nil {
		ingestDB.Close()
		return nil, nil, err
	}

	return ingestDB, queryDB, nil
}

func newDB(
	ctx context.Context,
	cfg config.DatabaseConfig,
	maxConns int32,
	runMigrations bool,
	logger *slog.Logger,
	component string,
) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	if maxConns > 0 {
		poolCfg.MaxConns = maxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	db := &DB{Pool: pool, logger: logger.With("component", component)}

	if runMigrations {
		if err := db.runMigrations(ctx); err != nil {
			pool.Close()
			return nil, fmt.Errorf("migrations: %w", err)
		}
	}

	return db, nil
}

func (db *DB) Close() {
	db.Pool.Close()
}

func (db *DB) runMigrations(ctx context.Context) error {
	// Look for migration files in common locations.
	migrationGlobs := []string{
		"migrations/*.sql",
		"../migrations/*.sql",
		"../../migrations/*.sql",
	}

	var migrationFiles []string
	seen := make(map[string]struct{})
	for _, pattern := range migrationGlobs {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("glob migrations %q: %w", pattern, err)
		}
		sort.Strings(matches)
		for _, match := range matches {
			absPath, err := filepath.Abs(match)
			if err != nil {
				return fmt.Errorf("resolve migration path %q: %w", match, err)
			}
			if _, ok := seen[absPath]; ok {
				continue
			}
			seen[absPath] = struct{}{}
			migrationFiles = append(migrationFiles, match)
		}
		if len(migrationFiles) > 0 {
			break
		}
	}

	if len(migrationFiles) == 0 {
		db.logger.Warn("migration files not found, skipping")
		return nil
	}

	for _, path := range migrationFiles {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read migration %q: %w", path, err)
		}
		stmts := splitSQL(string(raw))
		for i, stmt := range stmts {
			if _, err := db.Pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("exec migration %q stmt %d: %w", path, i+1, err)
			}
		}
	}

	db.logger.Info("migrations applied successfully", "files", migrationFiles)
	return nil
}

// splitSQL splits a SQL file into individual statements, respecting
// dollar-quoted blocks (DO $$ ... $$;) so they aren't broken apart.
func splitSQL(sql string) []string {
	var stmts []string
	var buf strings.Builder
	inDollar := false
	lines := strings.Split(sql, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			if inDollar {
				buf.WriteString(line)
				buf.WriteByte('\n')
			}
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')

		// Track DO $$ ... END $$; blocks.
		upper := strings.ToUpper(trimmed)
		if !inDollar && strings.Contains(upper, "DO $$") {
			inDollar = true
		}
		if inDollar && strings.Contains(trimmed, "END $$;") {
			inDollar = false
			stmts = append(stmts, strings.TrimSpace(buf.String()))
			buf.Reset()
			continue
		}

		if !inDollar && strings.HasSuffix(trimmed, ";") {
			stmts = append(stmts, strings.TrimSpace(buf.String()))
			buf.Reset()
		}
	}
	if s := strings.TrimSpace(buf.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}
