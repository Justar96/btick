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

func TestSymbolConfigMetadataDefaults(t *testing.T) {
	cfg := SymbolConfig{Canonical: "BTC/USD"}

	base, quote := cfg.Assets()
	if base != "BTC" || quote != "USD" {
		t.Fatalf("Assets() = (%q, %q), want (%q, %q)", base, quote, "BTC", "USD")
	}
	if got := cfg.EffectiveProductType(); got != "price" {
		t.Fatalf("EffectiveProductType() = %q, want %q", got, "price")
	}
	if got := cfg.EffectiveProductSubType(); got != "reference" {
		t.Fatalf("EffectiveProductSubType() = %q, want %q", got, "reference")
	}
	if got := cfg.EffectiveProductName(); got != "BTC/USD Ref Price" {
		t.Fatalf("EffectiveProductName() = %q, want %q", got, "BTC/USD Ref Price")
	}
	if got := cfg.EffectiveMarketHours(); got != "24/7" {
		t.Fatalf("EffectiveMarketHours() = %q, want %q", got, "24/7")
	}
	if got := cfg.EffectiveFeedID(); got != "btick-refprice-btc-usd" {
		t.Fatalf("EffectiveFeedID() = %q, want %q", got, "btick-refprice-btc-usd")
	}
}

func TestSymbolConfigMetadataOverrides(t *testing.T) {
	cfg := SymbolConfig{
		Canonical:      "BTC/USD",
		BaseAsset:      "BTC_CR",
		QuoteAsset:     "USD_FX",
		ProductType:    "price",
		ProductSubType: "reference",
		ProductName:    "BTC/USD-RefPrice-DS-Premium-Global-003",
		MarketHours:    "24/7/365",
		FeedID:         "feed-btc-usd",
	}

	base, quote := cfg.Assets()
	if base != "BTC_CR" || quote != "USD_FX" {
		t.Fatalf("Assets() = (%q, %q), want (%q, %q)", base, quote, "BTC_CR", "USD_FX")
	}
	if got := cfg.EffectiveProductType(); got != "price" {
		t.Fatalf("EffectiveProductType() = %q, want %q", got, "price")
	}
	if got := cfg.EffectiveProductSubType(); got != "reference" {
		t.Fatalf("EffectiveProductSubType() = %q, want %q", got, "reference")
	}
	if got := cfg.EffectiveProductName(); got != "BTC/USD-RefPrice-DS-Premium-Global-003" {
		t.Fatalf("EffectiveProductName() = %q, want %q", got, "BTC/USD-RefPrice-DS-Premium-Global-003")
	}
	if got := cfg.EffectiveMarketHours(); got != "24/7/365" {
		t.Fatalf("EffectiveMarketHours() = %q, want %q", got, "24/7/365")
	}
	if got := cfg.EffectiveFeedID(); got != "feed-btc-usd" {
		t.Fatalf("EffectiveFeedID() = %q, want %q", got, "feed-btc-usd")
	}
}
