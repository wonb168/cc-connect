package claudecode

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Cost sources for the reply-footer figure. The CLI's total_cost_usd is only
// meaningful when the upstream bills the Anthropic API directly; behind a
// relay / router (e.g. OmniRoute) the CLI still prices tokens with its own
// Anthropic price table, which can overstate the real cost by an order of
// magnitude. "tokens" lets the operator price the CLI-reported token counts
// instead.
const (
	costSourceCLI    = "cli"    // use the CLI's total_cost_usd (default)
	costSourceTokens = "tokens" // compute from token usage × configured prices
	costSourceNone   = "none"   // never report cost
)

// defaultCNYRate is the FX divisor used for CNY prices when a row does not
// specify one (6 CNY ≈ 1 USD). Everything is normalised to USD internally so
// the reply footer keeps one unit.
const defaultCNYRate = 6

// exchangeRateDivisor returns the divisor that converts a native-currency
// price to USD. A positive explicit rate always wins; otherwise CNY/RMB
// defaults to defaultCNYRate and USD (or anything unknown) to 1.
func exchangeRateDivisor(currency string, rate float64) float64 {
	if rate > 0 {
		return rate
	}
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "CNY", "RMB", "¥", "￥", "YUAN":
		return defaultCNYRate
	default:
		return 1
	}
}

// tokenPrice is a model's price in USD per 1,000,000 tokens.
type tokenPrice struct {
	Input         float64
	Output        float64
	CacheRead     float64
	CacheCreation float64
}

// cost returns the USD cost of the given token counts.
func (p tokenPrice) cost(input, output, cacheRead, cacheCreation int) float64 {
	return (float64(input)*p.Input +
		float64(output)*p.Output +
		float64(cacheRead)*p.CacheRead +
		float64(cacheCreation)*p.CacheCreation) / 1_000_000
}

// costConfig controls how a session's per-turn cost is derived.
type costConfig struct {
	source       string
	defaultModel string                // price fallback when the reported model has no entry
	pricesFile   string                // optional CSV of per-model prices
	prices       map[string]tokenPrice // operator overrides, keys lower-cased; "*" = catch-all
}

// defaultCostConfig returns the upstream-compatible default: trust the CLI.
func defaultCostConfig() costConfig {
	return costConfig{source: costSourceCLI, prices: map[string]tokenPrice{}}
}

// builtinPrices holds DeepSeek's list prices (USD per 1M tokens). They are only
// used when source = "tokens"; operators can override any entry (or add a "*"
// catch-all) via [projects.agent.options.cost.prices].
var builtinPrices = map[string]tokenPrice{
	"deepseek-v4-flash": {Input: 0.22, Output: 0.66, CacheRead: 0.007, CacheCreation: 0.22},
	"deepseek-flash":    {Input: 0.22, Output: 0.66, CacheRead: 0.007, CacheCreation: 0.22},
	"deepseek-v4-pro":   {Input: 0.66, Output: 1.98, CacheRead: 0.022, CacheCreation: 0.66},
	"deepseek-chat":     {Input: 0.28, Output: 0.42, CacheRead: 0.014, CacheCreation: 0.28},
	"deepseek-v3":       {Input: 0.28, Output: 0.42, CacheRead: 0.014, CacheCreation: 0.28},
	"deepseek-v3.2":     {Input: 0.28, Output: 0.42, CacheRead: 0.014, CacheCreation: 0.28},
	"deepseek-reasoner": {Input: 0.55, Output: 2.19, CacheRead: 0.14, CacheCreation: 0.55},
	"deepseek-r1":       {Input: 0.55, Output: 2.19, CacheRead: 0.14, CacheCreation: 0.55},

	"glm-5.3-flash": {Input: 0.075, Output: 0.25, CacheRead: 0.015, CacheCreation: 0.075},
	"glm-5.3":       {Input: 1.2, Output: 5, CacheRead: 0.3, CacheCreation: 1.2},
	"glm-5":         {Input: 1, Output: 3.2, CacheRead: 0.2, CacheCreation: 1},

	// Anthropic first-party USD prices (cache write = 1.25x input).
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.2, CacheCreation: 2.5},
	"claude-sonnet-5":   {Input: 2, Output: 10, CacheRead: 0.2, CacheCreation: 2.5},
	"sonnet":            {Input: 2, Output: 10, CacheRead: 0.2, CacheCreation: 2.5},
}

