package core

import (
	"database/sql"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// modelPricing holds per-million-token USD prices for one model pattern.
type modelPricing struct {
	pattern    string // model id substring matched case-insensitively
	input      float64
	output     float64
	cacheWrite float64 // 5m cache write price
	cacheRead  float64
}

var (
	pricingMu     sync.RWMutex
	pricingTable  []modelPricing
	pricingLoaded bool
)

// builtinPricing is the fallback when DB is not available.
// All prices are per million tokens in USD.
var builtinPricing = []modelPricing{
	{"claude-opus-4-8", 5.0, 25.0, 6.25, 0.50},
	{"claude-opus-4-7", 5.0, 25.0, 6.25, 0.50},
	{"claude-opus-4",   15.0, 75.0, 18.75, 1.50},
	{"claude-sonnet-5", 2.0, 10.0, 2.50, 0.20},
	{"claude-sonnet-4", 3.0, 15.0, 3.75, 0.30},
	{"claude-haiku-4",  1.0, 5.0, 1.25, 0.10},
	{"claude-haiku-3",  0.8, 4.0, 1.00, 0.08},
}

// defaultPricing is used when no pattern matches.
var defaultPricing = modelPricing{"", 3.0, 15.0, 3.75, 0.30}

// LoadModelPricingFromDB loads the pricing table from the pricing table in the given DB.
// Called once at startup after the DB connection is established.
func LoadModelPricingFromDB(db *sql.DB) {
	if db == nil {
		return
	}
	rows, err := db.Query(`SELECT model, input, output, cache_write_5m, cache_read FROM pricing ORDER BY model`)
	if err != nil {
		slog.Warn("model_pricing: failed to load from DB", "error", err)
		return
	}
	defer rows.Close()

	var table []modelPricing
	for rows.Next() {
		var p modelPricing
		if err := rows.Scan(&p.pattern, &p.input, &p.output, &p.cacheWrite, &p.cacheRead); err != nil {
			slog.Warn("model_pricing: scan error", "error", err)
			continue
		}
		p.pattern = strings.ToLower(p.pattern)
		table = append(table, p)
	}
	if len(table) == 0 {
		slog.Warn("model_pricing: pricing table is empty, using built-in defaults")
		return
	}
	pricingMu.Lock()
	pricingTable = table
	pricingLoaded = true
	pricingMu.Unlock()
	slog.Info("model_pricing: loaded from DB", "entries", len(table))
}

// RefreshModelPricingFromDB reloads pricing in the background periodically.
func RefreshModelPricingFromDB(db *sql.DB, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			LoadModelPricingFromDB(db)
		}
	}()
}

func lookupPricing(model string) modelPricing {
	m := strings.ToLower(model)
	pricingMu.RLock()
	table := pricingTable
	loaded := pricingLoaded
	pricingMu.RUnlock()

	src := builtinPricing
	if loaded {
		src = table
	}
	// longest (most specific) match wins — iterate in order, track best
	best := defaultPricing
	bestLen := -1
	for _, p := range src {
		if strings.Contains(m, p.pattern) && len(p.pattern) > bestLen {
			best = p
			bestLen = len(p.pattern)
		}
	}
	return best
}

// calcTokenCost returns the USD cost for a given token count.
// tokenType: "input", "output", "cache_write", "cache_read"
// Prices are per million tokens.
func calcTokenCost(model, tokenType string, tokens int) float64 {
	if tokens <= 0 {
		return 0
	}
	p := lookupPricing(model)
	var pricePerM float64
	switch tokenType {
	case "input":
		pricePerM = p.input
	case "output":
		pricePerM = p.output
	case "cache_write":
		pricePerM = p.cacheWrite
	case "cache_read":
		pricePerM = p.cacheRead
	}
	return float64(tokens) / 1_000_000.0 * pricePerM
}
