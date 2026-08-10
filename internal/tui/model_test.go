package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/FullFran/agent-eval-tui/internal/domain"
	"github.com/FullFran/agent-eval-tui/internal/store/sqlite"
)

type fakeStore struct {
	traces   []domain.Trace
	spans    []domain.Span
	sessions []sqlite.Session
	err      error
}

func (f fakeStore) RecentTraces(context.Context, int) ([]domain.Trace, error) {
	return f.traces, f.err
}
func (f fakeStore) TraceSpans(context.Context, string) ([]domain.Span, error) {
	return f.spans, f.err
}
func (f fakeStore) Sessions(context.Context, int) ([]sqlite.Session, error) {
	return f.sessions, f.err
}

func threeTraces() []domain.Trace {
	base := time.Now().Add(-time.Minute)
	return []domain.Trace{
		{TraceID: "aaa", RootName: "invoke_agent one", StartTime: base, EndTime: base.Add(time.Second)},
		{TraceID: "bbb", RootName: "invoke_agent two", StartTime: base, EndTime: base.Add(time.Second), ErrorCount: 1},
		{TraceID: "ccc", RootName: "invoke_agent three", StartTime: base, EndTime: base.Add(time.Second)},
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// update applies a sequence of messages and returns the final model.
func update(m Model, msgs ...tea.Msg) Model {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func TestCursorMovement(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want int
	}{
		{"starts at the top", nil, 0},
		{"down moves one", []string{"j"}, 1},
		{"stops at the last row", []string{"j", "j", "j", "j", "j"}, 2},
		{"up stops at the first row", []string{"k", "k"}, 0},
		{"down then up returns", []string{"j", "j", "k"}, 1},
		{"G jumps to the end", []string{"G"}, 2},
		{"g returns to the top", []string{"G", "g"}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := update(New(fakeStore{}), tracesMsg(threeTraces()))
			for _, k := range tt.keys {
				m = update(m, key(k))
			}
			if m.cursor != tt.want {
				t.Errorf("cursor = %d, want %d", m.cursor, tt.want)
			}
		})
	}
}

func TestEnterOpensSelectedTrace(t *testing.T) {
	m := update(New(fakeStore{}), tracesMsg(threeTraces()), key("j"), key("enter"))

	if m.view != viewDetail {
		t.Errorf("view = %v, want detail", m.view)
	}
	if m.SelectedTraceID() != "bbb" {
		t.Errorf("selected = %q, want bbb", m.SelectedTraceID())
	}
}

func TestEscapeReturnsToTheList(t *testing.T) {
	m := update(New(fakeStore{}), tracesMsg(threeTraces()), key("enter"), key("esc"))

	if m.view != viewTraces {
		t.Errorf("view = %v, want traces", m.view)
	}
	if m.spans != nil {
		t.Error("spans should be cleared on the way back, or the next trace flashes stale content")
	}
}

func TestSTogglesSessionsAndBack(t *testing.T) {
	m := update(New(fakeStore{}), tracesMsg(threeTraces()), key("j"))

	m = update(m, key("s"))
	if m.view != viewSessions {
		t.Fatalf("view = %v, want sessions", m.view)
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d; it must reset, or it points at a row the other list may not have", m.cursor)
	}

	m = update(m, key("s"))
	if m.view != viewTraces {
		t.Errorf("view = %v, want traces", m.view)
	}
}

// Enter on an empty list must not open a detail view for a trace that is not
// there. Reaching for traces[cursor] unguarded would panic.
func TestEnterOnEmptyListDoesNothing(t *testing.T) {
	m := update(New(fakeStore{}), tracesMsg(nil), key("enter"))

	if m.view != viewTraces {
		t.Errorf("view = %v, want traces", m.view)
	}
	if m.SelectedTraceID() != "" {
		t.Errorf("selected = %q, want empty", m.SelectedTraceID())
	}
}

// A refresh that returns fewer traces than before must not leave the cursor
// pointing past the end.
func TestCursorClampsWhenTheListShrinks(t *testing.T) {
	m := update(New(fakeStore{}), tracesMsg(threeTraces()), key("G"))
	if m.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", m.cursor)
	}

	m = update(m, tracesMsg(threeTraces()[:1]))
	if m.cursor != 0 {
		t.Errorf("cursor = %d after shrinking to one row, want 0", m.cursor)
	}
}

func TestErrorIsShownAndClearedOnSuccess(t *testing.T) {
	m := update(New(fakeStore{}), errMsg{errors.New("database is locked")})
	if m.err == nil {
		t.Fatal("error should be recorded")
	}
	if got := m.View(); !contains(got, "database is locked") {
		t.Errorf("view should show the error, got:\n%s", got)
	}

	m = update(m, tracesMsg(threeTraces()))
	if m.err != nil {
		t.Error("a successful load must clear the previous error")
	}
}

