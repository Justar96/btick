package api

import (
	"net/http"
	"strings"
	"sync"

	"github.com/justar9/btick/internal/config"
)

type SymbolMetadata struct {
	Symbol         string `json:"symbol"`
	BaseAsset      string `json:"base_asset"`
	QuoteAsset     string `json:"quote_asset"`
	ProductType    string `json:"product_type"`
	ProductSubType string `json:"product_sub_type"`
	ProductName    string `json:"product_name"`
	MarketHours    string `json:"market_hours"`
	FeedID         string `json:"feed_id"`
}

type symbolMetadataStore struct {
	mu   sync.RWMutex
	data map[string]SymbolMetadata
}

func BuildSymbolMetadata(symbols []config.SymbolConfig) map[string]SymbolMetadata {
	metadata := make(map[string]SymbolMetadata, len(symbols))
	for _, symbol := range symbols {
		baseAsset, quoteAsset := symbol.Assets()
		metadata[symbol.Canonical] = SymbolMetadata{
			Symbol:         symbol.Canonical,
			BaseAsset:      baseAsset,
			QuoteAsset:     quoteAsset,
			ProductType:    symbol.EffectiveProductType(),
			ProductSubType: symbol.EffectiveProductSubType(),
			ProductName:    symbol.EffectiveProductName(),
			MarketHours:    symbol.EffectiveMarketHours(),
			FeedID:         symbol.EffectiveFeedID(),
		}
	}
	return metadata
}

func deriveSymbolMetadata(symbol string) SymbolMetadata {
	baseAsset := strings.TrimSpace(symbol)
	quoteAsset := ""
	if parts := strings.SplitN(symbol, "/", 2); len(parts) == 2 {
		baseAsset = strings.TrimSpace(parts[0])
		quoteAsset = strings.TrimSpace(parts[1])
	}
	normalized := strings.ToLower(strings.TrimSpace(symbol))
	normalized = strings.ReplaceAll(normalized, "/", "-")
	normalized = strings.ReplaceAll(normalized, " ", "-")
	return SymbolMetadata{
		Symbol:         symbol,
		BaseAsset:      baseAsset,
		QuoteAsset:     quoteAsset,
		ProductType:    "price",
		ProductSubType: "reference",
		ProductName:    symbol + " Ref Price",
		MarketHours:    "24/7",
		FeedID:         "btick-refprice-" + normalized,
	}
}

func (s *Server) SetSymbolMetadata(metadata map[string]SymbolMetadata) {
	s.symbolMetadata.mu.Lock()
	defer s.symbolMetadata.mu.Unlock()
	if metadata == nil {
		s.symbolMetadata.data = nil
		return
	}
	cloned := make(map[string]SymbolMetadata, len(metadata))
	for symbol, entry := range metadata {
		cloned[symbol] = entry
	}
	s.symbolMetadata.data = cloned
}

func (s *Server) symbolMetadataFor(symbol string) (SymbolMetadata, bool) {
	s.symbolMetadata.mu.RLock()
	entry, ok := s.symbolMetadata.data[symbol]
	s.symbolMetadata.mu.RUnlock()
	if ok {
		return entry, true
	}
	for _, configured := range s.engine.Symbols() {
		if configured == symbol {
			return deriveSymbolMetadata(symbol), true
		}
	}
	return SymbolMetadata{}, false
}

func (s *Server) handleMetadata(w http.ResponseWriter, r *http.Request) {
	symbol := s.resolveSymbol(r)
	if symbol == "" {
		http.Error(w, `{"error":"symbol not found"}`, http.StatusNotFound)
		return
	}

	metadata, ok := s.symbolMetadataFor(symbol)
	if !ok {
		http.Error(w, `{"error":"symbol not found"}`, http.StatusNotFound)
		return
	}

	s.writeJSON(w, metadata)
}
