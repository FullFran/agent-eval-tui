# harness-experiments

Objective: turn agent-eval-tui into the controlled-experiment bench for coding-agent harnesses (Claude Code + the metaharness profile), complementing the observational usage report in FullFran/metaharness `analysis/usage`.
Branch: `feat/harness-experiments`. Delivery: ask-on-risk. Test runner: `go test -race ./...` (plus `gofmt -l .`, `go vet ./...`, as in CI).

## Specs

- **S1 Proportions are measured as proportions.** The `success` metric uses a Wilson score interval per variant and a Wilson-based (Newcombe) interval for the difference between two variants. A sample where every run agrees (e.g. 30/30) must not be reported as "100% ± 0 pp": the interval stays non-degenerate. Latency, cost and tokens keep the existing t / Welch path, including its zero-variance behaviour.
- **S2 Ingest Claude Code telemetry.** Accept OTLP logs on `/v1/logs` (http/json and http/protobuf, same receiver as `/v1/traces`) and store the Claude Code events needed to reconstruct, per session/run: model, input/output/cache tokens, cost, duration, tool calls and errors. Runs are tagged through resource attributes (`agent_eval.case`, `agent_eval.variant`, plus a run id).
- **S3 A case is a task with an acceptance check.** A case declares a repository/fixture, a prompt and an acceptance command; the run is accepted when that command exits 0. This replaces "no span errored" as the meaning of success for harness runs (issues #1, #5).
- **S4 A variant is a harness profile.** A variant declares how the agent is launched (command, model, settings/profile, env). The runner executes each (case, variant) N times, each in a disposable copy/worktree, with telemetry pointed at the local receiver and tagged per run.
- **S5 Compare uses the outcome.** `agent-eval compare` reads accepted/rejected from S3 for `success`, and per-run cost/tokens/latency from S2, so a cheaper variant that fails more is visible as such.
- **S6 Feed the experiment register.** Document how a result maps to the keep/remove register in metaharness `analysis/usage/EXPERIMENTS.md` (task id, outcome, model/effort, profile, timestamps). The descriptive analysis of historical transcripts stays in metaharness; this repo does not duplicate it.

Constraints: real experiment runs consume Fran's Claude quota, so tests use a fake agent command and no real `claude -p` batch runs without Fran's explicit go-ahead. Generated artifacts in English.

## Tasks

| ID | Specs | Route | Status | Commit |
|----|-------|-------|--------|--------|
| T1 | S1 | inline | done (RED: build fail on missing Wilson/CompareProportions; GREEN: suite green, Newcombe 1998 published values match) | see Log L4 |
| T2 | S2 | inline | done (RED: LogsPath undefined; GREEN: suite green; e2e: real captured payload -> serve -> traces/cases/compare, cost $0.0111 = total_cost_usd, 0 identity attrs stored) | see Log L5 |
| T3 | S3, S4 | inline | pending | |
| T4 | S5 | inline | pending | |
| T5 | S6 | inline | pending | |

Route evidence: no Writer trigger (units are sequential: T3-T5 depend on T2's data), so the parent writes inline.

## Log

- L1 (2026-11-13, verbatim): "quiero retomarlo y ampliarlo o reconvertirlo para añadirlo para evaluar este tipo de harnesses como el que tenemos montado y asi de paso aprobechar para avanzar en el analisis de los datos que tenemos para optimizar el uso de claude"
- L2 (2026-11-13): proposal accepted verbatim "me cuadra!": agent-eval-tui becomes the controlled bench (cases with acceptance checks, variants = harness profiles, `/v1/logs` ingestion for Claude Code, Wilson for proportions, output to the metaharness experiment register); metaharness keeps the observational analysis.
- L3 (2026-11-13): evidence. Base `35f607f`: build, vet, 72 tests green. `internal/stats/stats.go:176` returns margin 0 when both samples have zero variance, which for 0/1 success reports 30/30 as exact. Claude Code here exports logs + metrics (not traces) over http/json to otlp.fullfran.com; the receiver only serves `/v1/traces`. Forecast ~1500-2500 authored lines across T1-T5, so delivery will exceed 400: chained PRs per task.
- L4 (2026-11-13): T1 done. `internal/stats/proportion.go` adds Wilson + Newcombe; `experiment` routes `success` through them; CLI prints asymmetric bounds as `[low, high]`, column widths unchanged so latency/cost/tokens output is byte-identical. Engram mirror PENDING: this session is bound to project `dotfiles`, write to `agent-eval-tui` refused (session_project_mismatch); resync from a session started in this repo.
- Claude Code OTel facts for T2 (docs, 2026-11-13): logs events `claude_code.api_request` (model, cost_usd, duration_ms, input/output/cache_read/cache_creation tokens, request_id, query_source, agent.name, skill.name, mcp_server.name), `tool_result` (tool_name, success "true"/"false", duration_ms, error_type), `api_error`, `subagent_completed`, `compaction`; event attrs `session.id`, `prompt.id`, `event.timestamp`, `agent_id`, `parent_agent_id`; `OTEL_RESOURCE_ATTRIBUTES` custom keys land on every event and the resource. Logs export interval 5 s; flush-on-exit of `claude -p` is unverified, check empirically. `claude -p --output-format json` gives `session_id`, `total_cost_usd` (includes subagents), `usage` (excludes subagents).
- L5 (2026-11-13): T2 done. Empirical capture (one `claude -p` on Haiku, scratch receiver): Claude Code sends `body=claude_code.<event>` plus attribute `event.name`, no trace/span ids, no `eventName` field; custom `OTEL_RESOURCE_ATTRIBUTES` appear both on the resource and on every record; a headless run flushes all events on exit in one batch; the record carries user.email/account ids, so identity attrs are dropped before storage and the committed fixture is synthetic. Settings-file `env` overrides the shell, so per-run overrides go through `--settings`. Decision: Claude Code's `cost_usd` is used when present (prices 5m vs 1h cache tiers), the LiteLLM table otherwise. Finding for Fran: a bare "ok" on Haiku costs $0.011 because a fresh session writes ~55k tokens to the 1 h cache (harness context ~66k tokens).
- Open point for T4: the `tokens` metric and TOKENS column count input+output only; for harness runs cache read/creation dominate, so S5 should expose them.
- Next: T3.