// The detail view must not reload under the cursor: a finished trace does not
// change, and refreshing while someone is reading it is worse than stale data.
func TestTickDoesNotReloadTheDetailView(t *testing.T) {
	m := update(New(fakeStore{}), tracesMsg(threeTraces()), key("enter"))

	next, cmd := m.Update(tickMsg{})
	m = next.(Model)

	if m.view != viewDetail {
		t.Errorf("view = %v, want detail", m.view)
	}
	if cmd == nil {
		t.Error("the tick must reschedule itself, or refresh stops for the rest of the session")
	}
}

func TestQuitSetsTheFlag(t *testing.T) {
	m := update(New(fakeStore{}), key("q"))
	if !m.quit {
		t.Error("q should quit")
	}
	if m.View() != "" {
		t.Error("a quitting model should render nothing, or the last frame is left on screen")
	}
}

// The empty state is the first thing a new user sees, so it has to say what to
// do rather than just that there is nothing.
func TestEmptyStateExplainsHowToConnect(t *testing.T) {
	got := New(fakeStore{}).View()
	for _, want := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "4318"} {
		if !contains(got, want) {
			t.Errorf("empty state should mention %q, got:\n%s", want, got)
		}
	}
}

func TestWindowKeepsCursorVisible(t *testing.T) {
	many := make([]domain.Trace, 50)
	for i := range many {
		many[i] = domain.Trace{TraceID: string(rune('a' + i%26)), RootName: "t"}
	}

	m := New(fakeStore{})
	m = update(m, tea.WindowSizeMsg{Width: 100, Height: 17}, tracesMsg(many), key("G"))

	start := m.windowStart(len(many))
	if m.cursor < start || m.cursor >= start+m.rows() {
		t.Errorf("cursor %d outside window [%d,%d)", m.cursor, start, start+m.rows())
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// Renders a populated list and detail view. Asserts the columns a user relies
// on are present, and prints the frame under -v so a human can look at it
// without a terminal.
func TestRenderedFrames(t *testing.T) {
	base := time.Now().Add(-90 * time.Second)
	traces := []domain.Trace{{
		TraceID: "0a0b0c0d", RootName: "invoke_agent soporte", ServiceName: "hagabox",
		StartTime: base, EndTime: base.Add(4500 * time.Millisecond),
		SpanCount: 3, LLMCalls: 1, ToolCalls: 1, ErrorCount: 1,
		InputTokens: 2480, OutputTokens: 512, Cost: 0.02052, Priced: true,
	}}
	spans := []domain.Span{
		{TraceID: "0a0b0c0d", SpanID: "1", Name: "invoke_agent soporte", ServiceName: "hagabox",
			Operation: "invoke_agent", StartTime: base, EndTime: base.Add(4500 * time.Millisecond)},
		{TraceID: "0a0b0c0d", SpanID: "2", ParentSpanID: "1", Name: "chat", Operation: "chat",
			ResponseModel: "claude-sonnet-4-5", InputTokens: 2480, OutputTokens: 512,
			Cost: 0.02052, Priced: true,
			StartTime: base.Add(200 * time.Millisecond), EndTime: base.Add(2100 * time.Millisecond)},
		{TraceID: "0a0b0c0d", SpanID: "3", ParentSpanID: "1", Name: "execute_tool",
			Operation: "execute_tool", ToolName: "buscar_pedido",
			StatusCode: domain.StatusError, StatusMessage: "upstream 504",
			StartTime: base.Add(2200 * time.Millisecond), EndTime: base.Add(3000 * time.Millisecond)},
	}

	store := fakeStore{traces: traces, spans: spans}

	list := update(New(store), tea.WindowSizeMsg{Width: 100, Height: 24}, tracesMsg(traces)).View()
	t.Logf("\n%s", list)
	for _, want := range []string{"invoke_agent soporte", "$0.0205", "TOKENS"} {
		if !contains(list, want) {
			t.Errorf("list view missing %q", want)
		}
	}

	detail := update(New(store), tea.WindowSizeMsg{Width: 100, Height: 24},
		tracesMsg(traces), key("enter"), spansMsg(spans)).View()
	t.Logf("\n%s", detail)
	for _, want := range []string{"claude-sonnet-4-5", "buscar_pedido", "upstream 504", "hagabox"} {
		if !contains(detail, want) {
			t.Errorf("detail view missing %q", want)
		}
	}
}
