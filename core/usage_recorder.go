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
	Platform string `json:"platform"`
	// AppID is the platform-level app/bot/client registration ID (e.g. Feishu
	// app_id, DingTalk client_id, WeCom corp_id) — set when the platform
	// implements AppIDProvider. Distinguishes multiple bots of the same
	// Platform value recording into the same downstream table. Empty for
	// platforms without an app-registration concept.
	AppID          string `json:"app_id,omitempty"`
	SenderID       string `json:"sender_id"`
	SessionKey     string `json:"session_key"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	UserName       string `json:"user_name,omitempty"`
	// Agent is the cc-connect project name (e.g. "viking", "ccc") — one Engine
	// per project, so this is the project/bot identity, not the CLI backend.
	Agent string `json:"agent"`
	// AgentType is the underlying CLI backend name (e.g. "claudecode", "codex"),
	// from Engine.AgentTypeName(). Distinct from Agent: a recorder grouping by
	// project should use Agent; grouping by CLI/billing-backend should use
	// AgentType.
	AgentType                string `json:"agent_type,omitempty"`
	Model                    string `json:"model"`
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
	// CostUSD is the CLI's cumulative total_cost_usd for the session as of this
	// turn — a running total, NOT this turn's charge. Kept under the original
	// field name because that is what the destination column has always held;
	// summing it across turns double-counts.
	CostUSD float64 `json:"cost_usd"`
	// TurnCostUSD is the cost of this turn alone (the rise in CostUSD since the
	// previous turn). This is the field to SUM for spend over a period. 0 =
	// unknown (agent backend does not report billing).
	TurnCostUSD    float64 `json:"turn_cost_usd"`
	TurnDurationMs int     `json:"turn_duration_ms"`
	ContextPct     int     `json:"context_pct"`
	TimestampMs    int64   `json:"timestamp_ms"`
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
//
// p is the platform instance that handled this turn (may be nil, e.g. cron
// turns with no live platform); when it implements AppIDProvider, its AppID()
// is attached to the record so a recorder can distinguish multiple bots of
// the same Platform value.
func (e *Engine) recordUsage(sessionKey, model, agentSessionID string, p Platform, sessions *SessionManager, ev Event, usage *ContextUsage, turnDuration time.Duration, ts time.Time) {
	endpoint := e.usageRecorderEndpoint
	if endpoint == "" {
		return
	}
	platform, _, senderID := ParseSessionKey(sessionKey)
	var appID string
	if provider, ok := p.(AppIDProvider); ok {
		appID = provider.AppID()
	}
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
	// ev.CostUSD is this turn's charge, ev.SessionCostUSD the session running
	// total. A backend that reports a per-turn cost but keeps no cumulative
	// counter leaves the latter at 0; fall back so the long-standing cost_usd
	// field never regresses to 0 for such an agent.
	sessionCostUSD := ev.SessionCostUSD
	if sessionCostUSD <= 0 {
		sessionCostUSD = ev.CostUSD
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
		AppID:                    appID,
		SenderID:                 senderID,
		SessionKey:               sessionKey,
		AgentSessionID:           agentSessionID,
		UserName:                 userName,
		Agent:                    e.name,
		AgentType:                e.AgentTypeName(),
		Model:                    model,
		InputTokens:              ev.InputTokens,
		OutputTokens:             ev.OutputTokens,
		CacheCreationInputTokens: ev.CacheCreationInputTokens,
		CacheReadInputTokens:     ev.CacheReadInputTokens,
		CostUSD:                  sessionCostUSD,
		TurnCostUSD:              ev.CostUSD,
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
