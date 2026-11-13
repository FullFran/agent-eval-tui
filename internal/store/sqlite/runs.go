package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/FullFran/agent-eval-tui/internal/domain"
)

// Upsert by run id: saving an outcome again (a resumed or repeated write)
// must replace it, not add a second verdict for the same run.
const upsertRun = `
INSERT INTO runs (run_id, case_id, variant, agent_exit, accept_exit, accepted, start_unix_nano, end_unix_nano)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
    case_id         = excluded.case_id,
    variant         = excluded.variant,
    agent_exit      = excluded.agent_exit,
    accept_exit     = excluded.accept_exit,
    accepted        = excluded.accepted,
    start_unix_nano = excluded.start_unix_nano,
    end_unix_nano   = excluded.end_unix_nano`

const selectRunsByCase = `
SELECT run_id, case_id, variant, agent_exit, accept_exit, accepted, start_unix_nano, end_unix_nano
FROM runs
WHERE case_id = ?
ORDER BY start_unix_nano ASC`

// SaveRun persists one run outcome.
func (s *Store) SaveRun(ctx context.Context, o domain.RunOutcome) error {
	_, err := s.db.ExecContext(ctx, upsertRun,
		o.RunID, o.CaseID, o.Variant, o.AgentExit, o.AcceptExit, boolToInt(o.Accepted),
		o.StartTime.UnixNano(), o.EndTime.UnixNano())
	if err != nil {
		return fmt.Errorf("save run %s: %w", o.RunID, err)
	}
	return nil
}

// RunOutcomes returns the outcomes of a case, oldest first.
func (s *Store) RunOutcomes(ctx context.Context, caseID string) ([]domain.RunOutcome, error) {
	rows, err := s.db.QueryContext(ctx, selectRunsByCase, caseID)
	if err != nil {
		return nil, fmt.Errorf("query runs of case %s: %w", caseID, err)
	}
	defer rows.Close()

	var out []domain.RunOutcome
	for rows.Next() {
		var (
			o              domain.RunOutcome
			accepted       int
			startNS, endNS int64
		)
		if err := rows.Scan(&o.RunID, &o.CaseID, &o.Variant, &o.AgentExit, &o.AcceptExit,
			&accepted, &startNS, &endNS); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		o.Accepted = accepted != 0
		o.StartTime = time.Unix(0, startNS).UTC()
		o.EndTime = time.Unix(0, endNS).UTC()
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runs: %w", err)
	}
	return out, nil
}
