package otlp

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// idFields are the OTLP fields typed as `bytes` in the protobuf schema but
// written as hex in OTLP/JSON. Both spellings appear: protojson accepts the
// original proto field name as well as the lowerCamelCase one.
var idFields = map[string]bool{
	"traceId":  true,
	"trace_id": true,
	"spanId":   true,
	"span_id":  true,

	"parentSpanId":   true,
	"parent_span_id": true,
}

// normalizeJSONIDs rewrites hex-encoded trace and span IDs to base64.
//
// This exists because of a genuine incompatibility, not a preference. The
// OTLP/JSON specification requires trace and span IDs to be hex strings, while
// `bytes` fields in canonical protobuf JSON are base64. protojson implements
// the protobuf rule, so it decodes "0a0b0c..." as base64 and yields a
// different, valid-looking ID.
//
// The failure is silent and total: nothing errors, every ID is wrong, and
// spans stop joining to their parents. Found by sending a real OTLP/JSON
// payload with curl and watching the stored trace ID come back mangled.
//
// IDs that are not valid hex are left untouched, so a payload that already used
// base64 still decodes.
func normalizeJSONIDs(payload []byte) ([]byte, error) {
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	converted := walkJSON(root)

	out, err := json.Marshal(converted)
	if err != nil {
		return nil, fmt.Errorf("re-encode json: %w", err)
	}
	return out, nil
}

func walkJSON(node any) any {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			if idFields[key] {
				if s, ok := child.(string); ok {
					value[key] = hexToBase64(s)
					continue
				}
			}
			value[key] = walkJSON(child)
		}
		return value
	case []any:
		for i, child := range value {
			value[i] = walkJSON(child)
		}
		return value
	default:
		return node
	}
}

// hexToBase64 converts a hex ID to base64, returning the input unchanged when
// it is not hex. Trace IDs are 16 bytes and span IDs 8, so a valid hex ID has
// an even length; anything else is passed through for protojson to judge.
func hexToBase64(s string) string {
	if s == "" || len(s)%2 != 0 {
		return s
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return s
	}
	return base64.StdEncoding.EncodeToString(raw)
}
