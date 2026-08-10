package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/FullFran/agent-eval-tui/internal/domain"
	"github.com/FullFran/agent-eval-tui/internal/store/sqlite"
)

// Colours are ANSI indices, not hex. A terminal theme is a decision its owner
// already made; hardcoding hex overrides it and looks wrong in half of them.
var (
	styleHeader = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleSel    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	styleTool   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleModel  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
)

// View renders the current state.
func (m Model) View() string {
	if m.quit {
		return ""
	}

	var b strings.Builder
	b.WriteString(m.title() + "\n\n")

	if m.err != nil {
		b.WriteString(styleErr.Render("error: "+m.err.Error()) + "\n\n")
	}

	switch m.view {
	case viewSessions:
		b.WriteString(m.renderSessions())
	case viewDetail:
		b.WriteString(m.renderDetail())
	default:
		b.WriteString(m.renderTraces())
	}

	b.WriteString("\n" + m.help())
	return b.String()
}

func (m Model) title() string {
	switch m.view {
	case viewSessions:
		return styleHeader.Render("agent-eval") + styleDim.Render("  ·  sessions")
	case viewDetail:
		return styleHeader.Render("agent-eval") + styleDim.Render("  ·  trace "+short(m.selected, 16))
	default:
		return styleHeader.Render("agent-eval") + styleDim.Render("  ·  traces")
	}
}

func (m Model) help() string {
	switch m.view {
	case viewDetail:
		return styleDim.Render("↑/↓ scroll · esc back · r reload · q quit")
	default:
		return styleDim.Render("↑/↓ move · enter open · s sessions/traces · r reload · q quit")
	}
}

