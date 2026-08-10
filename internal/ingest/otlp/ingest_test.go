package otlp_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/FullFran/agent-eval-tui/internal/domain"
	"github.com/FullFran/agent-eval-tui/internal/ingest/otlp"
	"github.com/FullFran/agent-eval-tui/internal/store/sqlite"
)

func str(s string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: s}}
}

func i64(n int64) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: n}}
}

func kv(k string, v *commonpb.AnyValue) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: v}
}

// agentExport builds the span tree the GenAI conventions describe:
// invoke_agent -> chat + execute_tool.
func agentExport() *collectorpb.ExportTraceServiceRequest {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	traceID := []byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}

	root := &tracepb.Span{
		TraceId: traceID,
		SpanId:  []byte{1, 0, 0, 0, 0, 0, 0, 1},
		// Eight zero bytes, which is what exporters send for "no parent".
		ParentSpanId:      []byte{0, 0, 0, 0, 0, 0, 0, 0},
		Name:              "invoke_agent researcher",
		Kind:              tracepb.Span_SPAN_KIND_INTERNAL,
		StartTimeUnixNano: uint64(base.UnixNano()),
		EndTimeUnixNano:   uint64(base.Add(3 * time.Second).UnixNano()),
		Attributes: []*commonpb.KeyValue{
			kv(domain.AttrOperationName, str(domain.OpInvokeAgent)),
			kv(domain.AttrAgentName, str("researcher")),
		},
	}

	chat := &tracepb.Span{
		TraceId:           traceID,
		SpanId:            []byte{1, 0, 0, 0, 0, 0, 0, 2},
		ParentSpanId:      root.SpanId,
		Name:              "chat",
		StartTimeUnixNano: uint64(base.Add(100 * time.Millisecond).UnixNano()),
		EndTimeUnixNano:   uint64(base.Add(1200 * time.Millisecond).UnixNano()),
		Attributes: []*commonpb.KeyValue{
			kv(domain.AttrOperationName, str(domain.OpChat)),
			kv(domain.AttrProviderName, str("anthropic")),
			kv(domain.AttrRequestModel, str("claude-sonnet-4-5")),
			kv(domain.AttrResponseModel, str("claude-sonnet-4-5-20260101")),
			kv(domain.AttrInputTokens, i64(1200)),
			// Deliberately a string: some SDKs emit token counts this way, and
			// dropping them would silently zero the cost of a real agent.
			kv(domain.AttrOutputTokens, str("340")),
			kv(domain.AttrInputMessages, str(`[{"role":"user","content":"hola"}]`)),
		},
	}

	tool := &tracepb.Span{
		TraceId:           traceID,
		SpanId:            []byte{1, 0, 0, 0, 0, 0, 0, 3},
		ParentSpanId:      root.SpanId,
		Name:              "execute_tool search",
		StartTimeUnixNano: uint64(base.Add(1300 * time.Millisecond).UnixNano()),
		EndTimeUnixNano:   uint64(base.Add(2100 * time.Millisecond).UnixNano()),
		Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "timeout"},
		Attributes: []*commonpb.KeyValue{
			kv(domain.AttrOperationName, str(domain.OpExecuteTool)),
			kv(domain.AttrToolName, str("search")),
			kv(domain.AttrToolCallArguments, str(`{"q":"córdoba"}`)),
		},
	}

	return &collectorpb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kv(domain.AttrServiceName, str("hagabox"))},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{root, chat, tool},
			}},
		}},
	}
}

