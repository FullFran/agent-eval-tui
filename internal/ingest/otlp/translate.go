// Package otlp turns OTLP trace payloads into the domain model.
//
// This is the only package that knows OTLP exists. Everything downstream sees
// domain.Span.
package otlp

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/FullFran/agent-eval-tui/internal/domain"
)

// Translate flattens an OTLP export request into spans.
//
// Resource attributes are merged down onto every span rather than stored
// alongside: `service.name` lives on the resource, but every question worth
// asking ("which service produced this trace") is asked of a span.
func Translate(resourceSpans []*tracepb.ResourceSpans) []domain.Span {
	var out []domain.Span

	for _, rs := range resourceSpans {
		if rs == nil {
			continue
		}

		serviceName := ""
		if rs.Resource != nil {
			for _, kv := range rs.Resource.Attributes {
				if kv.GetKey() == domain.AttrServiceName {
					serviceName = anyValueString(kv.GetValue())
				}
			}
		}

		for _, ss := range rs.ScopeSpans {
			if ss == nil {
				continue
			}
			for _, s := range ss.Spans {
				if s == nil {
					continue
				}
				out = append(out, translateSpan(s, serviceName))
			}
		}
	}

	return out
}

func translateSpan(s *tracepb.Span, serviceName string) domain.Span {
	span := domain.Span{
		TraceID:     hex.EncodeToString(s.GetTraceId()),
		SpanID:      hex.EncodeToString(s.GetSpanId()),
		ServiceName: serviceName,
		Name:        s.GetName(),
		Kind:        spanKind(s.GetKind()),
		StartTime:   unixNano(s.GetStartTimeUnixNano()),
		EndTime:     unixNano(s.GetEndTimeUnixNano()),
		StatusCode:  domain.StatusUnset,
		Attributes:  map[string]any{},
	}

	// An all-zero parent ID means "no parent". Exporters send eight zero bytes
	// rather than an empty field, so hex-encoding it yields "0000000000000000"
	// and a root span would look like a child of a span that does not exist.
	if parent := s.GetParentSpanId(); len(parent) > 0 && !allZero(parent) {
		span.ParentSpanID = hex.EncodeToString(parent)
	}

	if st := s.GetStatus(); st != nil {
		switch st.GetCode() {
		case tracepb.Status_STATUS_CODE_OK:
			span.StatusCode = domain.StatusOK
		case tracepb.Status_STATUS_CODE_ERROR:
			span.StatusCode = domain.StatusError
		}
		span.StatusMessage = st.GetMessage()
	}

	for _, kv := range s.GetAttributes() {
		key := kv.GetKey()
		if key == "" {
			continue
		}
		val := kv.GetValue()
		span.Attributes[key] = anyValueNative(val)
		applyGenAI(&span, key, val)
		applyUsage(&span, key, val)
	}

	return span
}

// applyGenAI promotes the GenAI attributes worth querying into typed fields.
// Unknown keys are ignored here; they are still kept in Span.Attributes.
func applyGenAI(span *domain.Span, key string, val *commonpb.AnyValue) {
	switch key {
	case domain.AttrOperationName:
		span.Operation = anyValueString(val)
	case domain.AttrSystem, domain.AttrProviderName:
		// `gen_ai.system` was renamed to `gen_ai.provider.name`. Both are in the
		// wild, so accept either and let whichever arrives populate the field.
		if span.Provider == "" {
			span.Provider = anyValueString(val)
		}
	case domain.AttrRequestModel:
		span.RequestModel = anyValueString(val)
	case domain.AttrResponseModel:
		span.ResponseModel = anyValueString(val)
	case domain.AttrInputTokens:
		span.InputTokens = anyValueInt(val)
	case domain.AttrOutputTokens:
		span.OutputTokens = anyValueInt(val)
	case domain.AttrFinishReasons:
		span.FinishReasons = anyValueString(val)
	case domain.AttrConversationID:
		span.ConversationID = anyValueString(val)
	case domain.AttrAgentName:
		span.AgentName = anyValueString(val)
	case domain.AttrToolName:
		span.ToolName = anyValueString(val)
	case domain.AttrCase:
		span.CaseID = anyValueString(val)
	case domain.AttrVariant:
		span.Variant = anyValueString(val)
	case domain.AttrSystemInstructions:
		span.Content.SystemInstructions = anyValueString(val)
	case domain.AttrInputMessages:
		span.Content.InputMessages = anyValueString(val)
	case domain.AttrOutputMessages:
		span.Content.OutputMessages = anyValueString(val)
	case domain.AttrToolCallArguments:
		span.Content.ToolArguments = anyValueString(val)
	case domain.AttrToolCallResult:
		span.Content.ToolResult = anyValueString(val)
	}
}

