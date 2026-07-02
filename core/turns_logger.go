package core

import (
	"database/sql"
	"log/slog"
	"time"

	_ "github.com/lib/pq"
)

// TurnRecord holds the data written to the turns table after each agent turn.
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
	TurnDuration time.Duration
	RecordedAt   time.Time
}

// TurnsLogger writes turn records to a PostgreSQL turns table asynchronously.
type TurnsLogger struct {
	db   *sql.DB
	ch   chan TurnRecord
	done chan struct{}
}

// NewTurnsLogger connects to the given DSN and starts a background writer.
// Returns nil (with a warning log) if the DSN is empty or connection fails.
func NewTurnsLogger(dsn string) *TurnsLogger {
	if dsn == "" {
		return nil
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		slog.Warn("turns_db: failed to open connection", "error", err)
		return nil
	}
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		slog.Warn("turns_db: ping failed", "error", err)
		db.Close()
		return nil
	}
	l := &TurnsLogger{
		db:   db,
		ch:   make(chan TurnRecord, 256),
		done: make(chan struct{}),
	}
	go l.run()
	LoadModelPricingFromDB(db)
	RefreshModelPricingFromDB(db, 10*time.Minute)
	slog.Info("turns_db: logger started")
	return l
}

func (l *TurnsLogger) run() {
	defer close(l.done)
	for rec := range l.ch {
		ts := rec.RecordedAt.Format(time.RFC3339)
		_, err := l.db.Exec(
			`INSERT INTO turns (ts, user_id, user_name, agent, model, input_tokens, output_tokens, cache_read, cache_create, cost_usd, turn_duration_ms)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			ts, rec.UserID, rec.UserName, rec.Project, rec.Model,
			rec.InputTokens, rec.OutputTokens,
			rec.CacheRead, rec.CacheWrite,
			rec.CostUSD,
			int(rec.TurnDuration.Milliseconds()),
		)
		if err != nil {
			slog.Warn("turns_db: insert failed", "error", err)
		}
	}
}

// Log enqueues a turn record for async insertion. Non-blocking: drops if full.
func (l *TurnsLogger) Log(rec TurnRecord) {
	if l == nil {
		return
	}
	select {
	case l.ch <- rec:
	default:
		slog.Warn("turns_db: channel full, dropping turn record")
	}
}

// Close drains the queue and closes the DB connection.
func (l *TurnsLogger) Close() {
	if l == nil {
		return
	}
	close(l.ch)
	<-l.done
	l.db.Close()
}
