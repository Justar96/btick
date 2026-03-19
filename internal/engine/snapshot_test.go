package engine

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/justar9/btc-price-tick/internal/config"
	"github.com/justar9/btc-price-tick/internal/domain"
)

func TestComputeMedian_OddCount(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromFloat(84000.00),
		decimal.NewFromFloat(84100.00),
		decimal.NewFromFloat(84200.00),
	}
	median := computeMedian(prices)
	expected := decimal.NewFromFloat(84100.00)
	if !median.Equal(expected) {
		t.Errorf("expected %s, got %s", expected, median)
	}
}

func TestComputeMedian_EvenCount(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromFloat(84000.00),
		decimal.NewFromFloat(84100.00),
		decimal.NewFromFloat(84200.00),
		decimal.NewFromFloat(84300.00),
	}
	median := computeMedian(prices)
	expected := decimal.NewFromFloat(84150.00)
	if !median.Equal(expected) {
		t.Errorf("expected %s, got %s", expected, median)
	}
}

func TestComputeMedian_SingleValue(t *testing.T) {
	prices := []decimal.Decimal{decimal.NewFromFloat(84000.00)}
	median := computeMedian(prices)
	if !median.Equal(decimal.NewFromFloat(84000.00)) {
		t.Errorf("expected 84000, got %s", median)
	}
}

func TestComputeMedian_Empty(t *testing.T) {
	median := computeMedian(nil)
	if !median.IsZero() {
		t.Errorf("expected zero, got %s", median)
	}
}

func TestComputeMedian_Unsorted(t *testing.T) {
	prices := []decimal.Decimal{
		decimal.NewFromFloat(84300.00),
		decimal.NewFromFloat(84000.00),
		decimal.NewFromFloat(84100.00),
	}
	median := computeMedian(prices)
	expected := decimal.NewFromFloat(84100.00)
	if !median.Equal(expected) {
		t.Errorf("expected %s, got %s", expected, median)
	}
}

func TestSnapshotEngine_ComputeCanonical_MultiVenueMedian(t *testing.T) {
	eng := &SnapshotEngine{}
	eng.cfg.Mode = "multi_venue_median"
	eng.cfg.MinimumHealthySources = 2
	eng.cfg.OutlierRejectPct = 1.0

	refs := []domain.VenueRefPrice{
		{Source: "binance", RefPrice: decimal.NewFromFloat(84100.00), Basis: "trade", AgeMs: 50},
		{Source: "coinbase", RefPrice: decimal.NewFromFloat(84200.00), Basis: "trade", AgeMs: 80},
		{Source: "kraken", RefPrice: decimal.NewFromFloat(84150.00), Basis: "trade", AgeMs: 100},
	}

	price, basis, isDegraded, rejected := eng.computeCanonical(refs)

	if !price.Equal(decimal.NewFromFloat(84150.00)) {
		t.Errorf("expected 84150, got %s", price)
	}
	if basis != "median_trade" {
		t.Errorf("expected median_trade, got %s", basis)
	}
	if isDegraded {
		t.Error("should not be degraded with 3 sources")
	}
	if rejected != 0 {
		t.Errorf("expected 0 rejected, got %d", rejected)
	}
}

func TestSnapshotEngine_ComputeCanonical_SingleVenue(t *testing.T) {
	eng := &SnapshotEngine{}
	eng.cfg.Mode = "multi_venue_median"
	eng.cfg.MinimumHealthySources = 2

	refs := []domain.VenueRefPrice{
		{Source: "binance", RefPrice: decimal.NewFromFloat(84100.00), Basis: "trade"},
	}

	price, basis, isDegraded, _ := eng.computeCanonical(refs)

	if !price.Equal(decimal.NewFromFloat(84100.00)) {
		t.Errorf("expected 84100, got %s", price)
	}
	if basis != "single_trade" {
		t.Errorf("expected single_trade, got %s", basis)
	}
	if !isDegraded {
		t.Error("should be degraded with only 1 source (min 2)")
	}
}

func TestSnapshotEngine_ComputeCanonical_OutlierRejection(t *testing.T) {
	eng := &SnapshotEngine{}
	eng.cfg.Mode = "multi_venue_median"
	eng.cfg.MinimumHealthySources = 2
	eng.cfg.OutlierRejectPct = 1.0 // 1% threshold

	refs := []domain.VenueRefPrice{
		{Source: "binance", RefPrice: decimal.NewFromFloat(84100.00), Basis: "trade"},
		{Source: "coinbase", RefPrice: decimal.NewFromFloat(84150.00), Basis: "trade"},
		{Source: "kraken", RefPrice: decimal.NewFromFloat(90000.00), Basis: "trade"}, // >1% outlier
	}

	price, _, _, rejected := eng.computeCanonical(refs)

	// Kraken should be rejected as outlier (>1% from median of 84150)
	if rejected != 1 {
		t.Errorf("expected 1 rejected outlier, got %d", rejected)
	}
	// Price should be median of remaining two: (84100+84150)/2 = 84125
	expected := decimal.NewFromFloat(84125.00)
	if !price.Equal(expected) {
		t.Errorf("expected %s after outlier rejection, got %s", expected, price)
	}
}