// anyValueString renders an OTLP value as text.
//
// Token counts arrive as ints from one SDK and as strings from another, and
// `gen_ai.response.finish_reasons` is specified as an array but emitted as a
// bare string by several exporters. Being permissive here is not sloppiness:
// the alternative is silently dropping data from real agents because their
// SDK chose a different representation of the same fact.
func anyValueString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch value := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return value.StringValue
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(value.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(value.DoubleValue, 'g', -1, 64)
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(value.BoolValue)
	case *commonpb.AnyValue_ArrayValue, *commonpb.AnyValue_KvlistValue:
		// Structured values (message arrays, tool arguments) are kept as JSON
		// rather than flattened. The TUI renders them; nothing queries inside.
		encoded, err := json.Marshal(anyValueNative(v))
		if err != nil {
			return ""
		}
		return string(encoded)
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(value.BytesValue)
	default:
		return ""
	}
}

// anyValueInt reads a numeric value, tolerating the string encoding some SDKs
// use for token counts.
func anyValueInt(v *commonpb.AnyValue) int64 {
	if v == nil {
		return 0
	}
	switch value := v.GetValue().(type) {
	case *commonpb.AnyValue_IntValue:
		return value.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return int64(value.DoubleValue)
	case *commonpb.AnyValue_StringValue:
		n, err := strconv.ParseInt(strings.TrimSpace(value.StringValue), 10, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// anyValueNative converts to plain Go types so the attribute map marshals to
// ordinary JSON instead of protobuf's wire encoding.
func anyValueNative(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch value := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return value.StringValue
	case *commonpb.AnyValue_IntValue:
		return value.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return value.DoubleValue
	case *commonpb.AnyValue_BoolValue:
		return value.BoolValue
	case *commonpb.AnyValue_BytesValue:
		return hex.EncodeToString(value.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		items := value.ArrayValue.GetValues()
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, anyValueNative(item))
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		pairs := value.KvlistValue.GetValues()
		out := make(map[string]any, len(pairs))
		for _, kv := range pairs {
			out[kv.GetKey()] = anyValueNative(kv.GetValue())
		}
		return out
	default:
		return nil
	}
}

func spanKind(k tracepb.Span_SpanKind) string {
	switch k {
	case tracepb.Span_SPAN_KIND_INTERNAL:
		return "internal"
	case tracepb.Span_SPAN_KIND_SERVER:
		return "server"
	case tracepb.Span_SPAN_KIND_CLIENT:
		return "client"
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return "producer"
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return "consumer"
	default:
		return "unspecified"
	}
}

func unixNano(ns uint64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(ns)).UTC()
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// applyUsage collects every `gen_ai.usage.*` counter into the usage map.
//
// Deliberately prefix-matched rather than enumerated. The conventions
// standardise input and output tokens, but providers already bill for cached
// input, reasoning and audio tokens under names that are not settled yet.
// Enumerating would mean a new counter is invisible until this file is edited,
// and invisible usage is a wrong cost.
func applyUsage(span *domain.Span, key string, val *commonpb.AnyValue) {
	if !strings.HasPrefix(key, domain.AttrUsagePrefix) {
		return
	}
	name := strings.TrimPrefix(key, domain.AttrUsagePrefix)
	if name == "" {
		return
	}
	n := anyValueInt(val)
	if n == 0 {
		return
	}
	if span.Usage == nil {
		span.Usage = map[string]int64{}
	}
	// Stored verbatim after the prefix: "input_tokens", "output_tokens",
	// "cache_read_input_tokens". No trimming, so what the agent emitted is what
	// the pricing table has to name, and nobody has to guess a transformation.
	span.Usage[name] = n
}
