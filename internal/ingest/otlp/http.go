package otlp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collectorpb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/FullFran/agent-eval-tui/internal/domain"
)

// TracesPath is the OTLP/HTTP endpoint every SDK expects. An exporter
// configured with `OTEL_EXPORTER_OTLP_ENDPOINT=http://host:4318` appends it.
const TracesPath = "/v1/traces"

// maxBodyBytes caps a single export. Content recording means a batch can carry
// whole conversations, and an unbounded read is a trivial way to exhaust memory
// on the box this is sharing with the agent.
const maxBodyBytes = 32 << 20 // 32 MiB

// SpanSink receives translated spans.
//
// An interface rather than *sqlite.Store so the receiver can be tested without
// a database, and so a future sink (stdout, forwarding to a real backend) does
// not require touching this file.
type SpanSink interface {
	SaveSpans(ctx context.Context, spans []domain.Span) error
}

// Pricer turns a model and its usage into money. Optional: with no pricing
// table the tool still records everything and reports cost as unknown.
type Pricer interface {
	Cost(model string, usage map[string]int64) (float64, bool)
}

// Receiver serves the OTLP/HTTP trace endpoint.
type Receiver struct {
	sink   SpanSink
	pricer Pricer
	logger *slog.Logger
}

// NewReceiver builds a receiver writing into sink. pricer may be nil.
func NewReceiver(sink SpanSink, pricer Pricer, logger *slog.Logger) *Receiver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Receiver{sink: sink, pricer: pricer, logger: logger}
}

// price fills in cost at ingest time rather than at read time.
//
// Prices change, and a trace should be worth what it cost when it ran, not what
// the same tokens would cost today. Re-pricing history on every read would make
// yesterday's comparison move under you.
func (r *Receiver) price(spans []domain.Span) {
	if r.pricer == nil {
		return
	}
	for i := range spans {
		model := spans[i].Model()
		if model == "" || len(spans[i].Usage) == 0 {
			continue
		}
		if cost, ok := r.pricer.Cost(model, spans[i].Usage); ok {
			spans[i].Cost = cost
			spans[i].Priced = true
		}
	}
}

// Handler returns the HTTP routes.
func (r *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+TracesPath, r.handleTraces)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	return mux
}

func (r *Receiver) handleTraces(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxBodyBytes))
	if err != nil {
		r.logger.Warn("otlp: read body", "error", err)
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	contentType := req.Header.Get("Content-Type")
	var export collectorpb.ExportTraceServiceRequest

	// Both encodings are in the wild. Go and Python SDKs default to protobuf;
	// browser and edge SDKs, and anything hand-rolled with curl, send JSON.
	// Rejecting either would mean "framework-agnostic, except for yours".
	switch {
	case strings.HasPrefix(contentType, "application/x-protobuf"),
		strings.HasPrefix(contentType, "application/protobuf"):
		err = proto.Unmarshal(body, &export)
	case strings.HasPrefix(contentType, "application/json"):
		err = unmarshalJSON(body, &export)
	default:
		// No usable Content-Type: try protobuf, then JSON, before giving up.
		if perr := proto.Unmarshal(body, &export); perr != nil {
			err = unmarshalJSON(body, &export)
			if err != nil {
				err = fmt.Errorf("neither protobuf nor json: %w", errors.Join(perr, err))
			}
		}
	}

	if err != nil {
		r.logger.Warn("otlp: decode", "error", err, "content_type", contentType)
		http.Error(w, "cannot decode payload", http.StatusBadRequest)
		return
	}

	spans := Translate(export.GetResourceSpans())
	if len(spans) == 0 {
		writeSuccess(w)
		return
	}
	r.price(spans)

	// Not req.Context(): an exporter that hangs up mid-write would otherwise
	// cancel the transaction and lose a batch that arrived intact.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 30*time.Second)
	defer cancel()

	if err := r.sink.SaveSpans(ctx, spans); err != nil {
		r.logger.Error("otlp: persist", "error", err, "spans", len(spans))
		// 503, not 500: OTLP exporters retry on 503 and drop on 500. A failed
		// write here is transient (locked database, disk hiccup), so asking for
		// a retry is the honest answer.
		http.Error(w, "cannot persist spans", http.StatusServiceUnavailable)
		return
	}

	r.logger.Debug("otlp: stored", "spans", len(spans))
	writeSuccess(w)
}

// writeSuccess sends the empty ExportTraceServiceResponse the protocol expects.
// An empty JSON object is a valid encoding of it, and avoids marshalling a
// message with no fields set on every request.
func writeSuccess(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "{}")
}

// unmarshalJSON decodes an OTLP/JSON export.
//
// It normalises hex IDs to base64 first: OTLP/JSON specifies hex for trace and
// span IDs, protojson expects base64 for `bytes` fields, and the mismatch
// corrupts every ID without raising an error. See normalizeJSONIDs.
func unmarshalJSON(body []byte, export *collectorpb.ExportTraceServiceRequest) error {
	normalized, err := normalizeJSONIDs(body)
	if err != nil {
		return err
	}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(normalized, export)
}
