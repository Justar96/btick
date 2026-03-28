package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitSQLKeepsDOBlockIntact(t *testing.T) {
	sql := `
CREATE TABLE demo (id integer);
DO $$
BEGIN
    PERFORM 1;
    BEGIN
        PERFORM 2;
    EXCEPTION
        WHEN feature_not_supported THEN
            RAISE WARNING 'skip: %', SQLERRM;
    END;
END $$;
SELECT 3;
`

	stmts := splitSQL(sql)
	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(stmts))
	}

	if !strings.Contains(stmts[1], "RAISE WARNING 'skip: %', SQLERRM;") {
		t.Fatalf("expected DO block to remain intact, got %q", stmts[1])
	}
}

func TestMigrationCompressionPoliciesAreGuarded(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", "001_init.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	migration := string(raw)
	if strings.Contains(migration, "timescaledb.compress_bloomfilter") {
		t.Fatal("expected migration to avoid unsupported timescaledb.compress_bloomfilter")
	}

	if strings.Contains(migration, "WITH (timescaledb.continuous, timescaledb.compress = true)") {
		t.Fatal("expected continuous aggregates to enable compression via ALTER MATERIALIZED VIEW only")
	}

	stmts := splitSQL(migration)
	expected := map[string]bool{
		"add_compression_policy('raw_ticks'":           false,
		"add_compression_policy('snapshots_1s'":        false,
		"add_compression_policy('ohlcv_1m'":            false,
		"add_compression_policy('snapshot_rollups_1h'": false,
		"add_compression_policy('snapshot_rollups_1d'": false,
	}

	for _, stmt := range stmts {
		for needle := range expected {
			if !strings.Contains(stmt, needle) {
				continue
			}
			if !strings.Contains(stmt, "EXCEPTION") {
				t.Fatalf("expected guarded compression policy for %s, got %q", needle, stmt)
			}
			expected[needle] = true
		}
	}

	for needle, seen := range expected {
		if !seen {
			t.Fatalf("missing guarded compression policy for %s", needle)
		}
	}
}
