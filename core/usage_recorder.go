package core

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// usageRecorderTimeout bounds the outbound POST so a slow/dead recorder can
// never stall the reply path. The record is best-effort; on any failure we log
// and move on (Q8甲: fire-and-forget, never block, dropping a record is
// acceptable).
const usageRecorderTimeout = 1500 * time.Millisecond

// UsageRecord is the payload POSTed to the external usage-recorder microservice
// after every completed turn. Fields map to the companion's destination table
// (e.g. a shared "turns" table): no message content is carried (Q7甲).
type UsageRecord struct {
	Platform                 string  `json:"platform"`
	SenderID                 string  `json:"sender_id"`
	SessionKey               string  `json:"session_key"`
	AgentSessionID           string  `json:"agent_session_id,omitempty"`
	UserName                 string  `json:"user_name,omitempty"`
	Agent                    string  `json:"agent"`
	Model                    string  `json:"model"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
	TurnDurationMs           int     `json:"turn_duration_ms"`
	ContextPct               int     `json:"context_pct"`
	TimestampMs              int64   `json:"timestamp_ms"`
}

// SetUsageRecorderEndpoint sets the HTTP endpoint the engine POSTs completed-turn
// usage records to. Empty disables recording.
func (e *Engine) SetUsageRecorderEndpoint(endpoint string) {
	e.usageRecorderEndpoint = endpoint
}

// recordUsage fires a best-effort, non-blocking POST of a completed turn's usage
// to the configured recorder endpoint. Safe to call unconditionally: it returns
// immediately when recording is disabled. Never blocks the caller (spawns a
// goroutine with its own short timeout).
//
// agentSessionID is the underlying CLI's session ID (e.g. claudecode's
// --resume UUID), not cc-connect's own Session.ID. It changes on /new (cleared,
// then repopulated once the fresh agent session replies) while sessionKey stays
// stable across /new — so it's the field that lets a recorder distinguish
// per-conversation segments within the same platform:chat:user.
func (e *Engine) recordUsage(sessionKey, model, agentSessionID string, sessions *SessionManager, ev Event, usage *ContextUsage, turnDuration time.Duration, ts time.Time) {
	endpoint := e.usageRecorderEndpoint
	if endpoint == "" {
		return
	}
	platform, _, senderID := ParseSessionKey(sessionKey)
	var ctxPct int
	if usage != nil && usage.ContextWindow > 0 {
		used := usage.UsedTokens
		if used <= 0 && usage.TotalTokens > 0 {
			used = usage.TotalTokens
		}
		if used > 0 {
			ctxPct = used * 100 / usage.ContextWindow
			if ctxPct > 100 {
				ctxPct = 100
			}
		}
	}
	if sessions == nil {
		sessions = e.sessions
	}
	var userName string
	if sessions != nil {
		if meta := sessions.GetUserMeta(sessionKey); meta != nil {
			userName = meta.UserName
			if meta.UnionID != "" {
				senderID = meta.UnionID
			}
		}
	}
	rec := UsageRecord{
		Platform:                 platform,
		SenderID:                 senderID,
		SessionKey:               sessionKey,
		AgentSessionID:           agentSessionID,
		UserName:                 userName,
		Agent:                    e.name,
		Model:                    model,
		InputTokens:              ev.InputTokens,
		OutputTokens:             ev.OutputTokens,
		CacheCreationInputTokens: ev.CacheCreationInputTokens,
		CacheReadInputTokens:     ev.CacheReadInputTokens,
		CostUSD:                  ev.CostUSD,
		TurnDurationMs:           int(turnDuration / time.Millisecond),
		ContextPct:               ctxPct,
		TimestampMs:              ts.UnixMilli(),
	}
	go postUsageRecord(endpoint, rec)
}

// postUsageRecord performs the actual HTTP POST. Package-level (not a method) so
// it captures no engine state beyond the endpoint and record passed in.
func postUsageRecord(endpoint string, rec UsageRecord) {
	body, err := json.Marshal(rec)
	if err != nil {
		slog.Warn("usage recorder: marshal failed", "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), usageRecorderTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		slog.Warn("usage recorder: build request failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("usage recorder: post failed", "endpoint", endpoint, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("usage recorder: non-2xx response", "endpoint", endpoint, "status", resp.StatusCode)
	}
}
