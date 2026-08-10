// Command agent-eval ingests OTLP traces from agents and stores them locally.
//
// Two subcommands for now:
//
//	agent-eval serve            receive OTLP on :4318 and persist
//	agent-eval traces           list what has been received
//	agent-eval trace <id>       print one trace as a span tree
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/FullFran/agent-eval-tui/internal/config"
	"github.com/FullFran/agent-eval-tui/internal/domain"
	"github.com/FullFran/agent-eval-tui/internal/ingest/otlp"
	"github.com/FullFran/agent-eval-tui/internal/pricing"
	"github.com/FullFran/agent-eval-tui/internal/store/sqlite"
	"github.com/FullFran/agent-eval-tui/internal/tui"
)

// defaultDB is resolved at call time rather than stored in a constant, so
// AGENT_EVAL_DB works without every command remembering to check it.
func defaultDB() string { return config.DefaultDBPath() }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// No arguments opens the TUI. Looking at what the agents did is the common
	// case, and the common case should not need a subcommand.
	if len(args) == 0 {
		return runTUI(nil)
	}

	switch args[0] {
	case "tui":
		return runTUI(args[1:])
	case "serve":
		return serve(args[1:])
	case "traces":
		return listTraces(args[1:])
	case "trace":
		return showTrace(args[1:])
	case "sessions":
		return listSessions(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `agent-eval — LLM and agent observability in one binary and one SQLite file

  agent-eval                 open the TUI
  agent-eval serve           receive OTLP on :4318

  agent-eval traces          list recent traces
  agent-eval trace <id>      print one trace as a span tree
  agent-eval sessions        list multi-turn sessions

Flags: -db <path>  -limit <n>  -addr <host:port>  -pricing <overrides.json>  -debug

Connect an agent — no code change, it already speaks OpenTelemetry:
  OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
  OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
  OTEL_SERVICE_NAME=my-agent

Database: %s
  Override with -db or AGENT_EVAL_DB.
`, defaultDB())
}

func runTUI(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "path to the SQLite database")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	if err := config.EnsureParent(*dbPath); err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	// AltScreen so the terminal is restored on exit rather than leaving the
	// last frame in the scrollback.
	program := tea.NewProgram(tui.New(store), tea.WithAltScreen())
	_, err = program.Run()
	return err
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":4318", "listen address for OTLP/HTTP")
	dbPath := fs.String("db", defaultDB(), "path to the SQLite database")
	debug := fs.Bool("debug", false, "log every stored batch")
	pricingPath := fs.String("pricing", "", "optional JSON overrides for negotiated rates; public prices are already embedded")
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := config.EnsureParent(*dbPath); err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	table, err := pricing.Load(*pricingPath)
	if err != nil {
		return err
	}
	logger.Info("pricing", "models", table.Models(), "overrides", table.Overrides())

	receiver := otlp.NewReceiver(store, table, logger)
	server := &http.Server{
		Addr:              *addr,
		Handler:           receiver.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", *addr, "db", *dbPath, "path", otlp.TracesPath)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		// Drain in-flight exports rather than cutting them off: a dropped batch
		// is data an agent already spent tokens producing.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func listTraces(args []string) error {
	fs := flag.NewFlagSet("traces", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "path to the SQLite database")
	limit := fs.Int("limit", 20, "how many traces to show")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	store, err := sqlite.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	traces, err := store.RecentTraces(ctx, *limit)
	if err != nil {
		return err
	}
	if len(traces) == 0 {
		fmt.Println("no traces yet — point an OTLP exporter at this collector and run your agent")
		return nil
	}

	fmt.Printf("%-34s %-20s %9s %5s %5s %5s %9s %10s %s\n",
		"TRACE", "ROOT", "DURATION", "SPANS", "LLM", "TOOL", "TOKENS", "COST", "STATUS")
	for _, t := range traces {
		status := "ok"
		if t.Failed() {
			status = fmt.Sprintf("%d error(s)", t.ErrorCount)
		}
		fmt.Printf("%-34s %-20s %9s %5d %5d %5d %9d %10s %s\n",
			t.TraceID, truncate(rootLabel(t), 20), t.Duration().Round(time.Millisecond),
			t.SpanCount, t.LLMCalls, t.ToolCalls,
			t.InputTokens+t.OutputTokens, money(t.Cost, t.Priced), status)
	}
	return nil
}

