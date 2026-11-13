package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/FullFran/agent-eval-tui/internal/domain"
	"github.com/FullFran/agent-eval-tui/internal/harness"
)

// The store is the harness sink without harness importing this package.
var _ harness.OutcomeSink = (*Store)(nil)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func outcome(id, caseID string, start time.Time) domain.RunOutcome {
	return domain.RunOutcome{
		RunID: id, CaseID: caseID, Variant: "v",
		AgentExit: -1, AcceptExit: 0, Accepted: true,
		StartTime: start, EndTime: start.Add(1500 * time.Millisecond),
	}
}

func TestRunsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	o := outcome("r1", "c", time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC))
	o.AgentExit, o.AcceptExit, o.Accepted, o.Variant = 3, 1, false, "variant-a"
	if err := s.SaveRun(ctx, o); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunOutcomes(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], o) {
		t.Errorf("got %+v, want %+v", got, o)
	}
}

func TestRunsUpsert(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	o := outcome("r1", "c", start)
	if err := s.SaveRun(ctx, o); err != nil {
		t.Fatal(err)
	}
	o.Accepted, o.AcceptExit = false, 2
	if err := s.SaveRun(ctx, o); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunOutcomes(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], o) {
		t.Errorf("got %+v, want one row %+v", got, o)
	}
}

func TestRunOutcomesFiltersAndOrders(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, o := range []domain.RunOutcome{
		outcome("late", "a", base.Add(2*time.Second)),
		outcome("other", "b", base.Add(time.Second)),
		outcome("early", "a", base),
	} {
		if err := s.SaveRun(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RunOutcomes(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RunID != "early" || got[1].RunID != "late" {
		t.Errorf("got %+v, want early then late", got)
	}
}
