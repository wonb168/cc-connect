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
// after every completed turn. It carries only stable identifiers plus the
// turn's token counts and authoritative USD cost — no message content (Q7甲).
type UsageRecord struct {
	Platform                 string  `json:"platform"`
	SenderID                 string  `json:"sender_id"`
	SessionKey               string  `json:"session_key"`
	Model                    string  `json:"model"`
	InputTokens              int     `json:"input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
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
func (e *Engine) recordUsage(sessionKey, model string, ev Event, ts time.Time) {
	endpoint := e.usageRecorderEndpoint
	if endpoint == "" {
		return
	}
	platform, _, senderID := ParseSessionKey(sessionKey)
	rec := UsageRecord{
		Platform:                 platform,
		SenderID:                 senderID,
		SessionKey:               sessionKey,
		Model:                    model,
		InputTokens:              ev.InputTokens,
		OutputTokens:             ev.OutputTokens,
		CacheCreationInputTokens: ev.CacheCreationInputTokens,
		CacheReadInputTokens:     ev.CacheReadInputTokens,
		CostUSD:                  ev.CostUSD,
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
