package config

import (
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SymbolConfig defines a tradable symbol and its exchange sources.
type SymbolConfig struct {
	Canonical      string         `yaml:"canonical"`
	BaseAsset      string         `yaml:"base_asset"`
	QuoteAsset     string         `yaml:"quote_asset"`
	ProductType    string         `yaml:"product_type"`
	ProductSubType string         `yaml:"product_sub_type"`
	ProductName    string         `yaml:"product_name"`
	MarketHours    string         `yaml:"market_hours"`
	FeedID         string         `yaml:"feed_id"`
	Sources        []SourceConfig `yaml:"sources"`
}

func (s SymbolConfig) Assets() (string, string) {
	base := strings.TrimSpace(s.BaseAsset)
	quote := strings.TrimSpace(s.QuoteAsset)
	if base != "" || quote != "" {
		return base, quote
	}
	parts := strings.SplitN(strings.TrimSpace(s.Canonical), "/", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(s.Canonical), ""
}

func (s SymbolConfig) EffectiveProductType() string {
	if productType := strings.TrimSpace(s.ProductType); productType != "" {
		return productType
	}
	return "price"
}

func (s SymbolConfig) EffectiveProductSubType() string {
	if productSubType := strings.TrimSpace(s.ProductSubType); productSubType != "" {
		return productSubType
	}
	return "reference"
}

func (s SymbolConfig) EffectiveProductName() string {
	if productName := strings.TrimSpace(s.ProductName); productName != "" {
		return productName
	}
	return strings.TrimSpace(s.Canonical) + " Ref Price"
}

func (s SymbolConfig) EffectiveMarketHours() string {
	if marketHours := strings.TrimSpace(s.MarketHours); marketHours != "" {
		return marketHours
	}
	return "24/7"
}

func (s SymbolConfig) EffectiveFeedID() string {
	if feedID := strings.TrimSpace(s.FeedID); feedID != "" {
		return feedID
	}
	normalized := strings.ToLower(strings.TrimSpace(s.Canonical))
	normalized = strings.ReplaceAll(normalized, "/", "-")
	normalized = strings.ReplaceAll(normalized, " ", "-")
	return "btick-refprice-" + normalized
}

type Config struct {
	// Multi-symbol configuration (preferred).
	Symbols []SymbolConfig `yaml:"symbols"`

	// Legacy single-symbol fields — migrated into Symbols on load.
	CanonicalSymbol string         `yaml:"canonical_symbol"`
	Sources         []SourceConfig `yaml:"sources"`

	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Pricing  PricingConfig  `yaml:"pricing"`
	Storage  StorageConfig  `yaml:"storage"`
	Health   HealthConfig   `yaml:"health"`
	Access   AccessConfig   `yaml:"access"`
}

type ServerConfig struct {
	HTTPAddr string   `yaml:"http_addr"`
	WSPath   string   `yaml:"ws_path"`
	WS       WSConfig `yaml:"ws"`
}

type AccessConfig struct {
	Enabled           bool   `yaml:"enabled"`
	SignupEnabled     bool   `yaml:"signup_enabled"`
	DefaultSignupTier string `yaml:"default_signup_tier"`
}

func (a AccessConfig) SignupTier() string {
	tier := a.DefaultSignupTier
	if tier == "" {
		return "starter"
	}
	return tier
}

type WSConfig struct {
	SendBufferSize     int `yaml:"send_buffer_size"`
	HeartbeatIntervalS int `yaml:"heartbeat_interval_sec"`
	PingIntervalS      int `yaml:"ping_interval_sec"`
	ReadDeadlineS      int `yaml:"read_deadline_sec"`
	MaxClients         int `yaml:"max_clients"`
	SlowClientMaxDrops int `yaml:"slow_client_max_drops"`
}

func (w WSConfig) SendBuffer() int {
	if w.SendBufferSize <= 0 {
		return 256
	}
	return w.SendBufferSize
}

func (w WSConfig) HeartbeatInterval() time.Duration {
	if w.HeartbeatIntervalS <= 0 {
		return 5 * time.Second
	}
	return time.Duration(w.HeartbeatIntervalS) * time.Second
}

func (w WSConfig) PingInterval() time.Duration {
	if w.PingIntervalS <= 0 {
		return 30 * time.Second
	}
	return time.Duration(w.PingIntervalS) * time.Second
}

func (w WSConfig) ReadDeadline() time.Duration {
	if w.ReadDeadlineS <= 0 {
		return 60 * time.Second
	}
	return time.Duration(w.ReadDeadlineS) * time.Second
}

func (w WSConfig) MaxClientCount() int {
	if w.MaxClients <= 0 {
		return 1000
	}
	return w.MaxClients
}

func (w WSConfig) SlowClientMaxDropCount() int {
	if w.SlowClientMaxDrops <= 0 {
		return 500
	}
	return w.SlowClientMaxDrops
}

type DatabaseConfig struct {
	DSN            string `yaml:"dsn"`
	MaxConns       int32  `yaml:"max_conns"`
	IngestMaxConns int32  `yaml:"ingest_max_conns"`
	QueryMaxConns  int32  `yaml:"query_max_conns"`
	RunMigrations  bool   `yaml:"run_migrations"`
}

const (
	defaultIngestPoolMaxConns int32 = 12
	defaultQueryPoolMaxConns  int32 = 8
)

func (d DatabaseConfig) PoolMaxConns() (int32, int32) {
	if d.IngestMaxConns > 0 || d.QueryMaxConns > 0 {
		ingest := d.IngestMaxConns
		query := d.QueryMaxConns
		if ingest <= 0 {
			ingest = defaultIngestPoolMaxConns
		}
		if query <= 0 {
			query = defaultQueryPoolMaxConns
		}
		return ingest, query
	}

	if d.MaxConns <= 0 {
		return defaultIngestPoolMaxConns, defaultQueryPoolMaxConns
	}

	if d.MaxConns == 1 {
		return 1, 0
	}

	ingest := (d.MaxConns*3 + 4) / 5
	if ingest >= d.MaxConns {
		ingest = d.MaxConns - 1
	}

	query := d.MaxConns - ingest
	if query <= 0 {
		query = 1
		if ingest > 1 {
			ingest--
		}
	}

	return ingest, query
}

type SourceConfig struct {
	Name                  string `yaml:"name"`
	Enabled               bool   `yaml:"enabled"`
	WSURL                 string `yaml:"ws_url"`
	NativeSymbol          string `yaml:"native_symbol"`
	UseBookTickerFallback bool   `yaml:"use_book_ticker_fallback"`
	UseTickerFallback     bool   `yaml:"use_ticker_fallback"`
	PingIntervalSec       int    `yaml:"ping_interval_sec"`
	MaxConnLifetimeSec    int    `yaml:"max_conn_lifetime_sec"`
}

func (s SourceConfig) PingInterval() time.Duration {
	if s.PingIntervalSec <= 0 {
		return 20 * time.Second
	}
	return time.Duration(s.PingIntervalSec) * time.Second
}

func (s SourceConfig) MaxConnLifetime() time.Duration {
	if s.MaxConnLifetimeSec <= 0 {
		return 0 // no limit
	}
	return time.Duration(s.MaxConnLifetimeSec) * time.Second
}

type PricingConfig struct {
	Mode                            string  `yaml:"mode"`
	MinimumHealthySources           int     `yaml:"minimum_healthy_sources"`
	TradeFreshnessWindowMs          int     `yaml:"trade_freshness_window_ms"`
	QuoteFreshnessWindowMs          int     `yaml:"quote_freshness_window_ms"`
	LateArrivalGraceMs              int     `yaml:"late_arrival_grace_ms"`
	OutlierRejectPct                float64 `yaml:"outlier_reject_pct"`
	CarryForwardMaxSeconds          int     `yaml:"carry_forward_max_seconds"`
	SettlementReaggregationWindowMs int     `yaml:"settlement_reaggregation_window_ms"`
}

func (p PricingConfig) SettlementReaggregationWindow() time.Duration {
	if p.SettlementReaggregationWindowMs <= 0 {
		return 5 * time.Second // default 5s
	}
	return time.Duration(p.SettlementReaggregationWindowMs) * time.Millisecond
}

func (p PricingConfig) TradeFreshnessWindow() time.Duration {
	return time.Duration(p.TradeFreshnessWindowMs) * time.Millisecond
}

func (p PricingConfig) QuoteFreshnessWindow() time.Duration {
	return time.Duration(p.QuoteFreshnessWindowMs) * time.Millisecond
}

func (p PricingConfig) LateArrivalGrace() time.Duration {
	return time.Duration(p.LateArrivalGraceMs) * time.Millisecond
}

type StorageConfig struct {
	RawRetentionDays       int `yaml:"raw_retention_days"`
	CanonicalRetentionDays int `yaml:"canonical_retention_days"`
	SnapshotsRetentionDays int `yaml:"snapshots_retention_days"`
	BatchInsertMaxRows     int `yaml:"batch_insert_max_rows"`
	BatchInsertMaxDelayMs  int `yaml:"batch_insert_max_delay_ms"`
}

func (s StorageConfig) BatchInsertMaxDelay() time.Duration {
	return time.Duration(s.BatchInsertMaxDelayMs) * time.Millisecond
}

type HealthConfig struct {
	SourceStaleAfterMs    int `yaml:"source_stale_after_ms"`
	CanonicalStaleAfterMs int `yaml:"canonical_stale_after_ms"`
}

func (h HealthConfig) SourceStaleAfter() time.Duration {
	return time.Duration(h.SourceStaleAfterMs) * time.Millisecond
}

func (h HealthConfig) CanonicalStaleAfter() time.Duration {
	return time.Duration(h.CanonicalStaleAfterMs) * time.Millisecond
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Expand environment variables in config
	expanded := os.ExpandEnv(string(data))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, err
	}
	// Allow DATABASE_URL env var to override config
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		cfg.Database.DSN = dbURL
	}

	// Backward compat: migrate legacy single-symbol config into Symbols slice.
	if len(cfg.Symbols) == 0 && len(cfg.Sources) > 0 {
		canonical := cfg.CanonicalSymbol
		if canonical == "" {
			canonical = "BTC/USD"
		}
		cfg.Symbols = []SymbolConfig{{
			Canonical: canonical,
			Sources:   cfg.Sources,
		}}
	}
	if cfg.Server.HTTPAddr == "" {
		cfg.Server.HTTPAddr = ":8080"
	}
	if cfg.Server.WSPath == "" {
		cfg.Server.WSPath = "/ws/price"
	}
	if cfg.Pricing.MinimumHealthySources == 0 {
		cfg.Pricing.MinimumHealthySources = 2
	}
	if cfg.Pricing.TradeFreshnessWindowMs == 0 {
		cfg.Pricing.TradeFreshnessWindowMs = 2000
	}
	if cfg.Pricing.QuoteFreshnessWindowMs == 0 {
		cfg.Pricing.QuoteFreshnessWindowMs = 1000
	}
	if cfg.Pricing.LateArrivalGraceMs == 0 {
		cfg.Pricing.LateArrivalGraceMs = 250
	}
	if cfg.Pricing.OutlierRejectPct == 0 {
		cfg.Pricing.OutlierRejectPct = 1.0
	}
	if cfg.Pricing.CarryForwardMaxSeconds == 0 {
		cfg.Pricing.CarryForwardMaxSeconds = 10
	}
	if cfg.Storage.CanonicalRetentionDays == 0 {
		cfg.Storage.CanonicalRetentionDays = cfg.Storage.RawRetentionDays
	}
	if cfg.Storage.BatchInsertMaxRows == 0 {
		cfg.Storage.BatchInsertMaxRows = 2000
	}
	if cfg.Storage.BatchInsertMaxDelayMs == 0 {
		cfg.Storage.BatchInsertMaxDelayMs = 100
	}
	if cfg.Health.SourceStaleAfterMs == 0 {
		cfg.Health.SourceStaleAfterMs = 3000
	}
	if cfg.Health.CanonicalStaleAfterMs == 0 {
		cfg.Health.CanonicalStaleAfterMs = 3000
	}
	return &cfg, nil
}