func TestTranslatePromotesGenAIAttributes(t *testing.T) {
	spans := otlp.Translate(agentExport().GetResourceSpans())
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3", len(spans))
	}

	byName := map[string]domain.Span{}
	for _, s := range spans {
		byName[s.Name] = s
	}

	root := byName["invoke_agent researcher"]
	if root.ParentSpanID != "" {
		t.Errorf("all-zero parent must read as root, got %q", root.ParentSpanID)
	}
	if root.ServiceName != "hagabox" {
		t.Errorf("service.name must be merged from the resource, got %q", root.ServiceName)
	}
	if root.AgentName != "researcher" {
		t.Errorf("agent name = %q, want researcher", root.AgentName)
	}

	chat := byName["chat"]
	if !chat.IsLLMCall() {
		t.Error("chat span must be recognised as an LLM call")
	}
	if chat.InputTokens != 1200 {
		t.Errorf("input tokens = %d, want 1200", chat.InputTokens)
	}
	if chat.OutputTokens != 340 {
		t.Errorf("string-encoded output tokens = %d, want 340", chat.OutputTokens)
	}
	if chat.Model() != "claude-sonnet-4-5-20260101" {
		t.Errorf("Model() must prefer the response model, got %q", chat.Model())
	}
	if chat.Content.InputMessages == "" {
		t.Error("input messages must be captured as content")
	}

	tool := byName["execute_tool search"]
	if !tool.IsToolCall() {
		t.Error("execute_tool span must be recognised as a tool call")
	}
	if tool.StatusCode != domain.StatusError || tool.StatusMessage != "timeout" {
		t.Errorf("status = %q/%q, want error/timeout", tool.StatusCode, tool.StatusMessage)
	}
	if tool.Attributes[domain.AttrToolName] != "search" {
		t.Error("promoted attributes must still be present in the raw map")
	}
}

func TestRollupSummarisesTrace(t *testing.T) {
	spans := otlp.Translate(agentExport().GetResourceSpans())
	tr := domain.Rollup(spans)

	if tr.SpanCount != 3 || tr.LLMCalls != 1 || tr.ToolCalls != 1 {
		t.Errorf("counts = %d spans / %d llm / %d tool, want 3/1/1",
			tr.SpanCount, tr.LLMCalls, tr.ToolCalls)
	}
	if tr.ErrorCount != 1 || !tr.Failed() {
		t.Errorf("error count = %d, want 1", tr.ErrorCount)
	}
	if tr.InputTokens != 1200 || tr.OutputTokens != 340 {
		t.Errorf("tokens = %d/%d, want 1200/340", tr.InputTokens, tr.OutputTokens)
	}
	if tr.RootName != "invoke_agent researcher" {
		t.Errorf("root = %q", tr.RootName)
	}
	if tr.Duration() != 3*time.Second {
		t.Errorf("duration = %s, want 3s", tr.Duration())
	}
}

// Orphaned spans are the normal case when only part of a system is
// instrumented: the parent ID points at a span nothing ever exports.
func TestRollupTreatsOrphanAsRoot(t *testing.T) {
	base := time.Now()
	spans := []domain.Span{{
		TraceID: "t", SpanID: "child", ParentSpanID: "never-exported",
		Name: "orphan", StartTime: base, EndTime: base.Add(time.Second),
	}}

	tr := domain.Rollup(spans)
	if tr.RootSpanID != "child" {
		t.Errorf("root = %q, want the orphan itself", tr.RootSpanID)
	}
}