func (m Model) renderTraces() string {
	if len(m.traces) == 0 {
		return m.emptyState()
	}

	var b strings.Builder
	b.WriteString(styleDim.Render(fmt.Sprintf(
		"  %-20s %9s %6s %5s %5s %9s %10s", "ROOT", "WHEN", "TOOK", "LLM", "TOOL", "TOKENS", "COST")) + "\n")

	for i, t := range m.window(len(m.traces)) {
		idx := m.windowStart(len(m.traces)) + i
		line := fmt.Sprintf("  %-20s %9s %6s %5d %5d %9d %10s",
			short(rootLabel(t), 20),
			ago(t.StartTime),
			compactDuration(t.Duration()),
			t.LLMCalls, t.ToolCalls,
			t.InputTokens+t.OutputTokens,
			money(t.Cost, t.Priced),
		)

		switch {
		case idx == m.cursor:
			b.WriteString(styleSel.Render("▸" + line[1:]))
		case t.Failed():
			b.WriteString(styleErr.Render(line))
		default:
			b.WriteString(line)
		}
		if t.Failed() {
			b.WriteString(styleErr.Render("  ✗"))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) renderSessions() string {
	if len(m.sessions) == 0 {
		return styleDim.Render("  no sessions yet — they come from gen_ai.conversation.id,\n  which the agent has to set.\n")
	}

	var b strings.Builder
	b.WriteString(styleDim.Render(fmt.Sprintf(
		"  %-28s %7s %6s %7s %9s %10s", "SESSION", "TRACES", "SPANS", "ERRORS", "TOKENS", "COST")) + "\n")

	for i, s := range m.windowSessions() {
		idx := m.windowStart(len(m.sessions)) + i
		line := fmt.Sprintf("  %-28s %7d %6d %7d %9d %10s",
			short(s.SessionID, 28), s.Traces, s.Spans, s.Errors,
			s.InputTokens+s.OutputTokens, money(s.Cost, s.Priced))

		if idx == m.cursor {
			b.WriteString(styleSel.Render("▸" + line[1:]))
		} else if s.Errors > 0 {
			b.WriteString(styleErr.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) renderDetail() string {
	if len(m.spans) == 0 {
		return styleDim.Render("  loading…\n")
	}

	t := domain.Rollup(m.spans)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s   %s   %s\n\n",
		styleDim.Render("service "+orDash(t.ServiceName)),
		styleDim.Render(fmt.Sprintf("%d spans · %d llm · %d tools", t.SpanCount, t.LLMCalls, t.ToolCalls)),
		styleDim.Render(fmt.Sprintf("%s · %d tokens · %s",
			compactDuration(t.Duration()), t.InputTokens+t.OutputTokens, money(t.Cost, t.Priced))),
	))
	b.WriteString(renderTree(m.spans))
	return b.String()
}

// renderTree draws the span hierarchy. Spans whose parent was never exported
// are shown at the root, so a partially instrumented trace still shows
// everything it did report rather than hiding whole branches.
func renderTree(spans []domain.Span) string {
	byParent := map[string][]domain.Span{}
	known := map[string]bool{}
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
	for k := range byParent {
		sort.SliceStable(byParent[k], func(i, j int) bool {
			return byParent[k][i].StartTime.Before(byParent[k][j].StartTime)
		})
	}

	var b strings.Builder
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, s := range byParent[parent] {
			b.WriteString("  " + strings.Repeat("│ ", depth) + spanLine(s) + "\n")
			walk(s.SpanID, depth+1)
		}
	}
	walk("", 0)
	return b.String()
}

func spanLine(s domain.Span) string {
	var b strings.Builder

	if s.StatusCode == domain.StatusError {
		b.WriteString(styleErr.Render("✗ "))
	} else {
		b.WriteString(styleOK.Render("• "))
	}
	b.WriteString(s.Name)

	var extras []string
	if model := s.Model(); model != "" {
		extras = append(extras, styleModel.Render(model))
	}
	if s.ToolName != "" {
		extras = append(extras, styleTool.Render(s.ToolName))
	}
	if s.InputTokens > 0 || s.OutputTokens > 0 {
		extras = append(extras, fmt.Sprintf("%d/%d tok", s.InputTokens, s.OutputTokens))
	}
	if s.Priced {
		extras = append(extras, money(s.Cost, true))
	}
	if d := s.Duration(); d > 0 {
		extras = append(extras, compactDuration(d))
	}
	if len(extras) > 0 {
		b.WriteString(styleDim.Render("  " + strings.Join(extras, " · ")))
	}
	if s.StatusMessage != "" {
		b.WriteString(styleErr.Render("  " + s.StatusMessage))
	}
	return b.String()
}

func (m Model) emptyState() string {
	return styleDim.Render(`  Nothing received yet.

  Point an agent at the collector:

    OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
    OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
    OTEL_SERVICE_NAME=my-agent

  This screen refreshes on its own.
`)
}

// window returns the slice of traces that fits on screen, keeping the cursor
// visible.
func (m Model) window(total int) []domain.Trace {
	start := m.windowStart(total)
	end := min(total, start+m.rows())
	return m.traces[start:end]
}

func (m Model) windowSessions() []sqlite.Session {
	start := m.windowStart(len(m.sessions))
	end := min(len(m.sessions), start+m.rows())
	return m.sessions[start:end]
}

// rows is how many list lines fit, after the title, header, help and blanks.
func (m Model) rows() int {
	r := m.height - 7
	if r < 3 {
		return 3
	}
	return r
}

func (m Model) windowStart(total int) int {
	rows := m.rows()
	if total <= rows || m.cursor < rows {
		return 0
	}
	start := m.cursor - rows + 1
	if start+rows > total {
		start = total - rows
	}
	if start < 0 {
		return 0
	}
	return start
}

func rootLabel(t domain.Trace) string {
	switch {
	case t.RootName != "":
		return t.RootName
	case t.AgentName != "":
		return t.AgentName
	default:
		return t.ServiceName
	}
}

// money keeps "not priced" visibly different from "free".
func money(cost float64, priced bool) string {
	if !priced {
		return "-"
	}
	return fmt.Sprintf("$%.4f", cost)
}

// compactDuration trades precision for width: a list is easier to scan when
// every duration is four characters than when one of them is "1.234567891s".
func compactDuration(d time.Duration) string {
	switch {
	case d == 0:
		return "-"
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 02")
	}
}

func short(s string, n int) string {
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
