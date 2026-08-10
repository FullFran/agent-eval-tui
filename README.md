# agent-eval-tui

A terminal workbench for **agent telemetry and evaluation**: ingest traces from
any agent, run cases repeatedly, and report quality with error bars instead of
a single flattering number.

## The seam: speak OTLP, demand nothing

Most evaluation tools ship an SDK and ask the agent to adopt it — import the
library, decorate the functions, restructure the loop. That is a real cost, and
it is why most of them are never plugged into anything that already works.

This one ingests **OTLP**, the OpenTelemetry protocol. Any agent already
emitting OpenTelemetry spans — LangGraph, Mastra, a Go service, something
hand-rolled — can point its exporter here and be observed without changing a
line. Framework-agnostic is an architectural property, not a claim in a README.

## Why it exists: one run is not a measurement

Almost every agent benchmark executes a case once, takes the number and plots
it:

```
A: 0.82    B: 0.79
```

With a stochastic model that is noise wearing a lab coat. Is B worse, or did
that run go badly? Nothing in the output can tell you.

```
A − B = 0.03 ± 0.01    n = 30
```

Repeated sampling, variance, intervals, and a stated answer to "is this
difference real?". That is ordinary practice in experimental physics and
strangely rare here.

## Evaluating trajectories, not just answers

Scoring the final answer misses how agents actually fail. The failures are in
the path: loops, the wrong tool, giving up early, calling six tools where one
would do.

| | Single answer | Agent trajectory |
|---|---|---|
| What is measured | The output | Which tools, in what order, how many steps |
| Typical failures | Wrong content | Loops, wrong tool, premature stop, over-calling |
| Metrics | Accuracy, quality | Task success, steps to resolve, wasted calls, cost per resolved task |
| Existing tools | Many | Few worth using |

The second column is where the value is and where the competition is thin.

## Scoring, in order of preference

1. **Programmatic assertion** — cheap, deterministic, trustworthy.
2. **Exact or fuzzy match**.
3. **LLM as judge** — last resort, and only with its agreement against human
   judgement measured on a sample.

An unvalidated judge is an invented number with extra steps.

## Quickstart

One binary, one SQLite file, no server to install.

```bash
go build -o agent-eval ./cmd/agent-eval
./agent-eval serve                      # OTLP/HTTP on :4318
```

Point any OpenTelemetry exporter at it and run your agent:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4318
```

Then read what it saw:

```bash
./agent-eval traces
./agent-eval trace <trace-id>
./agent-eval sessions
```

```
trace 0a0b0c0d…  service=hagabox  duration=4.5s  spans=3  llm=1  tools=1  tokens=2480/512

• invoke_agent soporte  [invoke_agent · 4.5s]
  • chat  [chat · claude-sonnet-4-5 · tokens=2480/512 · 1.9s]
  ✗ execute_tool buscar_pedido  [execute_tool · tool=buscar_pedido · 800ms]
```

## What it captures

Mapped from the OpenTelemetry GenAI semantic conventions, so an agent that is
already instrumented needs no changes:

| From the conventions | Stored as |
|---|---|
| `gen_ai.operation.name` (`invoke_agent`, `chat`, `execute_tool`) | Operation, and whether the span is an LLM or tool call |
| `gen_ai.request.model`, `gen_ai.response.model` | Requested and resolved model — they differ, and cost belongs to what ran |
| `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens` | Token counts, per span and rolled up per trace |
| `gen_ai.tool.name` | Which tool, how often, how long |
| `gen_ai.provider.name` / `gen_ai.system` | Provider |
| `gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.system_instructions` | Content, in its own table |
| `gen_ai.tool.call.arguments`, `gen_ai.tool.call.result` | Tool payloads, in that same table |
| Span status | Errors, counted per trace |
| `gen_ai.conversation.id` | Session, grouping the many traces of one multi-turn interaction |
| every other `gen_ai.usage.*` counter | Usage map — cached, reasoning and audio tokens are billed differently and the set keeps growing |

## Cost

Prices are **not** hand-written. The [LiteLLM pricing dataset](https://github.com/BerriAI/litellm)
is embedded as a snapshot, so a fresh binary prices ~1,600 models with no
configuration:

```
TRACE                              ROOT                  DURATION SPANS   LLM  TOOL    TOKENS       COST
0a0b0c0d0e0f10111213141516171819   invoke_agent soporte      4.5s     3     1     1      2992    $0.0205
```

Regenerate the snapshot with `./scripts/update-pricing.sh`.

A table someone types by hand is stale the week after, and a stale price is
worse than no price: it prints a confident wrong number that nobody re-checks.
Same reasoning as `claudeops-tui`, which prices from LiteLLM for the same reason.

Two details that matter more than they look:

- **Cached input is priced separately.** At roughly a tenth of fresh input, an
  agent reusing a long system prompt has a cost profile that two integer columns
  cannot express.
- **"Not priced" prints as `-`, never as `$0.0000`.** A model missing from the
  dataset must not read as a free one.

Negotiated rates are in no public dataset, so `-pricing overrides.json` layers on
top of the snapshot rather than replacing it:

```json
{ "models": { "claude-sonnet-4-5": { "per_million": { "input_tokens": 2.4, "output_tokens": 12.0 } } } }
```

Content lives in a separate table on purpose. It is the only place personal
data can appear, so a retention policy has exactly one table to delete from
without losing a single measurement.

Every attribute is also kept as raw JSON. The conventions are still moving; when
they move again, a new column can be backfilled from stored data instead of
asking everyone to re-run their agents.

## Deployment

`CGO_ENABLED=0` produces a static binary, because `modernc.org/sqlite` is a
pure-Go SQLite. That is the whole deployment story: copy one file next to
whatever is running your agents, give it a path for the database, done. No
collector, no Postgres, no Docker required.

## Status

**Ingest works.** OTLP/HTTP over protobuf and JSON, GenAI attributes mapped,
persisted to SQLite, trace rollups, and a CLI that lists traces and prints the
span tree. CI vets, race-tests, builds the static binary and then drives the
real binary end to end with a real OTLP payload.

**The TUI does not exist yet**, and neither does the evaluation runner. The
domain model comes before the terminal interface — that is the fun part and the
part that decides least.

Two bugs found while building this, both worth knowing if you write an OTLP
receiver of your own:

- **OTLP/JSON encodes trace and span IDs as hex, but `protojson` decodes
  `bytes` fields as base64.** Feeding a real payload to plain `protojson`
  produces a different, valid-looking ID for every span — no error, and children
  stop joining their parents. IDs are normalised before decoding.
- **An absent parent arrives as eight zero bytes, not an empty field.** Hex-
  encoding it yields `0000000000000000`, so every root span looks like the child
  of a span that does not exist.

---

Written by [Francisco Olmedo](https://github.com/FullFran).