func postExport(t *testing.T, srv *httptest.Server, body []byte, contentType string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+otlp.TracesPath, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// End to end: a real OTLP payload over HTTP, persisted, then read back.
func TestReceiverPersistsProtobufAndJSON(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := httptest.NewServer(otlp.NewReceiver(store, nil).Handler())
	t.Cleanup(srv.Close)

	pb, err := proto.Marshal(agentExport())
	if err != nil {
		t.Fatal(err)
	}
	if resp := postExport(t, srv, pb, "application/x-protobuf"); resp.StatusCode != http.StatusOK {
		t.Fatalf("protobuf export: status %d", resp.StatusCode)
	}

	traces, err := store.RecentTraces(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	if traces[0].InputTokens != 1200 || traces[0].OutputTokens != 340 {
		t.Errorf("stored tokens = %d/%d, want 1200/340",
			traces[0].InputTokens, traces[0].OutputTokens)
	}
	if traces[0].ErrorCount != 1 {
		t.Errorf("stored error count = %d, want 1", traces[0].ErrorCount)
	}

	// The same batch again, as JSON. Exporters retry, and a retry must not
	// duplicate spans or double the trace totals.
	js, err := protojson.Marshal(agentExport())
	if err != nil {
		t.Fatal(err)
	}
	if resp := postExport(t, srv, js, "application/json"); resp.StatusCode != http.StatusOK {
		t.Fatalf("json export: status %d", resp.StatusCode)
	}

	count, err := store.CountSpans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("re-exporting the same batch stored %d spans, want 3", count)
	}

	traces, err = store.RecentTraces(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces after retry, want 1", len(traces))
	}
	if traces[0].InputTokens != 1200 {
		t.Errorf("tokens double-counted on retry: %d, want 1200", traces[0].InputTokens)
	}
}

// A batch arriving in two parts, children before the parent, must still produce
// a correct rollup. Exporters batch by time, not by tree.
func TestRollupSurvivesOutOfOrderBatches(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	all := otlp.Translate(agentExport().GetResourceSpans())
	var root, children []domain.Span
	for _, s := range all {
		if s.ParentSpanID == "" {
			root = append(root, s)
		} else {
			children = append(children, s)
		}
	}

	if err := store.SaveSpans(ctx, children); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSpans(ctx, root); err != nil {
		t.Fatal(err)
	}

	traces, err := store.RecentTraces(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	if traces[0].SpanCount != 3 {
		t.Errorf("span count = %d, want 3", traces[0].SpanCount)
	}
	if traces[0].RootName != "invoke_agent researcher" {
		t.Errorf("root resolved to %q after late arrival", traces[0].RootName)
	}
}

// Regression: OTLP/JSON specifies hex trace and span IDs, while protojson
// decodes `bytes` fields as base64. Feeding a real OTLP/JSON payload through
// plain protojson silently produced a different, valid-looking ID for every
// span — no error, and children stopped joining their parents.
//
// This test uses a hand-written hex payload on purpose. The earlier JSON test
// round-trips through protojson.Marshal, which emits base64, so it agreed with
// the bug instead of catching it.
func TestReceiverAcceptsHexEncodedJSONIDs(t *testing.T) {
	const traceID = "0a0b0c0d0e0f10111213141516171819"
	const rootID = "1111111111111111"
	const childID = "2222222222222222"

	payload := `{"resourceSpans":[{"resource":{"attributes":[
      {"key":"service.name","value":{"stringValue":"hagabox"}}]},
    "scopeSpans":[{"spans":[
      {"traceId":"` + traceID + `","spanId":"` + rootID + `","name":"invoke_agent soporte",
       "startTimeUnixNano":"1786104000000000000","endTimeUnixNano":"1786104004500000000",
       "attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"invoke_agent"}}]},
      {"traceId":"` + traceID + `","spanId":"` + childID + `","parentSpanId":"` + rootID + `","name":"chat",
       "startTimeUnixNano":"1786104000200000000","endTimeUnixNano":"1786104002100000000",
       "attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"chat"}},
                     {"key":"gen_ai.usage.input_tokens","value":{"intValue":"2480"}}]}
    ]}]}]}`

	ctx := context.Background()
	store, err := sqlite.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := httptest.NewServer(otlp.NewReceiver(store, nil).Handler())
	t.Cleanup(srv.Close)

	if resp := postExport(t, srv, []byte(payload), "application/json"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	traces, err := store.RecentTraces(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	if traces[0].TraceID != traceID {
		t.Errorf("trace id = %q, want %q", traces[0].TraceID, traceID)
	}
	if traces[0].RootSpanID != rootID {
		t.Errorf("root span id = %q, want %q", traces[0].RootSpanID, rootID)
	}

	spans, err := store.TraceSpans(ctx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}
	for _, s := range spans {
		if s.SpanID == childID && s.ParentSpanID != rootID {
			t.Errorf("child parent = %q, want %q — the tree is broken", s.ParentSpanID, rootID)
		}
	}
}