// parseCostConfig reads [projects.agent.options.cost]:
//
//	[projects.agent.options.cost]
//	source = "tokens"                      # cli (default) | tokens | none
//	default_model = "deepseek-v4-flash"    # fallback price for unmatched models
//	prices_file = "prices.csv"             # optional CSV; relative to the .toml
//
//	[projects.agent.options.cost.prices."auto/best-coding"]
//	input = 0.22
//	output = 0.66
//	cache_read = 0.007
//	cache_creation = 0.22
//
// Inline prices override CSV entries, which override the built-in table.
// Parsing is deliberately lenient: an unknown source, a missing CSV or a
// malformed entry is logged and ignored rather than failing agent startup.
func parseCostConfig(opts map[string]any) costConfig {
	cfg := defaultCostConfig()
	raw, ok := opts["cost"]
	if !ok || raw == nil {
		return cfg
	}
	m, ok := raw.(map[string]any)
	if !ok {
		slog.Warn("claudecode: cost must be a table, ignoring", "type", typeName(raw))
		return cfg
	}

	if s, _ := m["source"].(string); strings.TrimSpace(s) != "" {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case costSourceCLI, "claude", "cli_cost":
			cfg.source = costSourceCLI
		case costSourceTokens, "token", "price", "price_table":
			cfg.source = costSourceTokens
		case costSourceNone, "off", "hidden":
			cfg.source = costSourceNone
		default:
			slog.Warn("claudecode: unknown cost.source, falling back to cli", "source", s)
		}
	}

	if s, _ := m["default_model"].(string); strings.TrimSpace(s) != "" {
		cfg.defaultModel = strings.TrimSpace(s)
	}

	if s, _ := m["prices_file"].(string); strings.TrimSpace(s) != "" {
		path := strings.TrimSpace(s)
		// Relative paths are resolved against the config file's directory
		// (injected by the host as cc_config_dir), so "prices.csv" next to
		// the .toml works regardless of the process working directory.
		if !filepath.IsAbs(path) {
			if base, _ := opts["cc_config_dir"].(string); base != "" {
				path = filepath.Join(base, path)
			}
		}
		cfg.pricesFile = path
		if err := loadPricesCSV(path, cfg.prices); err != nil {
			slog.Warn("claudecode: cost.prices_file partially loaded",
				"path", path, "err", err)
		}
	}

	if prices, ok := m["prices"].(map[string]any); ok {
		for model, v := range prices {
			pm, ok := v.(map[string]any)
			if !ok {
				slog.Warn("claudecode: cost.prices entry must be a table, ignoring", "model", model)
				continue
			}
			key := normalizeModelName(model)
			if key == "" {
				continue
			}
			currency, _ := pm["currency"].(string)
			divisor := exchangeRateDivisor(currency, asFloat(pm["rate"]))
			cfg.prices[key] = tokenPrice{
				Input:         asFloat(pm["input"]) / divisor,
				Output:        asFloat(pm["output"]) / divisor,
				CacheRead:     asFloat(pm["cache_read"]) / divisor,
				CacheCreation: asFloat(pm["cache_creation"]) / divisor,
			}
		}
	}
	return cfg
}

// toOptions renders the parsed config back into the options shape understood
// by New(), so per-workspace agent instances inherit the same cost source.
func (c costConfig) toOptions() map[string]any {
	m := map[string]any{"source": c.source}
	if c.defaultModel != "" {
		m["default_model"] = c.defaultModel
	}
	if c.pricesFile != "" {
		m["prices_file"] = c.pricesFile
	}
	if len(c.prices) > 0 {
		prices := make(map[string]any, len(c.prices))
		for model, p := range c.prices {
			prices[model] = map[string]any{
				"input":          p.Input,
				"output":         p.Output,
				"cache_read":     p.CacheRead,
				"cache_creation": p.CacheCreation,
			}
		}
		m["prices"] = prices
	}
	return m
}

