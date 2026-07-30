package core

import (
	"strings"
	"testing"
	"time"
)

func TestFormatCostSegment(t *testing.T) {
	i18n := NewI18n(LangChinese)
	cases := []struct {
		name  string
		cost  float64
		ratio float64
		want  string
	}{
		{"unknown_zero", 0, 1, ""},
		{"negative", -1, 1, ""},
		{"ratio_one", 0.0421, 1, "0.0421 点 ($0.0421)"},
		{"ratio_ten", 0.5, 10, "5 点 ($0.5)"},
		{"ratio_default_when_zero", 2, 0, "2 点 ($2)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatCostSegment(tc.cost, tc.ratio, i18n)
			if got != tc.want {
				t.Errorf("formatCostSegment(%v, %v) = %q, want %q", tc.cost, tc.ratio, got, tc.want)
			}
		})
	}
}

func TestRenderFooterTemplate_MissingValuesEmpty(t *testing.T) {
	// Q11 甲: missing placeholders render as empty string.
	tmpl := "上下文{ctx}%-{point}点({cost_usd})-{elapsed}秒"
	vars := map[string]string{
		"ctx":     "45",
		"point":   "",
		"cost_usd": "",
		"elapsed": "3.2",
	}
	got := renderFooterTemplate(tmpl, vars)
	want := "上下文45%-点()-3.2秒"
	if got != want {
		t.Errorf("renderFooterTemplate = %q, want %q", got, want)
	}
}

func TestComposeRichStatusFooter_CostSegmentAppended(t *testing.T) {
	session := &controllableAgentSession{
		model:   "claude-opus-4-8[1m]",
		workDir: "/tmp/ws",
		costUSD: 0.1234,
		contextUsage: &ContextUsage{
			InputTokens:              1,
			OutputTokens:             168,
			CacheCreationInputTokens: 971,
			CachedInputTokens:        40800,
			ContextWindow:            1_000_000,
			UsedTokens:               1 + 971 + 40800,
		},
	}
	e := &Engine{i18n: NewI18n(LangChinese)}
	e.SetReplyFooterEnabled(true)
	e.SetShowContextIndicator(true)
	e.SetPointRatio(1)

	got := e.composeRichStatusFooter(false, time.Now().Add(-3*time.Second), nil, session, "/tmp/ws")
	if !strings.Contains(got, "点 ($0.1234)") {
		t.Errorf("expected cost segment in footer, got %q", got)
	}
}

func TestComposeRichStatusFooter_TemplateOverride(t *testing.T) {
	session := &controllableAgentSession{
		model:   "claude-opus-4-8[1m]",
		workDir: "/tmp/ws",
		costUSD: 0.5,
		contextUsage: &ContextUsage{
			OutputTokens:  100,
			ContextWindow: 1000,
			UsedTokens:    450,
		},
	}
	e := &Engine{i18n: NewI18n(LangChinese)}
	e.SetReplyFooterEnabled(true)
	e.SetShowContextIndicator(true)
	e.SetPointRatio(10)
	e.SetReplyFooterTemplate("上下文{ctx}%-{point}点({cost_usd})")

	got := e.composeRichStatusFooter(false, time.Now(), nil, session, "/tmp/ws")
	want := "上下文45%-5点(0.5)"
	if got != want {
		t.Errorf("template footer = %q, want %q", got, want)
	}
}

func TestComposeRichStatusFooter_TemplateMissingCostEmpty(t *testing.T) {
	// No cost reported → {point} and {cost_usd} render empty.
	session := &controllableAgentSession{
		model:   "claude-opus-4-8[1m]",
		workDir: "/tmp/ws",
		contextUsage: &ContextUsage{
			OutputTokens:  100,
			ContextWindow: 1000,
			UsedTokens:    450,
		},
	}
	e := &Engine{i18n: NewI18n(LangChinese)}
	e.SetReplyFooterEnabled(true)
	e.SetReplyFooterTemplate("{ctx}%|{point}|{cost_usd}")

	got := e.composeRichStatusFooter(false, time.Now(), nil, session, "/tmp/ws")
	want := "45%||"
	if got != want {
		t.Errorf("template footer = %q, want %q", got, want)
	}
}
