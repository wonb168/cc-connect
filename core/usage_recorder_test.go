package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRecordUsage_PostsRecord(t *testing.T) {
	var (
		mu   sync.Mutex
		got  UsageRecord
		hits int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := &Engine{agent: &stubAgent{}, name: "viking"} // Agent field reports the project name
	e.SetUsageRecorderEndpoint(srv.URL)

	ev := Event{
		Type:                     EventResult,
		InputTokens:              10,
		OutputTokens:             20,
		CacheCreationInputTokens: 5,
		CacheReadInputTokens:     7,
		CostUSD:                  0.42,
		SessionCostUSD:           1.75,
	}
	e.recordUsage("feishu:chatABC:ou_sender123", "claude-opus-4-8[1m]", "agent-sess-xyz", &appIDStubPlatform{appID: "cli_app_123"}, nil, ev, &ContextUsage{ContextWindow: 1000, UsedTokens: 50}, 3*time.Second, time.UnixMilli(1700000000000))

	// recordUsage posts asynchronously; wait for the request to land.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := hits > 0
		mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("expected 1 POST, got %d", hits)
	}
	if got.Platform != "feishu" {
		t.Errorf("Platform = %q, want feishu", got.Platform)
	}
	if got.SenderID != "ou_sender123" {
		t.Errorf("SenderID = %q, want ou_sender123", got.SenderID)
	}
	if got.SessionKey != "feishu:chatABC:ou_sender123" {
		t.Errorf("SessionKey = %q", got.SessionKey)
	}
	if got.Model != "claude-opus-4-8[1m]" {
		t.Errorf("Model = %q", got.Model)
	}
	if got.Agent != "viking" {
		t.Errorf("Agent = %q, want viking (project name)", got.Agent)
	}
	if got.AgentType != "stub" {
		t.Errorf("AgentType = %q, want stub (stubAgent.Name())", got.AgentType)
	}
	if got.AppID != "cli_app_123" {
		t.Errorf("AppID = %q, want cli_app_123 (from AppIDProvider)", got.AppID)
	}
	// cost_usd keeps its historical meaning (session running total);
	// turn_cost_usd is this turn's own charge.
	if got.CostUSD != 1.75 {
		t.Errorf("CostUSD = %v, want 1.75 (session running total)", got.CostUSD)
	}
	if got.TurnCostUSD != 0.42 {
		t.Errorf("TurnCostUSD = %v, want 0.42 (this turn only)", got.TurnCostUSD)
	}
	if got.OutputTokens != 20 || got.InputTokens != 10 || got.CacheCreationInputTokens != 5 || got.CacheReadInputTokens != 7 {
		t.Errorf("token counts mismatch: %+v", got)
	}
	if got.AgentSessionID != "agent-sess-xyz" {
		t.Errorf("AgentSessionID = %q, want agent-sess-xyz", got.AgentSessionID)
	}
}

// appIDStubPlatform implements core.AppIDProvider so recordUsage's AppID
// wiring can be exercised without depending on a real platform package.
type appIDStubPlatform struct {
	stubPlatformEngine
	appID string
}

func (p *appIDStubPlatform) AppID() string { return p.appID }

func TestRecordUsage_DisabledWhenNoEndpoint(t *testing.T) {
	e := &Engine{} // no endpoint
	// Must not panic and must be a no-op.
	e.recordUsage("feishu:c:u", "m", "agent-sess-1", nil, nil, Event{CostUSD: 1}, nil, 0, time.Now())
}

// TestRecordUsage_AppIDOmittedWhenPlatformDoesNotImplementProvider verifies
// that a platform not implementing AppIDProvider (or a nil platform, e.g. a
// cron turn with no live platform) leaves AppID empty rather than panicking.
func TestRecordUsage_AppIDOmittedWhenPlatformDoesNotImplementProvider(t *testing.T) {
	var (
		mu  sync.Mutex
		got UsageRecord
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := &Engine{agent: &stubAgent{}, name: "viking"}
	e.SetUsageRecorderEndpoint(srv.URL)

	e.recordUsage("feishu:c:u", "m", "agent-sess-1", &stubPlatformEngine{n: "feishu"}, nil, Event{CostUSD: 1}, nil, 0, time.Now())

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := got.SessionKey != ""
		mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if got.AppID != "" {
		t.Errorf("AppID = %q, want empty (platform does not implement AppIDProvider)", got.AppID)
	}
}

// TestRecordUsage_SessionCostFallsBackToTurnCost covers an agent backend that
// reports a per-turn cost but keeps no cumulative counter (SessionCostUSD
// unset). cost_usd must not regress to 0 for such an agent.
func TestRecordUsage_SessionCostFallsBackToTurnCost(t *testing.T) {
	var (
		mu  sync.Mutex
		got UsageRecord
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := &Engine{agent: &stubAgent{}, name: "viking"}
	e.SetUsageRecorderEndpoint(srv.URL)
	e.recordUsage("feishu:c:u", "m", "agent-sess-1", nil, nil, Event{CostUSD: 0.33}, nil, 0, time.Now())

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := got.SessionKey != ""
		mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if got.CostUSD != 0.33 {
		t.Errorf("CostUSD = %v, want 0.33 (fell back to turn cost)", got.CostUSD)
	}
	if got.TurnCostUSD != 0.33 {
		t.Errorf("TurnCostUSD = %v, want 0.33", got.TurnCostUSD)
	}
}

// TestRecordUsage_PrefersUnionIDOverSessionKeySender verifies that when the
// SessionManager has a UnionID recorded for a session (e.g. Feishu union_id,
// stable across the developer's apps for the same person), recordUsage uses
// it as SenderID instead of the open_id embedded in the sessionKey.
func TestRecordUsage_PrefersUnionIDOverSessionKeySender(t *testing.T) {
	var (
		mu  sync.Mutex
		got UsageRecord
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := &Engine{agent: &stubAgent{}, name: "viking"}
	e.SetUsageRecorderEndpoint(srv.URL)

	sessions := NewSessionManager("")
	sessions.UpdateUserMeta("feishu:chatABC:ou_sender123", "Zhang San", "", "on_union_abc")

	e.recordUsage("feishu:chatABC:ou_sender123", "claude-opus-4-8[1m]", "agent-sess-xyz", nil, sessions, Event{CostUSD: 0.1}, nil, 0, time.Now())

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		done := got.SenderID != ""
		mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if got.SenderID != "on_union_abc" {
		t.Errorf("SenderID = %q, want union_id on_union_abc to override open_id from sessionKey", got.SenderID)
	}
	if got.SessionKey != "feishu:chatABC:ou_sender123" {
		t.Errorf("SessionKey = %q, want unchanged sessionKey with open_id", got.SessionKey)
	}
}
