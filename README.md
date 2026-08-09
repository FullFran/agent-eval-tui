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

## Status

Planning. See the issues. The domain model comes before the TUI — the terminal
interface is the fun part and the part that decides least.

---

Written by [Francisco Olmedo](https://github.com/FullFran).
