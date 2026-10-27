package claudecode

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-12
}

func TestParseCostConfigDefaultsToCLI(t *testing.T) {
	cfg := parseCostConfig(nil)
	if cfg.source != costSourceCLI {
		t.Fatalf("source = %q, want %q", cfg.source, costSourceCLI)
	}
	if cfg.prices == nil {
		t.Fatal("prices map is nil")
	}
}

func TestParseCostConfigSources(t *testing.T) {
	cases := map[string]string{
		"cli":         costSourceCLI,
		"tokens":      costSourceTokens,
		"price_table": costSourceTokens,
		"none":        costSourceNone,
		"off":         costSourceNone,
		"bogus":       costSourceCLI, // unknown falls back to the upstream default
	}
	for in, want := range cases {
		cfg := parseCostConfig(map[string]any{"cost": map[string]any{"source": in}})
		if cfg.source != want {
			t.Errorf("source %q => %q, want %q", in, cfg.source, want)
		}
	}
}

func TestParseCostConfigPrices(t *testing.T) {
	cfg := parseCostConfig(map[string]any{
		"cost": map[string]any{
			"source":        "tokens",
			"default_model": "deepseek-v4-flash",
			"prices": map[string]any{
				"Auto/Best-Coding": map[string]any{
					"input":          0.5,      // float64 (TOML decoder)
					"output":         int64(1), // int64
					"cache_read":     0.01,
					"cache_creation": 0.6,
				},
			},
		},
	})
	if cfg.source != costSourceTokens || cfg.defaultModel != "deepseek-v4-flash" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	p, ok := cfg.prices["auto/best-coding"]
	if !ok {
		t.Fatalf("model key was not normalized: %#v", cfg.prices)
	}
	if !almostEqual(p.Input, 0.5) || !almostEqual(p.Output, 1) ||
		!almostEqual(p.CacheRead, 0.01) || !almostEqual(p.CacheCreation, 0.6) {
		t.Fatalf("parsed price = %+v", p)
	}
}

func TestParseCostConfigPricesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.csv")
	content := "# USD per 1M tokens\n" +
		"model,input,output,cache_read,cache_creation\n" +
		"auto/best-coding,0.22,0.66,0.007,0.22\n" +
		"deepseek-v4-pro,0.66,1.98,0.022,0.66\n" +
		"*,0.5,1.5,0,0\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := parseCostConfig(map[string]any{
		"cost": map[string]any{
			"source":      "tokens",
			"prices_file": path,
		},
	})
	if cfg.pricesFile != path {
		t.Fatalf("pricesFile = %q, want %q", cfg.pricesFile, path)
	}
	if p := cfg.prices["auto/best-coding"]; !almostEqual(p.Input, 0.22) || !almostEqual(p.Output, 0.66) {
		t.Errorf("auto/best-coding = %+v", p)
	}
	if p := cfg.prices["deepseek-v4-pro"]; !almostEqual(p.CacheRead, 0.022) {
		t.Errorf("deepseek-v4-pro = %+v", p)
	}
	if p, ok := cfg.prices["*"]; !ok || !almostEqual(p.Input, 0.5) {
		t.Errorf("catch-all = %+v ok=%v", p, ok)
	}
}

func TestLoadPricesCSVCurrencyRate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.csv")
	content := "model,input,output,cache_read,cache_creation,currency,rate\n" +
		"cn,1.32,3.96,0.042,1.32,CNY,6\n" +
		"cn-default,1.32,3.96,0.042,1.32,CNY,\n" +
		"usd,2,10,0.2,2.5,USD,1\n" +
		"cn-rate7,7,14,0,0,CNY,7\n" +
		"cny-norate,6,12,0,0,CNY,\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	dst := map[string]tokenPrice{}
	if err := loadPricesCSV(path, dst); err != nil {
		t.Fatalf("loadPricesCSV: %v", err)
	}
	if p := dst["cn"]; !almostEqual(p.Input, 0.22) || !almostEqual(p.Output, 0.66) {
		t.Errorf("CNY/6 row = %+v, want 0.22/0.66", p)
	}
	if p := dst["cn-default"]; !almostEqual(p.Input, 0.22) {
		t.Errorf("CNY without rate = %+v, want /6", p)
	}
	if p := dst["usd"]; !almostEqual(p.Input, 2) || !almostEqual(p.CacheCreation, 2.5) {
		t.Errorf("USD row = %+v, want unchanged", p)
	}
	if p := dst["cn-rate7"]; !almostEqual(p.Input, 1) || !almostEqual(p.Output, 2) {
		t.Errorf("explicit rate 7 = %+v, want /7", p)
	}
	if p := dst["cny-norate"]; !almostEqual(p.Input, 1) {
		t.Errorf("CNY default rate = %+v, want /6", p)
	}
}

