package harness

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FullFran/agent-eval-tui/internal/domain"
)

// OutcomeSink receives each outcome as soon as the run finishes.
type OutcomeSink interface {
	SaveRun(ctx context.Context, o domain.RunOutcome) error
}

// Runner executes every (case, variant) pair of a spec, one run at a time.
type Runner struct {
	Endpoint      string        // OTLP/HTTP endpoint for the agent, e.g. http://127.0.0.1:4318
	AgentTimeout  time.Duration // zero means 30 minutes
	AcceptTimeout time.Duration // zero means 10 minutes
	Stdout        io.Writer     // agent and acceptance stdout/stderr go here; nil discards
}

const (
	defaultAgentTimeout  = 30 * time.Minute
	defaultAcceptTimeout = 10 * time.Minute

	// waitDelay bounds how long Wait blocks after the process is killed when a
	// grandchild still holds its output open; without it a timeout could hang.
	waitDelay = time.Second
)

// Run executes the spec sequentially in the order case, variant, run 1..N and
// hands each outcome to sink. Runs are sequential on purpose: agents share the
// machine, and concurrent runs would skew each other's timings and limits.
func (r Runner) Run(ctx context.Context, spec Spec, sink OutcomeSink) error {
	for _, c := range spec.Cases {
		for _, v := range spec.Variants {
			for n := 1; n <= spec.Runs; n++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				o, err := r.runOnce(ctx, c, v, n)
				if err != nil {
					return err
				}
				if err := sink.SaveRun(ctx, o); err != nil {
					return fmt.Errorf("save run %s: %w", o.RunID, err)
				}
			}
		}
	}
	return nil
}

// runOnce executes one run in a disposable copy of the case directory, so the
// agent can never alter the case itself or see a previous run's leftovers.
func (r Runner) runOnce(ctx context.Context, c Case, v Variant, n int) (domain.RunOutcome, error) {
	dir, err := os.MkdirTemp("", "agent-eval-run-*")
	if err != nil {
		return domain.RunOutcome{}, fmt.Errorf("create run directory: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := copyTree(c.Workdir, dir); err != nil {
		return domain.RunOutcome{}, fmt.Errorf("copy case %s: %w", c.ID, err)
	}

	start := time.Now().UTC()
	runID := fmt.Sprintf("%s.%s.%d.%d", c.ID, v.ID, n, start.UnixNano())
	env := r.environ(c, v, runID)

	argv := make([]string, len(v.Command))
	for i, a := range v.Command {
		argv[i] = strings.ReplaceAll(a, "{prompt}", c.Prompt)
	}

	agentExit, err := r.execute(ctx, orDefault(r.AgentTimeout, defaultAgentTimeout), dir, env, argv)
	if err != nil {
		return domain.RunOutcome{}, fmt.Errorf("run %s: start agent: %w", runID, err)
	}
	acceptExit, err := r.execute(ctx, orDefault(r.AcceptTimeout, defaultAcceptTimeout), dir, env,
		[]string{"bash", "-c", c.Accept})
	if err != nil {
		return domain.RunOutcome{}, fmt.Errorf("run %s: start acceptance command: %w", runID, err)
	}
	end := time.Now().UTC()

	return domain.RunOutcome{
		RunID:      runID,
		CaseID:     c.ID,
		Variant:    v.ID,
		AgentExit:  agentExit,
		AcceptExit: acceptExit,
		Accepted:   acceptExit == 0,
		StartTime:  start,
		EndTime:    end,
	}, nil
}

// environ builds the run's environment. Later entries win, so the runner's
// tags override anything the variant or the parent environment set: the run
// must be attributable regardless of the variant's configuration.
func (r Runner) environ(c Case, v Variant, runID string) []string {
	env := os.Environ()

	keys := make([]string, 0, len(v.Env))
	for k := range v.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+v.Env[k])
	}

	if r.Endpoint != "" {
		env = append(env, "OTEL_EXPORTER_OTLP_ENDPOINT="+r.Endpoint)
	}
	return append(env,
		fmt.Sprintf("OTEL_RESOURCE_ATTRIBUTES=agent_eval.case=%s,agent_eval.variant=%s,agent_eval.run=%s", c.ID, v.ID, runID),
		"AGENT_EVAL_RUN="+runID,
		"AGENT_EVAL_CASE="+c.ID,
		"AGENT_EVAL_VARIANT="+v.ID,
	)
}

// execute runs argv in dir and returns its exit status, or -1 when it was
// killed for exceeding timeout. Only a failure to start is an error.
func (r Runner) execute(ctx context.Context, timeout time.Duration, dir string, env, argv []string) (int, error) {
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(tctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = waitDelay
	if r.Stdout != nil {
		cmd.Stdout = r.Stdout
		cmd.Stderr = r.Stdout
	}

	if err := cmd.Start(); err != nil {
		return 0, err
	}
	err := cmd.Wait()
	if tctx.Err() != nil {
		return -1, nil
	}
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode(), nil
	}
	return 0, err
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// copyTree copies src into the existing directory dst, keeping file modes and
// recreating symlinks as symlinks: following them could copy files from
// outside the case, or loop.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case mode.IsDir():
			if rel == "." {
				return os.Chmod(dst, mode.Perm())
			}
			if err := os.Mkdir(target, mode.Perm()); err != nil {
				return err
			}
			// Mkdir is subject to the umask; set the exact mode afterwards.
			return os.Chmod(target, mode.Perm())
		case mode.IsRegular():
			return copyFile(path, target, mode.Perm())
		default:
			return fmt.Errorf("%s: unsupported file type %s", path, mode.Type())
		}
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}
