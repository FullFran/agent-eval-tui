package otlp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/FullFran/agent-eval-tui/internal/domain"
)

// Claude Code event names, without the "claude_code." prefix it puts in the
// log body. See https://code.claude.com/docs/en/monitoring-usage.
const (
	ccAPIRequest = "api_request"
	ccAPIError   = "api_error"
	ccToolResult = "tool_result"

	ccEventPrefix = "claude_code."
)

// Claude Code attribute names this translator reads.
const (
	ccAttrEventName = "event.name"
	ccAttrSession   = "session.id"
	ccAttrSequence  = "event.sequence"
	ccAttrModel     = "model"
	ccAttrDuration  = "duration_ms"
	ccAttrCost      = "cost_usd"
	ccAttrRequestID = "request_id"
	ccAttrToolName  = "tool_name"
	ccAttrToolUseID = "tool_use_id"
	ccAttrSuccess   = "success"
	ccAttrErrorType = "error_type"
	ccAttrError     = "error"
	ccAttrAgentName = "agent.name"

	// AttrRun names one execution. The runner sets it so that a run is one
	// trace even if the agent starts more than one session.
	AttrRun = "agent_eval.run"
)

// ccUsage are the token counters an api_request event carries. Named as the
// event names them; the pricing table already knows these spellings.
var ccUsage = []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens"}

// identityAttrs are dropped before storage. They identify the person and the
// account, which evaluating a harness never needs, and the database is a file
// that gets copied around.
var identityAttrs = map[string]bool{
	"user.email":        true,
	"user.id":           true,
	"user.account_uuid": true,
	"user.account_id":   true,
	"organization.id":   true,
}

// TranslateLogs turns Claude Code log events into spans.
//
// Claude Code does not emit GenAI spans: it reports each model request and
// each tool result as a log event with no trace or span id. Rebuilding them as
// spans, one trace per run, lets everything downstream (rollup, cost,
// sessions, compare) work unchanged instead of growing a second data model.
//
// Only the events that describe work become spans: api_request, api_error and
// tool_result. Configuration chatter (hook_registered, plugin_loaded, ...) is
// ignored. A record that names neither a run nor a session is dropped, because
// there is no trace it could honestly belong to.
func TranslateLogs(resourceLogs []*logspb.ResourceLogs) []domain.Span {
	var out []domain.Span
	for _, rl := range resourceLogs {
		if rl == nil {
			continue
		}
		resource := map[string]*commonpb.AnyValue{}
		if rl.Resource != nil {
			for _, kv := range rl.Resource.Attributes {
				resource[kv.GetKey()] = kv.GetValue()
			}
		}
		for _, sl := range rl.ScopeLogs {
			if sl == nil {
				continue
			}
			for _, rec := range sl.LogRecords {
				if rec == nil {
					continue
				}
				if span, ok := translateLogRecord(rec, resource); ok {
					out = append(out, span)
				}
			}
		}
	}
	return out
}

func translateLogRecord(rec *logspb.LogRecord, resource map[string]*commonpb.AnyValue) (domain.Span, bool) {
	// Record attributes win over resource attributes, as in the OTel model.
	attrs := make(map[string]*commonpb.AnyValue, len(resource)+len(rec.Attributes))
	for k, v := range resource {
		attrs[k] = v
	}
	for _, kv := range rec.Attributes {
		attrs[kv.GetKey()] = kv.GetValue()
	}
	get := func(key string) string { return anyValueString(attrs[key]) }

	event := eventName(rec, get(ccAttrEventName))
	if event != ccAPIRequest && event != ccAPIError && event != ccToolResult {
		return domain.Span{}, false
	}

	runKey := get(AttrRun)
	if runKey == "" {
		runKey = get(ccAttrSession)
	}
	if runKey == "" {
		return domain.Span{}, false
	}

	end := unixNano(rec.GetTimeUnixNano())
	if end.IsZero() {
		end = unixNano(rec.GetObservedTimeUnixNano())
	}
	start := end.Add(-time.Duration(anyValueInt(attrs[ccAttrDuration])) * time.Millisecond)

	traceID := digest(16, "claude-code-run", runKey)
	span := domain.Span{
		TraceID: traceID,
		SpanID: digest(8, traceID, event, get(ccAttrRequestID), get(ccAttrToolUseID),
			fmt.Sprint(rec.GetTimeUnixNano()), get(ccAttrSequence)),
		ServiceName:    get(domain.AttrServiceName),
		Name:           ccEventPrefix + event,
		Kind:           "internal",
		StartTime:      start,
		EndTime:        end,
		StatusCode:     domain.StatusOK,
		ConversationID: get(ccAttrSession),
		AgentName:      get(ccAttrAgentName),
		CaseID:         get(domain.AttrCase),
		Variant:        get(domain.AttrVariant),
		Attributes:     make(map[string]any, len(attrs)),
	}
	for k, v := range attrs {
		if !identityAttrs[k] {
			span.Attributes[k] = anyValueNative(v)
		}
	}

	switch event {
	case ccAPIRequest:
		span.Operation = domain.OpChat
		span.Provider = "anthropic"
		span.RequestModel = get(ccAttrModel)
		for _, name := range ccUsage {
			if n := anyValueInt(attrs[name]); n != 0 {
				if span.Usage == nil {
					span.Usage = map[string]int64{}
				}
				span.Usage[name] = n
			}
		}
		span.InputTokens = span.Usage["input_tokens"]
		span.OutputTokens = span.Usage["output_tokens"]
		// What the agent says the request cost. It prices cache tiers (5 min
		// against 1 h writes) that a generic table cannot tell apart.
		if v, ok := attrs[ccAttrCost]; ok {
			if cost, ok := anyValueFloat(v); ok {
				span.Cost = cost
				span.Priced = true
			}
		}
	case ccAPIError:
		// Not an LLM call: no model output came back. The model stays in the
		// attributes. Marked as an error even if a retry later succeeded,
		// because the span describes this attempt.
		span.StatusCode = domain.StatusError
		span.StatusMessage = get(ccAttrError)
	case ccToolResult:
		span.Operation = domain.OpExecuteTool
		span.ToolName = get(ccAttrToolName)
		if get(ccAttrSuccess) == "false" {
			span.StatusCode = domain.StatusError
			span.StatusMessage = get(ccAttrErrorType)
		}
	}
	return span, true
}

// eventName finds the event's name: the event.name attribute Claude Code
// sets, then the LogRecord field newer SDKs use, then the log body.
func eventName(rec *logspb.LogRecord, attr string) string {
	name := attr
	if name == "" {
		name = rec.GetEventName()
	}
	if name == "" {
		name = anyValueString(rec.GetBody())
	}
	return strings.TrimPrefix(name, ccEventPrefix)
}

// digest derives a stable hex id of n bytes. Stable because exporters retry,
// and a retried event must land on the same span instead of a new one.
func digest(n int, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:n])
}

// anyValueFloat reads a number however it was encoded.
func anyValueFloat(v *commonpb.AnyValue) (float64, bool) {
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue, true
	case *commonpb.AnyValue_IntValue:
		return float64(x.IntValue), true
	case *commonpb.AnyValue_StringValue:
		var f float64
		if _, err := fmt.Sscan(x.StringValue, &f); err == nil {
			return f, true
		}
	}
	return 0, false
}