func TestParseCostConfigInlineCurrency(t *testing.T) {
	cfg := parseCostConfig(map[string]any{
		"cost": map[string]any{
			"source": "tokens",
			"prices": map[string]any{
				"cn-model": map[string]any{
					"input": 6, "output": 12, "currency": "CNY", "rate": 6,
				},
			},
		},
	})
	p := cfg.prices["cn-model"]
	if !almostEqual(p.Input, 1) || !almostEqual(p.Output, 2) {
		t.Fatalf("inline CNY price = %+v, want /6", p)
	}
}

func TestParseCostConfigRelativePricesFile(t *testing.T) {
	dir := t.TempDir()
	content := "model,input,output\nauto/best-coding,0.22,0.66\n"
	if err := os.WriteFile(filepath.Join(dir, "prices.csv"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := parseCostConfig(map[string]any{
		"cc_config_dir": dir,
		"cost": map[string]any{
			"source":      "tokens",
			"prices_file": "prices.csv",
		},
	})
	if want := filepath.Join(dir, "prices.csv"); cfg.pricesFile != want {
		t.Fatalf("pricesFile = %q, want %q", cfg.pricesFile, want)
	}
	if p, ok := cfg.prices["auto/best-coding"]; !ok || !almostEqual(p.Output, 0.66) {
		t.Fatalf("relative CSV not loaded: %+v ok=%v", p, ok)
	}
}

func TestCostConfigToOptionsRoundTrip(t *testing.T) {
	cfg := costConfig{
		source:       costSourceTokens,
		defaultModel: "deepseek-v4-flash",
		prices:       map[string]tokenPrice{"auto/best-coding": {Input: 0.22, Output: 0.66}},
	}
	got := parseCostConfig(map[string]any{"cost": cfg.toOptions()})
	if got.source != costSourceTokens || got.defaultModel != "deepseek-v4-flash" {
		t.Fatalf("round trip lost config: %+v", got)
	}
	if p := got.prices["auto/best-coding"]; !almostEqual(p.Input, 0.22) || !almostEqual(p.Output, 0.66) {
		t.Fatalf("round trip lost price: %+v", p)
	}
}

func TestParseCostConfigHeaderlessCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.csv")
	content := "my-model,1,2,0.1,1.5\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parseCostConfig(map[string]any{
		"cost": map[string]any{"source": "tokens", "prices_file": path},
	})
	p, ok := cfg.prices["my-model"]
	if !ok || !almostEqual(p.Input, 1) || !almostEqual(p.Output, 2) ||
		!almostEqual(p.CacheRead, 0.1) || !almostEqual(p.CacheCreation, 1.5) {
		t.Fatalf("my-model = %+v ok=%v", p, ok)
	}
}

func TestLoadPricesCSVPartialRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.csv")
	content := "model,input,output\n" +
		"good,1,2\n" +
		"bad,not-a-number,3\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := map[string]tokenPrice{}
	err := loadPricesCSV(path, dst)
	if err == nil {
		t.Fatal("expected an error reporting the skipped row")
	}
	if p, ok := dst["good"]; !ok || !almostEqual(p.Output, 2) {
		t.Fatalf("valid row not applied: %+v ok=%v", p, ok)
	}
	if _, ok := dst["bad"]; ok {
		t.Error("malformed row should not be applied")
	}
}

func TestParseCostConfigInlineOverridesCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.csv")
	content := "model,input,output\nauto/best-coding,0.22,0.66\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parseCostConfig(map[string]any{
		"cost": map[string]any{
			"source":      "tokens",
			"prices_file": path,
			"prices": map[string]any{
				"auto/best-coding": map[string]any{"input": 0.99, "output": 1.99},
			},
		},
	})
	if p := cfg.prices["auto/best-coding"]; !almostEqual(p.Input, 0.99) || !almostEqual(p.Output, 1.99) {
		t.Fatalf("inline price did not win: %+v", p)
	}
}

func TestParseCostConfigMissingCSVKeepsBuiltins(t *testing.T) {
	cfg := parseCostConfig(map[string]any{
		"cost": map[string]any{
			"source":      "tokens",
			"prices_file": filepath.Join(t.TempDir(), "nope.csv"),
		},
	})
	if _, ok := cfg.prices["deepseek-v4-flash"]; ok {
		t.Fatal("missing file should not inject prices")
	}
	if p, ok := cfg.priceFor("deepseek-v4-flash"); !ok || !almostEqual(p.Input, 0.22) {
		t.Fatalf("built-in price should still resolve: %+v ok=%v", p, ok)
	}
}

