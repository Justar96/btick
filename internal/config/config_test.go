package config

import "testing"

func TestDatabaseConfigPoolMaxConns(t *testing.T) {
	tests := []struct {
		name       string
		cfg        DatabaseConfig
		wantIngest int32
		wantQuery  int32
	}{
		{
			name:       "defaults",
			cfg:        DatabaseConfig{},
			wantIngest: defaultIngestPoolMaxConns,
			wantQuery:  defaultQueryPoolMaxConns,
		},
		{
			name:       "split explicit",
			cfg:        DatabaseConfig{IngestMaxConns: 10, QueryMaxConns: 6},
			wantIngest: 10,
			wantQuery:  6,
		},
		{
			name:       "derive from total",
			cfg:        DatabaseConfig{MaxConns: 20},
			wantIngest: 12,
			wantQuery:  8,
		},
		{
			name:       "small total",
			cfg:        DatabaseConfig{MaxConns: 2},
			wantIngest: 1,
			wantQuery:  1,
		},
		{
			name:       "single total connection",
			cfg:        DatabaseConfig{MaxConns: 1},
			wantIngest: 1,
			wantQuery:  0,
		},
		{
			name:       "single explicit side",
			cfg:        DatabaseConfig{IngestMaxConns: 14},
			wantIngest: 14,
			wantQuery:  defaultQueryPoolMaxConns,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIngest, gotQuery := tt.cfg.PoolMaxConns()
			if gotIngest != tt.wantIngest || gotQuery != tt.wantQuery {
				t.Fatalf("PoolMaxConns() = (%d, %d), want (%d, %d)",
					gotIngest, gotQuery, tt.wantIngest, tt.wantQuery)
			}
		})
	}
}