func listSessions(args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "path to the SQLite database")
	limit := fs.Int("limit", 20, "how many sessions to show")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	store, err := sqlite.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	sessions, err := store.Sessions(ctx, *limit)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("no sessions yet — sessions come from gen_ai.conversation.id, which the agent must set")
		return nil
	}

	fmt.Printf("%-32s %7s %6s %7s %9s %10s\n",
		"SESSION", "TRACES", "SPANS", "ERRORS", "TOKENS", "COST")
	for _, s := range sessions {
		fmt.Printf("%-32s %7d %6d %7d %9d %10s\n",
			truncate(s.SessionID, 32), s.Traces, s.Spans, s.Errors,
			s.InputTokens+s.OutputTokens, money(s.Cost, s.Priced))
	}
	return nil
}

// money renders a cost, keeping "not priced" visibly different from "free".
// A dash is the honest answer when no pricing table covered the model.
func money(cost float64, priced bool) string {
	if !priced {
		return "-"
	}
	return fmt.Sprintf("$%.4f", cost)
}

func showTrace(args []string) error {
	fs := flag.NewFlagSet("trace", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "path to the SQLite database")
	// Go's flag package stops parsing at the first non-flag argument, so
	// `trace <id> -db path` would silently ignore -db and open the default
	// database — reporting "not found" for a trace that exists. Parsing in
	// rounds, consuming one positional at a time, accepts flags on either side.
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(positional) < 1 {
		return errors.New("usage: agent-eval trace <trace-id> [-db path]")
	}
	traceID := positional[0]

	ctx := context.Background()
	store, err := sqlite.Open(ctx, *dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	spans, err := store.TraceSpans(ctx, traceID)
	if err != nil {
		return err
	}
	if len(spans) == 0 {
		return fmt.Errorf("trace %s not found", traceID)
	}

	t := domain.Rollup(spans)
	fmt.Printf("trace %s  service=%s  duration=%s  spans=%d  llm=%d  tools=%d  tokens=%d/%d\n\n",
		t.TraceID, orDash(t.ServiceName), t.Duration().Round(time.Millisecond),
		t.SpanCount, t.LLMCalls, t.ToolCalls, t.InputTokens, t.OutputTokens)

	printTree(spans)
	return nil
}

// printTree renders the span hierarchy. Spans whose parent was never exported
// are printed at the root, so an incompletely instrumented trace still shows
// everything it did report instead of hiding branches.
func printTree(spans []domain.Span) {
	byParent := make(map[string][]domain.Span)
	known := make(map[string]bool, len(spans))
	for _, s := range spans {
		known[s.SpanID] = true
	}
	for _, s := range spans {
		parent := s.ParentSpanID
		if parent != "" && !known[parent] {
			parent = ""
		}
		byParent[parent] = append(byParent[parent], s)
	}
	for parent := range byParent {
		sort.SliceStable(byParent[parent], func(i, j int) bool {
			return byParent[parent][i].StartTime.Before(byParent[parent][j].StartTime)
		})
	}

	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, s := range byParent[parent] {
			fmt.Printf("%s%s\n", strings.Repeat("  ", depth), spanLine(s))
			walk(s.SpanID, depth+1)
		}
	}
	walk("", 0)
}

func spanLine(s domain.Span) string {
	var b strings.Builder
	if s.StatusCode == domain.StatusError {
		b.WriteString("✗ ")
	} else {
		b.WriteString("• ")
	}
	b.WriteString(s.Name)

	var extras []string
	if s.Operation != "" {
		extras = append(extras, s.Operation)
	}
	if m := s.Model(); m != "" {
		extras = append(extras, m)
	}
	if s.ToolName != "" {
		extras = append(extras, "tool="+s.ToolName)
	}
	if s.InputTokens > 0 || s.OutputTokens > 0 {
		extras = append(extras, fmt.Sprintf("tokens=%d/%d", s.InputTokens, s.OutputTokens))
	}
	if d := s.Duration(); d > 0 {
		extras = append(extras, d.Round(time.Millisecond).String())
	}
	if len(extras) > 0 {
		b.WriteString("  [" + strings.Join(extras, " · ") + "]")
	}
	if s.StatusMessage != "" {
		b.WriteString("  " + s.StatusMessage)
	}
	return b.String()
}

func rootLabel(t domain.Trace) string {
	if t.RootName != "" {
		return t.RootName
	}
	if t.AgentName != "" {
		return t.AgentName
	}
	return t.ServiceName
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
