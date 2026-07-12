package core

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// TurnRecord holds the data reported to turn-report-svc after each agent turn.
type TurnRecord struct {
	Project      string // maps to "agent" column
	UserID       string
	UserName     string
	Model        string
	InputTokens  int
	OutputTokens int
	CacheWrite   int // maps to cache_create column
	CacheRead    int
	CostUSD      float64
	ContextPct   int // context window usage percentage (0-100)
	TurnDuration time.Duration
	RecordedAt   time.Time
}

// turnReportPayload is the JSON body sent to turn-report-svc's POST /turn.
type turnReportPayload struct {
	Project        string  `json:"project"`
	UserID         string  `json:"user_id"`
	UserName       string  `json:"user_name"`
	Model          string  `json:"model"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	CacheWrite     int     `json:"cache_write"`
	CacheRead      int     `json:"cache_read"`
	CostUSD        float64 `json:"cost_usd"`
	ContextPct     int     `json:"context_pct"`
	TurnDurationMs int     `json:"turn_duration_ms"`
	Ts             string  `json:"ts"`
}

// TurnsLogger asynchronously reports turn records to the turn-report-svc
// microservice over HTTP, which is responsible for persisting them to
// PostgreSQL. Keeping this as an HTTP call (rather than a direct DB
// connection) means cc-connect stays decoupled from the turns table schema.
type TurnsLogger struct {
	url    string
	client *http.Client
	ch     chan TurnRecord
	done   chan struct{}
}

// NewTurnsLogger starts a background worker that POSTs turn records to the
// given turn-report-svc URL (e.g. "http://127.0.0.1:8790/turn").
// Returns nil (with a warning log) if the URL is empty.
func NewTurnsLogger(reportURL string) *TurnsLogger {
	if reportURL == "" {
		return nil
	}
	l := &TurnsLogger{
		url:    reportURL,
		client: &http.Client{Timeout: 5 * time.Second},
		ch:     make(chan TurnRecord, 256),
		done:   make(chan struct{}),
	}
	go l.run()
	slog.Info("turns_report: logger started", "url", reportURL)
	return l
}

func (l *TurnsLogger) run() {
	defer close(l.done)
	for rec := range l.ch {
		payload := turnReportPayload{
			Project:        rec.Project,
			UserID:         rec.UserID,
			UserName:       rec.UserName,
			Model:          rec.Model,
			InputTokens:    rec.InputTokens,
			OutputTokens:   rec.OutputTokens,
			CacheWrite:     rec.CacheWrite,
			CacheRead:      rec.CacheRead,
			CostUSD:        rec.CostUSD,
			ContextPct:     rec.ContextPct,
			TurnDurationMs: int(rec.TurnDuration.Milliseconds()),
			Ts:             rec.RecordedAt.Format(time.RFC3339),
		}
		body, err := json.Marshal(payload)
		if err != nil {
			slog.Warn("turns_report: marshal failed", "error", err)
			continue
		}
		resp, err := l.client.Post(l.url, "application/json", bytes.NewReader(body))
		if err != nil {
			slog.Warn("turns_report: request failed", "error", err)
			continue
		}
		if resp.StatusCode >= 400 {
			slog.Warn("turns_report: non-2xx response", "status", resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// Log enqueues a turn record for async HTTP reporting. Non-blocking: drops if full.
func (l *TurnsLogger) Log(rec TurnRecord) {
	if l == nil {
		return
	}
	select {
	case l.ch <- rec:
	default:
		slog.Warn("turns_report: channel full, dropping turn record")
	}
}

// Close drains the queue.
func (l *TurnsLogger) Close() {
	if l == nil {
		return
	}
	close(l.ch)
	<-l.done
}
