package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FullFran/agent-eval-tui/internal/domain"
)

type recordingSink struct {
	outcomes []domain.RunOutcome
	err      error
}

func (s *recordingSink) SaveRun(_ context.Context, o domain.RunOutcome) error {
	s.outcomes = append(s.outcomes, o)
	return s.err
}

// fakeAgent writes a bash script that records its environment, argv and
// working directory under $RECORD_DIR/<run id>, writes done.txt when WRITE=1,
// sleeps SLEEP seconds when set, and exits with EXIT (default 0).
func fakeAgent(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.sh")
	script := `#!/usr/bin/env bash
out="$RECORD_DIR/$AGENT_EVAL_RUN"
mkdir -p "$out"
pwd > "$out/pwd"
printf '%s\n' "$@" > "$out/argv"
printf '%s' "${OTEL_EXPORTER_OTLP_ENDPOINT-unset}" > "$out/endpoint"
printf '%s' "$OTEL_RESOURCE_ATTRIBUTES" > "$out/attrs"
printf '%s' "$AGENT_EVAL_RUN" > "$out/run"
printf '%s' "$EXTRA" > "$out/extra"
if [ "$WRITE" = "1" ]; then echo ok > done.txt; fi
if [ -n "$SLEEP" ]; then exec sleep "$SLEEP"; fi
exit "${EXIT:-0}"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func caseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readRecord(t *testing.T, recordDir, runID, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(recordDir, runID, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		names = append(names, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestRunnerOrderAcceptanceAndEnvironment(t *testing.T) {
	agent := fakeAgent(t)
	work := caseDir(t)
	before := listDir(t, work)
	records := t.TempDir()

	spec := Spec{
		Runs: 3,
		Cases: []Case{{
			ID: "c1", Workdir: work, Prompt: "fix it",
			Accept: "test -f done.txt && test -L link && test -f .git/HEAD",
		}},
		Variants: []Variant{
			{ID: "writes", Command: []string{agent, "--p={prompt}!", "{prompt}"},
				Env: map[string]string{"RECORD_DIR": records, "WRITE": "1", "EXTRA": "x"}},
			{ID: "idle", Command: []string{agent}, Env: map[string]string{"RECORD_DIR": records}},
		},
	}
	sink := &recordingSink{}
	r := Runner{Endpoint: "http://127.0.0.1:4318"}
	if err := r.Run(context.Background(), spec, sink); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(sink.outcomes) != 6 {
		t.Fatalf("got %d outcomes, want 6", len(sink.outcomes))
	}
	for i, o := range sink.outcomes {
		wantVariant := "writes"
		if i >= 3 {
			wantVariant = "idle"
		}
		run := i%3 + 1
		prefix := "c1." + wantVariant + "." + string(rune('0'+run)) + "."
		if o.CaseID != "c1" || o.Variant != wantVariant || !strings.HasPrefix(o.RunID, prefix) {
			t.Errorf("outcome %d = %+v, want case c1 variant %s run %d", i, o, wantVariant, run)
		}
		if o.StartTime.IsZero() || o.EndTime.Before(o.StartTime) || o.StartTime.Location() != time.UTC {
			t.Errorf("outcome %d has bad times: %v %v", i, o.StartTime, o.EndTime)
		}
		if o.AgentExit != 0 {
			t.Errorf("outcome %d AgentExit = %d", i, o.AgentExit)
		}
		if wantVariant == "writes" {
			if !o.Accepted || o.AcceptExit != 0 {
				t.Errorf("outcome %d: Accepted=%v AcceptExit=%d, want true/0", i, o.Accepted, o.AcceptExit)
			}
		} else if o.Accepted || o.AcceptExit != 1 {
			t.Errorf("outcome %d: Accepted=%v AcceptExit=%d, want false/1", i, o.Accepted, o.AcceptExit)
		}

		// The agent worked in a copy that is gone now.
		pwd := strings.TrimSpace(readRecord(t, records, o.RunID, "pwd"))
		if pwd == work {
			t.Errorf("outcome %d ran in the original directory", i)
		}
		if _, err := os.Stat(pwd); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("outcome %d: copy %s still exists (err %v)", i, pwd, err)
		}

		if got := readRecord(t, records, o.RunID, "endpoint"); got != "http://127.0.0.1:4318" {
			t.Errorf("endpoint = %q", got)
		}
		wantAttrs := "agent_eval.case=c1,agent_eval.variant=" + wantVariant + ",agent_eval.run=" + o.RunID
		if got := readRecord(t, records, o.RunID, "attrs"); got != wantAttrs {
			t.Errorf("attrs = %q, want %q", got, wantAttrs)
		}
		if got := readRecord(t, records, o.RunID, "run"); got != o.RunID {
			t.Errorf("AGENT_EVAL_RUN = %q, want %q", got, o.RunID)
		}
		if wantVariant == "writes" {
			if got := readRecord(t, records, o.RunID, "extra"); got != "x" {
				t.Errorf("variant env EXTRA = %q", got)
			}
			if got := readRecord(t, records, o.RunID, "argv"); got != "--p=fix it!\nfix it\n" {
				t.Errorf("argv = %q", got)
			}
		}
	}

	after := listDir(t, work)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("original directory changed:\nbefore %v\nafter  %v", before, after)
	}
}

func TestRunnerEmptyEndpointNotSet(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	agent := fakeAgent(t)
	records := t.TempDir()
	spec := Spec{
		Runs:     1,
		Cases:    []Case{{ID: "c", Workdir: t.TempDir(), Accept: "true"}},
		Variants: []Variant{{ID: "v", Command: []string{agent}, Env: map[string]string{"RECORD_DIR": records}}},
	}
	sink := &recordingSink{}
	if err := (Runner{}).Run(context.Background(), spec, sink); err != nil {
		t.Fatal(err)
	}
	if got := readRecord(t, records, sink.outcomes[0].RunID, "endpoint"); got != "unset" {
		t.Errorf("endpoint = %q, want unset", got)
	}
}

func TestRunnerAgentExitCodeStillAccepts(t *testing.T) {
	agent := fakeAgent(t)
	records := t.TempDir()
	marker := filepath.Join(t.TempDir(), "accepted-ran")
	spec := Spec{
		Runs:     1,
		Cases:    []Case{{ID: "c", Workdir: t.TempDir(), Accept: "touch " + marker}},
		Variants: []Variant{{ID: "v", Command: []string{agent}, Env: map[string]string{"RECORD_DIR": records, "EXIT": "3"}}},
	}
	sink := &recordingSink{}
	if err := (Runner{}).Run(context.Background(), spec, sink); err != nil {
		t.Fatal(err)
	}
	o := sink.outcomes[0]
	if o.AgentExit != 3 || !o.Accepted || o.AcceptExit != 0 {
		t.Errorf("outcome = %+v, want AgentExit 3 and accepted", o)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("acceptance command did not run: %v", err)
	}
}

func TestRunnerAgentTimeout(t *testing.T) {
	agent := fakeAgent(t)
	records := t.TempDir()
	marker := filepath.Join(t.TempDir(), "accepted-ran")
	spec := Spec{
		Runs:     1,
		Cases:    []Case{{ID: "c", Workdir: t.TempDir(), Accept: "touch " + marker}},
		Variants: []Variant{{ID: "v", Command: []string{agent}, Env: map[string]string{"RECORD_DIR": records, "SLEEP": "5"}}},
	}
	sink := &recordingSink{}
	start := time.Now()
	if err := (Runner{AgentTimeout: 200 * time.Millisecond}).Run(context.Background(), spec, sink); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("run took %v; the agent was not killed", elapsed)
	}
	o := sink.outcomes[0]
	if o.AgentExit != -1 || !o.Accepted {
		t.Errorf("outcome = %+v, want AgentExit -1 and accepted", o)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("acceptance command did not run: %v", err)
	}
}

func TestRunnerStartFailureIsError(t *testing.T) {
	spec := Spec{
		Runs:     1,
		Cases:    []Case{{ID: "c", Workdir: t.TempDir(), Accept: "true"}},
		Variants: []Variant{{ID: "v", Command: []string{filepath.Join(t.TempDir(), "no-such-agent")}}},
	}
	sink := &recordingSink{}
	if err := (Runner{}).Run(context.Background(), spec, sink); err == nil {
		t.Fatal("want error when the agent cannot start")
	}
	if len(sink.outcomes) != 0 {
		t.Errorf("got %d outcomes, want 0", len(sink.outcomes))
	}
}

func TestRunnerSinkErrorStops(t *testing.T) {
	spec := Spec{
		Runs:     3,
		Cases:    []Case{{ID: "c", Workdir: t.TempDir(), Accept: "true"}},
		Variants: []Variant{{ID: "v", Command: []string{"true"}}},
	}
	sink := &recordingSink{err: errors.New("disk full")}
	err := (Runner{}).Run(context.Background(), spec, sink)
	if err == nil || !strings.HasPrefix(err.Error(), "save run c.v.1.") || !strings.HasSuffix(err.Error(), ": disk full") {
		t.Fatalf("err = %v", err)
	}
	if len(sink.outcomes) != 1 {
		t.Errorf("got %d outcomes, want 1", len(sink.outcomes))
	}
}

func TestRunnerCancelledContext(t *testing.T) {
	spec := Spec{
		Runs:     2,
		Cases:    []Case{{ID: "c", Workdir: t.TempDir(), Accept: "true"}},
		Variants: []Variant{{ID: "v", Command: []string{"true"}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := &recordingSink{}
	if err := (Runner{}).Run(ctx, spec, sink); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(sink.outcomes) != 0 {
		t.Errorf("got %d outcomes, want 0", len(sink.outcomes))
	}
}
