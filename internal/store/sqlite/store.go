// Package sqlite persists telemetry in a single file.
//
// modernc.org/sqlite is a pure-Go SQLite, so this builds without cgo and the
// result is one static binary. That is the deployment story: drop it next to an
// agent, point an OTLP exporter at it, done. A cgo driver would be marginally
// faster and would cost a C toolchain in every build and container.
package sqlite

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"database/sql"

	_ "modernc.org/sqlite"

	"github.com/FullFran/agent-eval-tui/internal/domain"
)

//go:embed schema.sql
var schema string

// Store is the telemetry database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies the schema.
// Use ":memory:" for tests.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite at %s: %w", path, err)
	}

	// SQLite takes one writer at a time. The receiver is concurrent, so more
	// than one connection buys nothing here and turns contention into
	// SQLITE_BUSY errors instead of a queue.
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// SaveSpans persists a batch and refreshes the rollup of every trace it
// touches, in one transaction.
//
// Spans are upserted by span ID: exporters retry, and a retry must not create a
// second copy or double the trace's token totals.
func (s *Store) SaveSpans(ctx context.Context, spans []domain.Span) error {
	if len(spans) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UnixNano()
	touched := make(map[string]struct{})

	for _, span := range spans {
		if span.TraceID == "" || span.SpanID == "" {
			// A span without identity cannot be stored, deduplicated or joined.
			// Dropping it is the only honest option; the alternative is
			// inventing an ID and corrupting the trace tree.
			continue
		}

		attrs, err := json.Marshal(span.Attributes)
		if err != nil {
			attrs = []byte("{}")
		}

		if _, err := tx.ExecContext(ctx, upsertSpan,
			span.SpanID, span.TraceID, nullable(span.ParentSpanID),
			span.ServiceName, span.Name, span.Kind,
			span.StartTime.UnixNano(), endNano(span), span.Duration().Nanoseconds(),
			string(span.StatusCode), span.StatusMessage,
			span.Operation, span.Provider, span.RequestModel, span.ResponseModel,
			span.InputTokens, span.OutputTokens, span.FinishReasons,
			span.ConversationID, span.AgentName, span.ToolName,
			boolToInt(span.IsLLMCall()), boolToInt(span.IsToolCall()),
			string(attrs), now,
		); err != nil {
			return fmt.Errorf("upsert span %s: %w", span.SpanID, err)
		}

		if !span.Content.IsEmpty() {
			if _, err := tx.ExecContext(ctx, upsertContent,
				span.SpanID,
				span.Content.SystemInstructions, span.Content.InputMessages,
				span.Content.OutputMessages, span.Content.ToolArguments,
				span.Content.ToolResult,
			); err != nil {
				return fmt.Errorf("upsert content %s: %w", span.SpanID, err)
			}
		}

		touched[span.TraceID] = struct{}{}
	}

	for traceID := range touched {
		if err := refreshTrace(ctx, tx, traceID, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// refreshTrace recomputes one trace's rollup from the spans currently stored.
func refreshTrace(ctx context.Context, tx *sql.Tx, traceID string, now int64) error {
	rows, err := tx.QueryContext(ctx, selectSpansForRollup, traceID)
	if err != nil {
		return fmt.Errorf("read spans of %s: %w", traceID, err)
	}
	defer rows.Close()

	var spans []domain.Span
	for rows.Next() {
		var (
			span           domain.Span
			parent         sql.NullString
			startNS, endNS int64
			statusCode     string
		)
		if err := rows.Scan(
			&span.SpanID, &span.TraceID, &parent, &span.ServiceName, &span.Name,
			&startNS, &endNS, &statusCode, &span.RequestModel, &span.ResponseModel,
			&span.InputTokens, &span.OutputTokens, &span.Operation,
			&span.AgentName, &span.ToolName,
		); err != nil {
			return fmt.Errorf("scan span of %s: %w", traceID, err)
		}
		span.ParentSpanID = parent.String
		span.StatusCode = domain.StatusCode(statusCode)
		if startNS > 0 {
			span.StartTime = time.Unix(0, startNS).UTC()
		}
		if endNS > 0 {
			span.EndTime = time.Unix(0, endNS).UTC()
		}
		spans = append(spans, span)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate spans of %s: %w", traceID, err)
	}

	t := domain.Rollup(spans)
	if t.TraceID == "" {
		return nil
	}

	_, err = tx.ExecContext(ctx, upsertTrace,
		t.TraceID, t.ServiceName, t.RootSpanID, t.RootName, t.AgentName,
		t.StartTime.UnixNano(), traceEndNano(t), t.Duration().Nanoseconds(),
		t.SpanCount, t.ErrorCount, t.LLMCalls, t.ToolCalls,
		t.InputTokens, t.OutputTokens, now,
	)
	if err != nil {
		return fmt.Errorf("upsert trace %s: %w", traceID, err)
	}
	return nil
}

// RecentTraces returns the most recently started traces.
func (s *Store) RecentTraces(ctx context.Context, limit int) ([]domain.Trace, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.QueryContext(ctx, selectRecentTraces, limit)
	if err != nil {
		return nil, fmt.Errorf("query traces: %w", err)
	}
	defer rows.Close()

	var out []domain.Trace
	for rows.Next() {
		var (
			t              domain.Trace
			startNS, endNS int64
		)
		if err := rows.Scan(
			&t.TraceID, &t.ServiceName, &t.RootSpanID, &t.RootName, &t.AgentName,
			&startNS, &endNS, &t.SpanCount, &t.ErrorCount, &t.LLMCalls,
			&t.ToolCalls, &t.InputTokens, &t.OutputTokens,
		); err != nil {
			return nil, fmt.Errorf("scan trace: %w", err)
		}
		if startNS > 0 {
			t.StartTime = time.Unix(0, startNS).UTC()
		}
		if endNS > 0 {
			t.EndTime = time.Unix(0, endNS).UTC()
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TraceSpans returns every span of a trace, ordered by start time so the caller
// can build the tree without sorting.
func (s *Store) TraceSpans(ctx context.Context, traceID string) ([]domain.Span, error) {
	rows, err := s.db.QueryContext(ctx, selectSpansForRollup, traceID)
	if err != nil {
		return nil, fmt.Errorf("query spans of %s: %w", traceID, err)
	}
	defer rows.Close()

	var out []domain.Span
	for rows.Next() {
		var (
			span           domain.Span
			parent         sql.NullString
			startNS, endNS int64
			statusCode     string
		)
		if err := rows.Scan(
			&span.SpanID, &span.TraceID, &parent, &span.ServiceName, &span.Name,
			&startNS, &endNS, &statusCode, &span.RequestModel, &span.ResponseModel,
			&span.InputTokens, &span.OutputTokens, &span.Operation,
			&span.AgentName, &span.ToolName,
		); err != nil {
			return nil, fmt.Errorf("scan span: %w", err)
		}
		span.ParentSpanID = parent.String
		span.StatusCode = domain.StatusCode(statusCode)
		if startNS > 0 {
			span.StartTime = time.Unix(0, startNS).UTC()
		}
		if endNS > 0 {
			span.EndTime = time.Unix(0, endNS).UTC()
		}
		out = append(out, span)
	}
	return out, rows.Err()
}

// CountSpans reports how many spans are stored. Used by tests and the status
// line.
func (s *Store) CountSpans(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM spans`).Scan(&n)
	return n, err
}

func endNano(s domain.Span) int64 {
	if s.EndTime.IsZero() {
		return 0
	}
	return s.EndTime.UnixNano()
}

func traceEndNano(t domain.Trace) int64 {
	if t.EndTime.IsZero() {
		return 0
	}
	return t.EndTime.UnixNano()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