func TestBuiltinSonnetPrice(t *testing.T) {
	cfg := defaultCostConfig()
	p, ok := cfg.priceFor("claude-sonnet-5-5")
	if !ok {
		t.Fatal("claude-sonnet-5-5 has no built-in price")
	}
	if !almostEqual(p.Input, 2) || !almostEqual(p.Output, 10) ||
		!almostEqual(p.CacheRead, 0.2) || !almostEqual(p.CacheCreation, 2.5) {
		t.Fatalf("sonnet price = %+v", p)
	}
}

func TestCostConfigPriceFor(t *testing.T) {
	cfg := costConfig{
		source:       costSourceTokens,
		defaultModel: "deepseek-v4-pro",
		prices: map[string]tokenPrice{
			"auto/best-coding": {Input: 0.9, Output: 1.8},
			"*":                {Input: 0.1, Output: 0.2},
		},
	}

	if p, ok := cfg.priceFor("AUTO/BEST-CODING"); !ok || !almostEqual(p.Input, 0.9) {
		t.Errorf("exact override not used: %+v ok=%v", p, ok)
	}
	if p, ok := cfg.priceFor("something-else"); !ok || !almostEqual(p.Input, 0.1) {
		t.Errorf("catch-all not used: %+v ok=%v", p, ok)
	}

	// Without a catch-all the built-in table wins.
	noCatchAll := costConfig{
		source:       costSourceTokens,
		defaultModel: "deepseek-v4-pro",
		prices:       map[string]tokenPrice{},
	}
	if p, ok := noCatchAll.priceFor("deepseek-v4-flash[1m]"); !ok || !almostEqual(p.Output, 0.66) {
		t.Errorf("builtin price not resolved: %+v ok=%v", p, ok)
	}
	// Unknown non-DeepSeek models use the configured default model.
	if p, ok := noCatchAll.priceFor("auto/best-coding"); !ok || !almostEqual(p.Input, 0.66) {
		t.Errorf("default_model fallback not used: %+v ok=%v", p, ok)
	}

	// DeepSeek aliases fall back to v4-flash even without default_model.
	bare := costConfig{source: costSourceTokens, prices: map[string]tokenPrice{}}
	if _, ok := bare.priceFor("pool-deepseek"); !ok {
		t.Error("deepseek alias should resolve to a built-in price")
	}
	if _, ok := bare.priceFor("gpt-5.5"); ok {
		t.Error("unrelated model should not resolve")
	}
	// Provider-prefixed ids resolve via their last path segment.
	if p, ok := bare.priceFor("bigmodel/glm-5.3-flash"); !ok || !almostEqual(p.Input, 0.075) {
		t.Errorf("provider-prefixed model not resolved: %+v ok=%v", p, ok)
	}
}

func TestCostConfigTurnCostUSDPrefersModelUsage(t *testing.T) {
	cfg := costConfig{
		source: costSourceTokens,
		prices: map[string]tokenPrice{
			"model-a": {Input: 1, Output: 2, CacheRead: 0.1, CacheCreation: 1.5},
			"model-b": {Input: 3, Output: 4},
		},
	}

	raw := map[string]any{
		// Deliberately wrong top-level usage: modelUsage must win.
		"usage": map[string]any{"input_tokens": float64(999999)},
		"modelUsage": map[string]any{
			"model-a": map[string]any{
				"inputTokens":              float64(1_000_000),
				"outputTokens":             float64(1_000_000),
				"cacheReadInputTokens":     float64(1_000_000),
				"cacheCreationInputTokens": float64(1_000_000),
			},
			"model-b": map[string]any{
				"inputTokens":  float64(1_000_000),
				"outputTokens": float64(1_000_000),
			},
		},
	}
	got := cfg.turnCostUSD(raw, "")
	want := (1 + 2 + 0.1 + 1.5) + (3 + 4)
	if !almostEqual(got, want) {
		t.Fatalf("turnCostUSD = %v, want %v", got, want)
	}
}

func TestCostConfigTurnCostUSDFallsBackToUsage(t *testing.T) {
	cfg := costConfig{
		source: costSourceTokens,
		prices: map[string]tokenPrice{"auto/best-coding": {Input: 1, Output: 2}},
	}
	raw := map[string]any{
		"usage": map[string]any{
			"input_tokens":                float64(1_000_000),
			"output_tokens":               float64(1_000_000),
			"cache_read_input_tokens":     float64(0),
			"cache_creation_input_tokens": float64(0),
		},
	}
	if got := cfg.turnCostUSD(raw, "auto/best-coding"); !almostEqual(got, 3) {
		t.Fatalf("turnCostUSD = %v, want 3", got)
	}
}

