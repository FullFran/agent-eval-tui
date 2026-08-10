// Package tui is the terminal interface: a list of traces, and the span tree of
// whichever one is selected.
//
// State lives in Model and changes only in Update, so every behaviour here can
// be tested by calling Update with a message — no terminal, no timing.
package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/FullFran/agent-eval-tui/internal/domain"
	"github.com/FullFran/agent-eval-tui/internal/store/sqlite"
)

// Store is what the TUI needs from persistence. An interface so Update can be
// tested against a fake with no database.
type Store interface {
	RecentTraces(ctx context.Context, limit int) ([]domain.Trace, error)
	TraceSpans(ctx context.Context, traceID string) ([]domain.Span, error)
	Sessions(ctx context.Context, limit int) ([]sqlite.Session, error)
}

type view int

const (
	viewTraces view = iota
	viewSessions
	viewDetail
)

// refreshInterval polls for new traces. The collector writes from another
// process, so there is nothing to subscribe to; polling a local SQLite file a
// few times a minute costs nothing and keeps a live agent visible.
const refreshInterval = 3 * time.Second

// listLimit caps what is loaded. The TUI is for looking at recent behaviour,
// not for scrolling a year of history — that is what SQL is for.
const listLimit = 200

// Messages.
type (
	tracesMsg   []domain.Trace
	sessionsMsg []sqlite.Session
	spansMsg    []domain.Span
	errMsg      struct{ err error }
	tickMsg     struct{}
)

// Model is the whole TUI state.
type Model struct {
	store Store

	view     view
	traces   []domain.Trace
	sessions []sqlite.Session
	spans    []domain.Span

	cursor   int
	selected string

	err  error
	quit bool

	width  int
	height int
}

// New builds a model over store.
func New(store Store) Model {
	return Model{store: store, view: viewTraces, width: 100, height: 30}
}

// Init loads the first page and starts the refresh tick.
func (m Model) Init() tea.Cmd {
	return tea.Batch(loadTraces(m.store), tick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func loadTraces(s Store) tea.Cmd {
	return func() tea.Msg {
		traces, err := s.RecentTraces(context.Background(), listLimit)
		if err != nil {
			return errMsg{err}
		}
		return tracesMsg(traces)
	}
}

func loadSessions(s Store) tea.Cmd {
	return func() tea.Msg {
		sessions, err := s.Sessions(context.Background(), listLimit)
		if err != nil {
			return errMsg{err}
		}
		return sessionsMsg(sessions)
	}
}

func loadSpans(s Store, traceID string) tea.Cmd {
	return func() tea.Msg {
		spans, err := s.TraceSpans(context.Background(), traceID)
		if err != nil {
			return errMsg{err}
		}
		return spansMsg(spans)
	}
}

// Update handles one message. Every state transition in the TUI goes through
// here, which is what makes the behaviour testable without a terminal.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tracesMsg:
		m.traces = msg
		m.err = nil
		m.cursor = clamp(m.cursor, len(msg))
		return m, nil

	case sessionsMsg:
		m.sessions = msg
		m.err = nil
		m.cursor = clamp(m.cursor, len(msg))
		return m, nil

	case spansMsg:
		m.spans = msg
		m.err = nil
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil

	case tickMsg:
		// The detail view is deliberately not refreshed: a finished trace does
		// not change, and reloading under the cursor while someone is reading
		// is worse than slightly stale data.
		switch m.view {
		case viewTraces:
			return m, tea.Batch(loadTraces(m.store), tick())
		case viewSessions:
			return m, tea.Batch(loadSessions(m.store), tick())
		default:
			return m, tick()
		}

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quit = true
		return m, tea.Quit

	case "esc", "backspace", "h", "left":
		if m.view == viewDetail {
			m.view = viewTraces
			m.spans = nil
			return m, loadTraces(m.store)
		}
		return m, nil

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil

	case "down", "j":
		if m.cursor < m.listLen()-1 {
			m.cursor++
		}
		return m, nil

	case "g", "home":
		m.cursor = 0
		return m, nil

	case "G", "end":
		m.cursor = max(0, m.listLen()-1)
		return m, nil

	case "enter", "l", "right":
		if m.view == viewTraces && m.cursor < len(m.traces) {
			m.selected = m.traces[m.cursor].TraceID
			m.view = viewDetail
			return m, loadSpans(m.store, m.selected)
		}
		return m, nil

	case "s":
		// One key toggles the two lists. A second nav mode would be a menu, and
		// a menu is what this tool exists to avoid.
		if m.view == viewSessions {
			m.view = viewTraces
			m.cursor = 0
			return m, loadTraces(m.store)
		}
		m.view = viewSessions
		m.cursor = 0
		return m, loadSessions(m.store)

	case "r":
		switch m.view {
		case viewSessions:
			return m, loadSessions(m.store)
		case viewDetail:
			return m, loadSpans(m.store, m.selected)
		default:
			return m, loadTraces(m.store)
		}
	}

	return m, nil
}

func (m Model) listLen() int {
	switch m.view {
	case viewSessions:
		return len(m.sessions)
	case viewTraces:
		return len(m.traces)
	default:
		return 0
	}
}

// SelectedTraceID exposes what the cursor is on, for tests and for handing off
// to a shell command.
func (m Model) SelectedTraceID() string { return m.selected }

func clamp(cursor, length int) int {
	if length == 0 {
		return 0
	}
	if cursor >= length {
		return length - 1
	}
	if cursor < 0 {
		return 0
	}
	return cursor
}
