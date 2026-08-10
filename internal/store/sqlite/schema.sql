-- Schema for the telemetry store.
--
-- One file, no server, no migration tool yet: the whole point of this binary is
-- that it can be dropped next to an agent with nothing else installed.

PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS spans (
    span_id         TEXT PRIMARY KEY,
    trace_id        TEXT NOT NULL,
    parent_span_id  TEXT,

    service_name    TEXT NOT NULL DEFAULT '',
    name            TEXT NOT NULL DEFAULT '',
    kind            TEXT NOT NULL DEFAULT '',

    -- Unix nanoseconds. Stored as integers rather than TEXT timestamps so that
    -- ordering and duration arithmetic happen in SQL without parsing.
    start_unix_nano INTEGER NOT NULL DEFAULT 0,
    end_unix_nano   INTEGER NOT NULL DEFAULT 0,
    duration_ns     INTEGER NOT NULL DEFAULT 0,

    status_code     TEXT NOT NULL DEFAULT 'unset',
    status_message  TEXT NOT NULL DEFAULT '',

    operation       TEXT NOT NULL DEFAULT '',
    provider        TEXT NOT NULL DEFAULT '',
    request_model   TEXT NOT NULL DEFAULT '',
    response_model  TEXT NOT NULL DEFAULT '',
    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    finish_reasons  TEXT NOT NULL DEFAULT '',
    conversation_id TEXT NOT NULL DEFAULT '',
    agent_name      TEXT NOT NULL DEFAULT '',
    tool_name       TEXT NOT NULL DEFAULT '',

    -- Usage counters as a JSON map, not two columns. Providers bill cached,
    -- reasoning and fresh tokens differently and keep adding kinds; widening
    -- the schema per provider is a losing race.
    usage_json      TEXT NOT NULL DEFAULT '{}',
    cost_usd        REAL NOT NULL DEFAULT 0,
    -- Distinguishes "priced at 0" from "no price for this model". Without it a
    -- missing pricing entry reads as a free model.
    priced          INTEGER NOT NULL DEFAULT 0,

    is_llm_call     INTEGER NOT NULL DEFAULT 0,
    is_tool_call    INTEGER NOT NULL DEFAULT 0,

    -- Every attribute as JSON, including the promoted ones. Costs storage and
    -- buys the ability to backfill a column when the conventions move, instead
    -- of asking everyone to re-run their agents.
    attributes      TEXT NOT NULL DEFAULT '{}',

    received_at     INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_spans_trace ON spans (trace_id, start_unix_nano);
CREATE INDEX IF NOT EXISTS idx_spans_parent ON spans (parent_span_id);
CREATE INDEX IF NOT EXISTS idx_spans_tool ON spans (tool_name) WHERE tool_name <> '';
CREATE INDEX IF NOT EXISTS idx_spans_model ON spans (response_model, request_model);

-- Prompts, completions and tool payloads live apart from the metrics.
--
-- Two reasons, and the second is the one that matters. They are bulky, so
-- keeping them out of the spans table keeps the common queries scanning less.
-- And they are the only place personal data can appear, so a retention policy
-- has exactly one table to delete from without losing a single measurement.
CREATE TABLE IF NOT EXISTS span_content (
    span_id             TEXT PRIMARY KEY REFERENCES spans (span_id) ON DELETE CASCADE,
    system_instructions TEXT NOT NULL DEFAULT '',
    input_messages      TEXT NOT NULL DEFAULT '',
    output_messages     TEXT NOT NULL DEFAULT '',
    tool_arguments      TEXT NOT NULL DEFAULT '',
    tool_result         TEXT NOT NULL DEFAULT ''
);

-- Derived rollup, recomputed from spans rather than accumulated.
--
-- Spans arrive out of order, across batches, and exporters retry: an
-- incremental "+= tokens" would double-count on every redelivery. Recomputing
-- costs an indexed scan of one trace and makes the totals idempotent.
CREATE TABLE IF NOT EXISTS traces (
    trace_id        TEXT PRIMARY KEY,
    service_name    TEXT NOT NULL DEFAULT '',
    root_span_id    TEXT NOT NULL DEFAULT '',
    root_name       TEXT NOT NULL DEFAULT '',
    agent_name      TEXT NOT NULL DEFAULT '',

    start_unix_nano INTEGER NOT NULL DEFAULT 0,
    end_unix_nano   INTEGER NOT NULL DEFAULT 0,
    duration_ns     INTEGER NOT NULL DEFAULT 0,

    span_count      INTEGER NOT NULL DEFAULT 0,
    error_count     INTEGER NOT NULL DEFAULT 0,
    llm_calls       INTEGER NOT NULL DEFAULT 0,
    tool_calls      INTEGER NOT NULL DEFAULT 0,

    input_tokens    INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    usage_json      TEXT NOT NULL DEFAULT '{}',
    cost_usd        REAL NOT NULL DEFAULT 0,
    priced          INTEGER NOT NULL DEFAULT 0,

    -- From gen_ai.conversation.id: one multi-turn interaction is many traces,
    -- and without this they are unrelated rows.
    session_id      TEXT NOT NULL DEFAULT '',

    updated_at      INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_traces_session ON traces (session_id, start_unix_nano)
    WHERE session_id <> '';

CREATE INDEX IF NOT EXISTS idx_traces_start ON traces (start_unix_nano DESC);
CREATE INDEX IF NOT EXISTS idx_traces_errors ON traces (error_count) WHERE error_count > 0;