func TestCostConfigTurnCostUSDUnknownModelIsZero(t *testing.T) {
	cfg := costConfig{source: costSourceTokens, prices: map[string]tokenPrice{}}
	raw := map[string]any{
		"modelUsage": map[string]any{
			"gpt-5.5": map[string]any{"inputTokens": float64(1_000_000)},
		},
	}
	if got := cfg.turnCostUSD(raw, ""); got != 0 {
		t.Fatalf("unknown model cost = %v, want 0", got)
	}
}

// TestHandleResultCostSourceTokens verifies that "tokens" ignores the CLI's
// total_cost_usd (which overstates third-party models) and accumulates the
// per-turn price instead.
func TestHandleResultCostSourceTokens(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events:  make(chan core.Event, 8),
		ctx:     ctx,
		costCfg: costConfig{source: costSourceTokens, prices: map[string]tokenPrice{"pool-deepseek": {Input: 1, Output: 2}}},
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	result := func() map[string]any {
		return map[string]any{
			"type":           "result",
			"result":         "done",
			"session_id":     "test-session",
			"total_cost_usd": 0.5, // must be ignored
			"modelUsage": map[string]any{
				"pool-deepseek": map[string]any{
					"inputTokens":  float64(1_000_000),
					"outputTokens": float64(1_000_000),
				},
			},
		}
	}

	cs.handleResult(result())
	evt := <-cs.events
	if !almostEqual(evt.CostUSD, 3) {
		t.Errorf("turn 1 CostUSD = %v, want 3", evt.CostUSD)
	}
	if !almostEqual(evt.SessionCostUSD, 3) {
		t.Errorf("turn 1 SessionCostUSD = %v, want 3", evt.SessionCostUSD)
	}

	cs.handleResult(result())
	evt = <-cs.events
	if !almostEqual(evt.CostUSD, 3) || !almostEqual(evt.SessionCostUSD, 6) {
		t.Errorf("turn 2 cost = %v/%v, want 3/6", evt.CostUSD, evt.SessionCostUSD)
	}
	if !almostEqual(cs.GetLastCostUSD(), 3) || !almostEqual(cs.GetSessionCostUSD(), 6) {
		t.Errorf("session reporters = %v/%v, want 3/6", cs.GetLastCostUSD(), cs.GetSessionCostUSD())
	}
}

// TestHandleResultCostSourceTokensFallsBackToCLI covers models that have no
// configured token price (e.g. an Anthropic model selected for one turn): the
// CLI's per-turn delta is accurate there and must be used instead of silently
// reporting zero.
func TestHandleResultCostSourceTokensFallsBackToCLI(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events:  make(chan core.Event, 8),
		ctx:     ctx,
		costCfg: costConfig{source: costSourceTokens, prices: map[string]tokenPrice{"deepseek-v4-flash": {Input: 0.22, Output: 0.66}}},
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	result := func(total float64) map[string]any {
		return map[string]any{
			"type":           "result",
			"result":         "done",
			"session_id":     "test-session",
			"total_cost_usd": total,
			"modelUsage": map[string]any{
				"some-unpriced-model": map[string]any{
					"inputTokens":  float64(1000),
					"outputTokens": float64(1000),
				},
			},
		}
	}

	cs.handleResult(result(0.50))
	evt := <-cs.events
	if !almostEqual(evt.CostUSD, 0.50) {
		t.Errorf("turn 1 CostUSD = %v, want 0.50", evt.CostUSD)
	}

	cs.handleResult(result(0.80))
	evt = <-cs.events
	if !almostEqual(evt.CostUSD, 0.30) {
		t.Errorf("turn 2 CostUSD = %v, want 0.30", evt.CostUSD)
	}
	if !almostEqual(evt.SessionCostUSD, 0.80) {
		t.Errorf("turn 2 SessionCostUSD = %v, want 0.80", evt.SessionCostUSD)
	}
}

func TestHandleResultCostSourceNone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := &claudeSession{
		events:  make(chan core.Event, 8),
		ctx:     ctx,
		costCfg: costConfig{source: costSourceNone, prices: map[string]tokenPrice{}},
	}
	cs.sessionID.Store("test-session")
	cs.alive.Store(true)

	cs.handleResult(map[string]any{
		"type":           "result",
		"result":         "done",
		"session_id":     "test-session",
		"total_cost_usd": 0.5,
	})
	evt := <-cs.events
	if evt.CostUSD != 0 || evt.SessionCostUSD != 0 {
		t.Errorf("none source reported cost: %v/%v", evt.CostUSD, evt.SessionCostUSD)
	}
	if cs.GetLastCostUSD() != 0 || cs.GetSessionCostUSD() != 0 {
		t.Errorf("none source reported cost via reporter")
	}
}