func TestSnapshotEngine_ComputeCanonical_MixedBasis(t *testing.T) {
	eng := &SnapshotEngine{}
	eng.cfg.Mode = "multi_venue_median"
	eng.cfg.MinimumHealthySources = 2
	eng.cfg.OutlierRejectPct = 1.0

	refs := []domain.VenueRefPrice{
		{Source: "binance", RefPrice: decimal.NewFromFloat(84100.00), Basis: "trade"},
		{Source: "coinbase", RefPrice: decimal.NewFromFloat(84150.00), Basis: "midpoint"},
	}

	_, basis, _, _ := eng.computeCanonical(refs)

	if basis != "median_mixed" {
		t.Errorf("expected median_mixed with midpoint fallback, got %s", basis)
	}
}

func TestSnapshotEngine_ComputeCanonical_NoVenues(t *testing.T) {
	eng := &SnapshotEngine{}
	eng.cfg.Mode = "multi_venue_median"

	price, basis, isDegraded, _ := eng.computeCanonical(nil)

	if !price.IsZero() {
		t.Errorf("expected zero price, got %s", price)
	}
	if basis != "none" {
		t.Errorf("expected 'none', got %s", basis)
	}
	if !isDegraded {
		t.Error("should be degraded with no sources")
	}
}

func TestSnapshotEngine_ComputeQuality(t *testing.T) {
	eng := &SnapshotEngine{}
	eng.cfg.CarryForwardMaxSeconds = 10

	// 3 sources, low age
	refs := []domain.VenueRefPrice{
		{Source: "binance", Basis: "trade", AgeMs: 50},
		{Source: "coinbase", Basis: "trade", AgeMs: 80},
		{Source: "kraken", Basis: "trade", AgeMs: 100},
	}
	score := eng.computeQuality(refs, false, 0)
	if score.LessThan(decimal.NewFromFloat(0.8)) {
		t.Errorf("expected high quality score, got %s", score)
	}

	// Stale case
	staleScore := eng.computeQuality(refs, true, 0)
	if staleScore.GreaterThan(decimal.NewFromFloat(0.3)) {
		t.Errorf("stale score should be <=0.3, got %s", staleScore)
	}

	// Stale with decay
	decayScore := eng.computeQuality(refs, true, 5) // half of max
	if decayScore.GreaterThan(staleScore) {
		t.Errorf("decayed score should be less than fresh stale score, got %s vs %s", decayScore, staleScore)
	}
}

func TestSnapshotEngine_VenueRefs_TradeWithinFreshness(t *testing.T) {
	eng := &SnapshotEngine{
		cfg: config.PricingConfig{
			TradeFreshnessWindowMs: 2000,
			QuoteFreshnessWindowMs: 1000,
		},
		venueStates: map[string]*venueState{
			"binance": {
				lastTrade: &tradeInfo{
					price: decimal.NewFromFloat(84100.00),
					ts:    time.Now().Add(-500 * time.Millisecond), // 500ms ago
				},
			},
		},
	}

	refs := eng.computeVenueRefs(time.Now())
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Basis != "trade" {
		t.Errorf("expected trade basis, got %s", refs[0].Basis)
	}
}

func TestSnapshotEngine_VenueRefs_StaleTrade_MidpointFallback(t *testing.T) {
	now := time.Now()
	eng := &SnapshotEngine{
		cfg: config.PricingConfig{
			TradeFreshnessWindowMs: 2000,
			QuoteFreshnessWindowMs: 1000,
		},
		venueStates: map[string]*venueState{
			"binance": {
				lastTrade: &tradeInfo{
					price: decimal.NewFromFloat(84100.00),
					ts:    now.Add(-3 * time.Second), // stale trade
				},
				lastQuote: &quoteInfo{
					bid: decimal.NewFromFloat(84090.00),
					ask: decimal.NewFromFloat(84110.00),
					ts:  now.Add(-500 * time.Millisecond), // fresh quote
				},
			},
		},
	}

	refs := eng.computeVenueRefs(now)
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref (midpoint fallback), got %d", len(refs))
	}
	if refs[0].Basis != "midpoint" {
		t.Errorf("expected midpoint basis, got %s", refs[0].Basis)
	}
	expected := decimal.NewFromFloat(84100.00) // (84090+84110)/2
	if !refs[0].RefPrice.Equal(expected) {
		t.Errorf("expected midpoint %s, got %s", expected, refs[0].RefPrice)
	}
}

func TestSnapshotEngine_VenueRefs_AllStale(t *testing.T) {
	now := time.Now()
	eng := &SnapshotEngine{
		cfg: config.PricingConfig{
			TradeFreshnessWindowMs: 2000,
			QuoteFreshnessWindowMs: 1000,
		},
		venueStates: map[string]*venueState{
			"binance": {
				lastTrade: &tradeInfo{
					price: decimal.NewFromFloat(84100.00),
					ts:    now.Add(-5 * time.Second), // stale
				},
				lastQuote: &quoteInfo{
					bid: decimal.NewFromFloat(84090.00),
					ask: decimal.NewFromFloat(84110.00),
					ts:  now.Add(-3 * time.Second), // also stale
				},
			},
		},
	}

	refs := eng.computeVenueRefs(now)
	if len(refs) != 0 {
		t.Errorf("expected 0 refs (all stale), got %d", len(refs))
	}
}
