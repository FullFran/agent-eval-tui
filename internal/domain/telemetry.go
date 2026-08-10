// Package domain holds the telemetry model this tool reasons about.
//
// It is deliberately NOT the OTLP protobuf model. OTLP is a wire format with
// oneof-typed attribute values, repeated key/value lists and nanosecond
// integers; carrying that shape into the store and the TUI would spread
// protobuf concerns through the whole program. Everything past the ingest
// boundary works with the types here.
package domain

import "time"

// GenAI semantic convention attribute keys.
//
// Kept as constants rather than inlined so that a convention change is one
// diff in one file. The values follow the OpenTelemetry GenAI semantic
// conventions: agents emit `invoke_agent` spans, model calls emit `chat`, and
// tool invocations emit `execute_tool`.
const (
	AttrOperationName = "gen_ai.operation.name"
	AttrSystem        = "gen_ai.system"
	AttrProviderName  = "gen_ai.provider.name"
	AttrRequestModel  = "gen_ai.request.model"
	AttrResponseModel = "gen_ai.response.model"
	AttrInputTokens   = "gen_ai.usage.input_tokens"
	AttrOutputTokens  = "gen_ai.usage.output_tokens"
	// AttrUsagePrefix matches every usage attribute, including the vendor
	// extensions the conventions have not standardised yet -- cached input
	// tokens above all, which cost a fraction of a fresh one and therefore
	// change the cost of any agent with a long, reused system prompt.
	AttrUsagePrefix        = "gen_ai.usage."
	AttrFinishReasons      = "gen_ai.response.finish_reasons"
	AttrConversationID     = "gen_ai.conversation.id"
	AttrAgentName          = "gen_ai.agent.name"
	AttrToolName           = "gen_ai.tool.name"
	AttrSystemInstructions = "gen_ai.system_instructions"
	AttrInputMessages      = "gen_ai.input.messages"
	AttrOutputMessages     = "gen_ai.output.messages"
	AttrToolCallArguments  = "gen_ai.tool.call.arguments"
	AttrToolCallResult     = "gen_ai.tool.call.result"

	AttrServiceName = "service.name"
)

// GenAI operation names that carry meaning for evaluation.
const (
	OpInvokeAgent = "invoke_agent"
	OpChat        = "chat"
	OpExecuteTool = "execute_tool"
)

// StatusCode mirrors the three OTLP status values.
type StatusCode string

const (
	StatusUnset StatusCode = "unset"
	StatusOK    StatusCode = "ok"
	StatusError StatusCode = "error"
)

// Content holds the parts of a span that may contain prompts, completions and
// tool payloads.
//
// Separated from Span because it is optional and expensive. The GenAI
// conventions make content recording opt-in precisely because these fields can
// carry personal data; keeping them in their own type means the store can put
// them in their own table and a future retention policy has something to
// delete without touching the metrics.
type Content struct {
	SystemInstructions string
	InputMessages      string
	OutputMessages     string
	ToolArguments      string
	ToolResult         string
}

// IsEmpty reports whether there is anything worth persisting.
func (c Content) IsEmpty() bool {
	return c.SystemInstructions == "" &&
		c.InputMessages == "" &&
		c.OutputMessages == "" &&
		c.ToolArguments == "" &&
		c.ToolResult == ""
}

// Span is one unit of work reported by an instrumented agent.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string

	ServiceName string
	Name        string
	Kind        string

	StartTime time.Time
	EndTime   time.Time

	StatusCode    StatusCode
	StatusMessage string

	// GenAI fields. Zero values mean "the span did not report this", which is
	// the normal case for spans that are not model or tool calls.
	Operation     string
	Provider      string
	RequestModel  string
	ResponseModel string
	InputTokens   int64
	OutputTokens  int64
	// Usage keeps every reported usage counter by its short name ("input",
	// "output", "cache_read_input_tokens", ...). Two integer columns cannot
	// express a provider that bills cached, reasoning and fresh tokens at
	// different rates, and the set of names is still growing.
	Usage map[string]int64
	// Cost in USD, when a pricing table covered the model. Zero means "not
	// priced", which is deliberately distinguishable from "free" by Priced.
	Cost           float64
	Priced         bool
	FinishReasons  string
	ConversationID string
	AgentName      string
	ToolName       string

	Content Content

	// Attributes carries every attribute as JSON, including the ones already
	// promoted to fields above. Promotion is for querying; the raw map is kept
	// so an attribute this version does not understand is not lost. A future
	// convention change can then backfill from stored data instead of asking
	// everyone to re-run their agents.
	Attributes map[string]any
}