// loadPricesCSV merges a price CSV into dst. The accepted format is:
//
//	model,input,output,cache_read,cache_creation,currency,rate
//	auto/best-coding,1.32,3.96,0.042,1.32,CNY,6
//	claude-sonnet-5-5,2,10,0.2,2.5,USD,1
//	*,0.5,1.5,0,0,USD,1
//
// The header row is optional (positional order is used when absent). Prices are
// in the row's native currency per 1M tokens; CNY/RMB prices are divided by
// rate (default 6) and USD prices by 1, so every stored price is USD. Blank
// cells mean zero and '#' starts a comment line. Valid rows are applied even
// when some rows are malformed; the returned error reports the skipped count.
func loadPricesCSV(path string, dst map[string]tokenPrice) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	r.Comment = '#'
	rows, err := r.ReadAll()
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	columns := map[string]int{
		"model":          0,
		"input":          1,
		"output":         2,
		"cache_read":     3,
		"cache_creation": 4,
		"currency":       5,
		"rate":           6,
	}
	start := 0
	if len(rows[0]) > 0 && strings.EqualFold(strings.TrimSpace(rows[0][0]), "model") {
		columns = make(map[string]int, len(rows[0]))
		for i, name := range rows[0] {
			columns[normalizeColumnName(name)] = i
		}
		start = 1
		if _, ok := columns["model"]; !ok {
			return fmt.Errorf("header has no model column")
		}
	}

	var skipped int
	var firstErr error
	for i, row := range rows[start:] {
		rowNo := start + i + 1
		modelIdx, ok := columns["model"]
		if !ok || modelIdx >= len(row) {
			continue
		}
		key := normalizeModelName(row[modelIdx])
		if key == "" {
			continue
		}

		var p tokenPrice
		var rowErr error
		fields := []struct {
			name   string
			target *float64
		}{
			{"input", &p.Input},
			{"output", &p.Output},
			{"cache_read", &p.CacheRead},
			{"cache_creation", &p.CacheCreation},
		}
		for _, f := range fields {
			idx, ok := columns[f.name]
			if !ok || idx >= len(row) {
				continue
			}
			raw := strings.TrimSpace(row[idx])
			if raw == "" {
				continue
			}
			v, parseErr := strconv.ParseFloat(raw, 64)
			if parseErr != nil {
				rowErr = fmt.Errorf("row %d: %s=%q: %w", rowNo, f.name, raw, parseErr)
				break
			}
			*f.target = v
		}
		if rowErr != nil {
			skipped++
			if firstErr == nil {
				firstErr = rowErr
			}
			continue
		}

		// Normalise the native-currency price to USD. An invalid explicit
		// rate is a row error rather than a silent divide by zero.
		currency := cell(row, columns, "currency")
		var rate float64
		if raw := cell(row, columns, "rate"); raw != "" {
			v, parseErr := strconv.ParseFloat(raw, 64)
			if parseErr != nil || v <= 0 {
				skipped++
				if firstErr == nil {
					firstErr = fmt.Errorf("row %d: rate=%q: must be a positive number", rowNo, raw)
				}
				continue
			}
			rate = v
		}
		divisor := exchangeRateDivisor(currency, rate)
		p.Input /= divisor
		p.Output /= divisor
		p.CacheRead /= divisor
		p.CacheCreation /= divisor
		dst[key] = p
	}
	if skipped > 0 {
		if firstErr != nil {
			return fmt.Errorf("%d row(s) skipped, first: %w", skipped, firstErr)
		}
		return fmt.Errorf("%d row(s) skipped", skipped)
	}
	return nil
}

