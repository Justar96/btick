package storage

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justar9/btc-price-tick/internal/config"
)

// DB wraps a pgx connection pool.
type DB struct {
	Pool   *pgxpool.Pool
	logger *slog.Logger
}

// New creates a new DB connection pool.
func New(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) (*DB, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	db := &DB{Pool: pool, logger: logger.With("component", "storage")}

	if cfg.RunMigrations {
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
	// Look for migration files in common locations
	migrationPaths := []string{
		"migrations/001_init.sql",
		"../migrations/001_init.sql",
		"../../migrations/001_init.sql",
	}

	var sql []byte
	var err error
	for _, p := range migrationPaths {
		sql, err = os.ReadFile(p)
		if err == nil {
			break
		}
	}
	if err != nil {
		db.logger.Warn("migration file not found, skipping", "error", err)
		return nil
	}

	// Split into individual statements and execute
	stmts := splitStatements(string(sql))
	for _, stmt := range stmts {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			continue
		}
		if _, err := db.Pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("exec migration: %w\nstatement: %s", err, stmt)
		}
	}

	db.logger.Info("migrations applied successfully")
	return nil
}

func splitStatements(sql string) []string {
	// Simple statement splitter on semicolons, respecting comments
	var stmts []string
	var current strings.Builder
	lines := strings.Split(sql, "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")

		if strings.HasSuffix(trimmed, ";") {
			stmts = append(stmts, current.String())
			current.Reset()
		}
	}
	if s := strings.TrimSpace(current.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}
