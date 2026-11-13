package otlp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	logscollectorpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"

	"github.com/FullFran/agent-eval-tui/internal/ingest/otlp"
	"github.com/FullFran/agent-eval-tui/internal/store/sqlite"
)

// claudeCodeFixture is a synthetic export with the shape Claude Code sends
// (captured from a real `claude -p` run, identities replaced).
func claudeCodeFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../testdata/claude-code-logs.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func postLogs(t *testing.T, srv *httptest.Server, body []byte, contentType string) int {
	t.Helper()
	resp, err := http.Post(srv.URL+otlp.LogsPath, contentType, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func newLogsServer(t *testing.T) (*sqlite.Store, *httptest.Server) {
	t.Helper()
	store, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	srv := httptest.NewServer(otlp.NewReceiver(store, nil, nil).Handler())
	t.Cleanup(srv.Close)
	return store, srv
}

// One Claude Code run becomes one trace: model calls, tool calls, errors,
// tokens, the agent's own cost, and the experiment tags.
func TestReceiverIngestsClaudeCodeLogs(t *testing.T) {
	ctx := context.Background()
	store, srv := newLogsServer(t)

	if code := postLogs(t, srv, claudeCodeFixture(t), "application/json"); code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}

	traces, err := store.RecentTraces(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1 (one run)", len(traces))
	}
	tr := traces[0]
	if tr.LLMCalls != 2 || tr.ToolCalls != 2 {
		t.Errorf("llm/tool calls = %d/%d, want 2/2", tr.LLMCalls, tr.ToolCalls)
	}
	if tr.ErrorCount != 2 {
		t.Errorf("errors = %d, want 2 (failed Bash, api_error)", tr.ErrorCount)
	}
	if tr.InputTokens != 20 || tr.OutputTokens != 350 {
		t.Errorf("tokens = %d/%d, want 20/350", tr.InputTokens, tr.OutputTokens)
	}
	if tr.Usage["cache_read_tokens"] != 20000 || tr.Usage["cache_creation_tokens"] != 50000 {
		t.Errorf("cache usage = %v", tr.Usage)
	}
	if !tr.Priced || tr.Cost < 0.2099 || tr.Cost > 0.2101 {
		t.Errorf("cost = %v priced=%v, want 0.21 from cost_usd", tr.Cost, tr.Priced)
	}
	if tr.CaseID != "fix-typo" || tr.Variant != "sonnet-subagents" {
		t.Errorf("case/variant = %q/%q", tr.CaseID, tr.Variant)
	}
	if tr.SessionID != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("session = %q", tr.SessionID)
	}
	// First request starts at 100 ms, last event ends at 5600 ms.
	if got := tr.Duration().Milliseconds(); got != 5500 {
		t.Errorf("duration = %d ms, want 5500", got)
	}

	obs, err := store.CaseObservations(ctx, "fix-typo")
	if err != nil || len(obs) != 1 {
		t.Fatalf("observations = %v, %v; want one run", obs, err)
	}
}

// Exporters retry; a retried batch, in either encoding, must not double the run.
func TestReceiverClaudeCodeLogsAreIdempotentAcrossEncodings(t *testing.T) {
	ctx := context.Background()
	store, srv := newLogsServer(t)
	js := claudeCodeFixture(t)

	var export logscollectorpb.ExportLogsServiceRequest
	if err := protojson.Unmarshal(js, &export); err != nil {
		t.Fatal(err)
	}
	pb, err := proto.Marshal(&export)
	if err != nil {
		t.Fatal(err)
	}

	for _, body := range []struct {
		b  []byte
		ct string
	}{{js, "application/json"}, {pb, "application/x-protobuf"}, {js, "application/json"}} {
		if code := postLogs(t, srv, body.b, body.ct); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
	}

	count, err := store.CountSpans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Errorf("stored %d spans after three exports, want 5", count)
	}
	traces, _ := store.RecentTraces(ctx, 10)
	if len(traces) != 1 || traces[0].OutputTokens != 350 {
		t.Errorf("retry changed the run: %+v", traces)
	}
}

// Account identity is not needed to evaluate a harness and is not stored.
func TestClaudeCodeIdentityIsNotStored(t *testing.T) {
	ctx := context.Background()
	store, srv := newLogsServer(t)
	postLogs(t, srv, claudeCodeFixture(t), "application/json")

	traces, _ := store.RecentTraces(ctx, 10)
	spans, err := store.TraceSpans(ctx, traces[0].TraceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range spans {
		for _, key := range []string{"user.email", "user.account_uuid", "user.account_id", "organization.id", "user.id"} {
			if _, ok := s.Attributes[key]; ok {
				t.Errorf("span %s kept %s", s.Name, key)
			}
		}
	}
}

// A record that names neither a run nor a session cannot be placed in a trace.
func TestClaudeCodeLogWithoutSessionIsDropped(t *testing.T) {
	ctx := context.Background()
	store, srv := newLogsServer(t)
	body := `{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"timeUnixNano":"1794590000000000000",
	  "body":{"stringValue":"claude_code.api_request"},
	  "attributes":[{"key":"event.name","value":{"stringValue":"api_request"}},
	                {"key":"model","value":{"stringValue":"claude-haiku-5-5"}}]}]}]}]}`
	if code := postLogs(t, srv, []byte(body), "application/json"); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if n, _ := store.CountSpans(ctx); n != 0 {
		t.Errorf("stored %d spans, want 0", n)
	}
}
