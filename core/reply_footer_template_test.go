package core

import (
	"strings"
	"testing"
	"time"
)

func TestFormatCostSegment(t *testing.T) {
	i18n := NewI18n(LangChinese)
	cases := []struct {
		name        string
		cost        float64
		sessionCost float64
		ratio       float64
		want        string
	}{
		{"unknown_zero", 0, 0, 1, ""},
		{"negative", -1, 0, 1, ""},
		{"ratio_one", 0.0421, 0, 1, "0.0421 点 ($0.0421)"},
		{"ratio_ten", 0.5, 0, 10, "5 点 ($0.5)"},
		{"ratio_default_when_zero", 2, 0, 0, "2 点 ($2)"},
		// Session total appended once it exceeds the turn cost.
		{"with_session_total", 0.25, 1.5, 1, "0.25 点 ($0.25) / 共 1.5 点 ($1.5)"},
		// First turn of a session: total == turn cost, so it is not repeated.
		{"first_turn_no_duplicate", 0.25, 0.25, 1, "0.25 点 ($0.25)"},
		// Session total unknown (agent implements only CostReporter).
		{"session_total_unknown", 0.25, 0, 1, "0.25 点 ($0.25)"},
		// Ratio applies to both halves.
		{"session_total_ratio", 0.5, 2, 10, "5 点 ($0.5) / 共 20 点 ($2)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatCostSegment(tc.cost, tc.sessionCost, tc.ratio, i18n)
			if got != tc.want {
				t.Errorf("formatCostSegment(%v, %v, %v) = %q, want %q", tc.cost, tc.sessionCost, tc.ratio, got, tc.want)
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

// A mid-session turn shows both what this turn cost and what the session has
// cost so far, so a user can see the per-turn charge without mistaking the
// running total for it.
func TestComposeRichStatusFooter_TurnAndSessionCost(t *testing.T) {
	session := &controllableAgentSession{
		model:          "claude-opus-4-8[1m]",
		workDir:        "/tmp/ws",
		costUSD:        0.1234,
		sessionCostUSD: 1.5,
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
	if !strings.Contains(got, "0.1234 点 ($0.1234)") {
		t.Errorf("expected per-turn cost in footer, got %q", got)
	}
	if !strings.Contains(got, "共 1.5 点 ($1.5)") {
		t.Errorf("expected session total in footer, got %q", got)
	}
}

// The session_* placeholders expose the running total to a custom template.
func TestComposeRichStatusFooter_TemplateSessionCostPlaceholders(t *testing.T) {
	session := &controllableAgentSession{
		model:          "claude-opus-4-8[1m]",
		workDir:        "/tmp/ws",
		costUSD:        0.5,
		sessionCostUSD: 2,
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
	e.SetReplyFooterTemplate("本轮{point}点({cost_usd})|累计{session_point}点({session_cost_usd})")

	got := e.composeRichStatusFooter(false, time.Now(), nil, session, "/tmp/ws")
	want := "本轮5点(0.5)|累计20点(2)"
	if got != want {
		t.Errorf("template footer = %q, want %q", got, want)
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