// Duration is the span's wall time. Zero when the span has no end timestamp,
// which happens for spans exported before completion.
func (s Span) Duration() time.Duration {
	if s.StartTime.IsZero() || s.EndTime.IsZero() || s.EndTime.Before(s.StartTime) {
		return 0
	}
	return s.EndTime.Sub(s.StartTime)
}

// IsLLMCall reports whether the span represents a model invocation.
func (s Span) IsLLMCall() bool {
	return s.Operation == OpChat || s.RequestModel != "" || s.ResponseModel != ""
}

// IsToolCall reports whether the span represents a tool invocation.
func (s Span) IsToolCall() bool {
	return s.Operation == OpExecuteTool || s.ToolName != ""
}

// Model returns the response model when the provider reported one, falling
// back to the requested model.
//
// They differ in practice: ask for `claude-sonnet-4-5` and the response names
// the resolved snapshot. Cost and quality belong to what actually ran, so the
// response wins whenever it is present.
func (s Span) Model() string {
	if s.ResponseModel != "" {
		return s.ResponseModel
	}
	return s.RequestModel
}

// Trace is the rollup across every span sharing a trace ID.
//
// Derived, never ingested: spans arrive in any order, across batches, and a
// late span changes the totals. It is recomputed from the stored spans rather
// than accumulated, so a duplicate export cannot double-count tokens.
type Trace struct {
	TraceID     string
	ServiceName string
	RootSpanID  string
	RootName    string
	AgentName   string

	StartTime time.Time
	EndTime   time.Time

	SpanCount  int
	ErrorCount int
	LLMCalls   int
	ToolCalls  int

	InputTokens  int64
	OutputTokens int64
	Usage        map[string]int64
	Cost         float64
	Priced       bool

	// SessionID groups traces belonging to one multi-turn interaction, taken
	// from gen_ai.conversation.id. A single agent conversation is many traces;
	// without this they are unrelated rows.
	SessionID string
}

// Duration is the wall time from the earliest span start to the latest end.
func (t Trace) Duration() time.Duration {
	if t.StartTime.IsZero() || t.EndTime.IsZero() || t.EndTime.Before(t.StartTime) {
		return 0
	}
	return t.EndTime.Sub(t.StartTime)
}

// Failed reports whether any span in the trace ended in error.
func (t Trace) Failed() bool { return t.ErrorCount > 0 }

// Rollup computes the trace summary for a set of spans that share a trace ID.
// Returns the zero Trace when spans is empty.
func Rollup(spans []Span) Trace {
	if len(spans) == 0 {
		return Trace{}
	}

	// A span is the root when it has no parent, or when its parent was never
	// exported. The second case is common: an agent instrumented downstream of
	// an uninstrumented caller reports a parent ID nothing ever sends.
	known := make(map[string]bool, len(spans))
	for _, s := range spans {
		known[s.SpanID] = true
	}

	t := Trace{TraceID: spans[0].TraceID, SpanCount: len(spans)}
	var rootStart time.Time

	for _, s := range spans {
		if t.StartTime.IsZero() || (!s.StartTime.IsZero() && s.StartTime.Before(t.StartTime)) {
			t.StartTime = s.StartTime
		}
		if s.EndTime.After(t.EndTime) {
			t.EndTime = s.EndTime
		}

		if s.StatusCode == StatusError {
			t.ErrorCount++
		}
		if s.IsLLMCall() {
			t.LLMCalls++
		}
		if s.IsToolCall() {
			t.ToolCalls++
		}

		t.InputTokens += s.InputTokens
		t.OutputTokens += s.OutputTokens

		for name, n := range s.Usage {
			if t.Usage == nil {
				t.Usage = map[string]int64{}
			}
			t.Usage[name] += n
		}
		if s.Priced {
			t.Cost += s.Cost
			t.Priced = true
		}

		if s.ConversationID != "" && t.SessionID == "" {
			t.SessionID = s.ConversationID
		}

		if s.ServiceName != "" && t.ServiceName == "" {
			t.ServiceName = s.ServiceName
		}
		if s.AgentName != "" && t.AgentName == "" {
			t.AgentName = s.AgentName
		}

		// Prefer the earliest root when instrumentation reports several, so the
		// summary does not depend on the order spans happened to arrive in.
		isRoot := s.ParentSpanID == "" || !known[s.ParentSpanID]
		if isRoot && (t.RootSpanID == "" || s.StartTime.Before(rootStart)) {
			t.RootSpanID = s.SpanID
			t.RootName = s.Name
			rootStart = s.StartTime
		}
	}

	return t
}
