package domain

import "time"

// RunOutcome is one execution of a case by a variant and its verdict.
//
// Accepted comes from the case's acceptance command, not from the agent's own
// exit status or its telemetry: an agent can exit 0 having done nothing, and a
// run whose spans all succeeded can still leave the task undone.
type RunOutcome struct {
	RunID      string
	CaseID     string
	Variant    string
	AgentExit  int  // -1 when the agent was killed for exceeding its timeout
	AcceptExit int  // -1 when the acceptance command was killed for exceeding its timeout
	Accepted   bool // AcceptExit == 0
	StartTime  time.Time
	EndTime    time.Time
}