// cell returns the trimmed value of a named column, or "" when the column or
// cell is absent.
func cell(row []string, columns map[string]int, name string) string {
	idx, ok := columns[name]
	if !ok || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

// normalizeColumnName maps header spellings to canonical column names.
func normalizeColumnName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.ReplaceAll(n, "-", "_")
	n = strings.ReplaceAll(n, " ", "_")
	switch n {
	case "model", "model_id", "model_name":
		return "model"
	case "input", "input_price", "prompt", "input_usd":
		return "input"
	case "output", "output_price", "completion", "output_usd":
		return "output"
	case "cache_read", "cache_read_input", "cached", "cache_read_usd":
		return "cache_read"
	case "cache_creation", "cache_creation_input", "cache_write", "cache_creation_usd":
		return "cache_creation"
	case "currency", "unit", "币种", "货币":
		return "currency"
	case "rate", "fx", "exchange_rate", "汇率":
		return "rate"
	}
	return n
}

// priceFor resolves the price for a reported model id. Order: exact operator
// override, operator catch-all, built-in exact match, operator-configured
// default model, then any DeepSeek model falls back to the v4-flash list price
// (the router commonly reports pool aliases such as "pool-deepseek").
func (c costConfig) priceFor(model string) (tokenPrice, bool) {
	keys := modelLookupKeys(model)
	for _, key := range keys {
		if p, ok := c.prices[key]; ok {
			return p, true
		}
	}
	if p, ok := c.prices["*"]; ok {
		return p, true
	}
	for _, key := range keys {
		if p, ok := builtinPrices[key]; ok {
			return p, true
		}
	}
	if c.defaultModel != "" {
		dk := normalizeModelName(c.defaultModel)
		if p, ok := c.prices[dk]; ok {
			return p, true
		}
		if p, ok := builtinPrices[dk]; ok {
			return p, true
		}
	}
	for _, key := range keys {
		if strings.Contains(key, "deepseek") {
			return builtinPrices["deepseek-v4-flash"], true
		}
	}
	return tokenPrice{}, false
}

// modelLookupKeys returns the candidate price-table keys for a model id: the
// normalized id itself, then its last path segment (so "bigmodel/glm-5.3-flash"
// matches a "glm-5.3-flash" entry).
func modelLookupKeys(model string) []string {
	key := normalizeModelName(model)
	if key == "" {
		return nil
	}
	keys := []string{key}
	if i := strings.LastIndexByte(key, '/'); i >= 0 && i+1 < len(key) {
		keys = append(keys, key[i+1:])
	}
	return keys
}

// turnCostUSD computes the cost of one result event. It prefers the per-model
// modelUsage map (one entry per model that served the turn, already aggregated
// across sub-calls) and falls back to the aggregate usage block priced with
// fallbackModel. Unknown models contribute nothing.
func (c costConfig) turnCostUSD(raw map[string]any, fallbackModel string) float64 {
	if usage, ok := raw["modelUsage"].(map[string]any); ok && len(usage) > 0 {
		var total float64
		matched := false
		for model, v := range usage {
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			p, ok := c.priceFor(model)
			if !ok {
				slog.Debug("claudecode: no price for model, cost contribution is zero", "model", model)
				continue
			}
			matched = true
			total += p.cost(
				int(asFloat(m["inputTokens"])),
				int(asFloat(m["outputTokens"])),
				int(asFloat(m["cacheReadInputTokens"])),
				int(asFloat(m["cacheCreationInputTokens"])),
			)
		}
		if matched {
			return total
		}
	}

	usage, _ := raw["usage"].(map[string]any)
	input, output, cacheCreation, cacheRead := parseClaudeUsage(usage)
	p, ok := c.priceFor(fallbackModel)
	if !ok {
		return 0
	}
	return p.cost(input, output, cacheRead, cacheCreation)
}

// normalizeModelName lower-cases a model id and strips decorations such as the
// "[1m]" context-window suffix Claude Code appends.
func normalizeModelName(model string) string {
	s := strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	}
	return 0
}

func typeName(v any) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprintf("%T", v)
}
