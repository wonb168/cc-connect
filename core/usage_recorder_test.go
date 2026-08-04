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
	}
	e.recordUsage("feishu:chatABC:ou_sender123", "claude-opus-4-8[1m]", nil, ev, &ContextUsage{ContextWindow: 1000, UsedTokens: 50}, 3*time.Second, time.UnixMilli(1700000000000))

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
	if got.CostUSD != 0.42 {
		t.Errorf("CostUSD = %v, want 0.42", got.CostUSD)
	}
	if got.OutputTokens != 20 || got.InputTokens != 10 || got.CacheCreationInputTokens != 5 || got.CacheReadInputTokens != 7 {
		t.Errorf("token counts mismatch: %+v", got)
	}
}

func TestRecordUsage_DisabledWhenNoEndpoint(t *testing.T) {
	e := &Engine{} // no endpoint
	// Must not panic and must be a no-op.
	e.recordUsage("feishu:c:u", "m", nil, Event{CostUSD: 1}, nil, 0, time.Now())
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

	e.recordUsage("feishu:chatABC:ou_sender123", "claude-opus-4-8[1m]", sessions, Event{CostUSD: 0.1}, nil, 0, time.Now())

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
