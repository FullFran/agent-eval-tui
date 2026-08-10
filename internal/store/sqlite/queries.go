package sqlite

// SQL kept together and out of the logic, so a query can be read, copied into
// a sqlite3 shell and run against a real database without editing Go.

const upsertSpan = `
INSERT INTO spans (
    span_id, trace_id, parent_span_id,
    service_name, name, kind,
    start_unix_nano, end_unix_nano, duration_ns,
    status_code, status_message,
    operation, provider, request_model, response_model,
    input_tokens, output_tokens, finish_reasons,
    conversation_id, agent_name, tool_name,
    usage_json, cost_usd, priced,
    case_id, variant,
    is_llm_call, is_tool_call,
    attributes, received_at
) VALUES (
    ?, ?, ?,
    ?, ?, ?,
    ?, ?, ?,
    ?, ?,
    ?, ?, ?, ?,
    ?, ?, ?,
    ?, ?, ?,
    ?, ?, ?,
    ?, ?,
    ?, ?,
    ?, ?
)
ON CONFLICT(span_id) DO UPDATE SET
    trace_id        = excluded.trace_id,
    parent_span_id  = excluded.parent_span_id,
    service_name    = excluded.service_name,
    name            = excluded.name,
    kind            = excluded.kind,
    start_unix_nano = excluded.start_unix_nano,
    end_unix_nano   = excluded.end_unix_nano,
    duration_ns     = excluded.duration_ns,
    status_code     = excluded.status_code,
    status_message  = excluded.status_message,
    operation       = excluded.operation,
    provider        = excluded.provider,
    request_model   = excluded.request_model,
    response_model  = excluded.response_model,
    input_tokens    = excluded.input_tokens,
    output_tokens   = excluded.output_tokens,
    finish_reasons  = excluded.finish_reasons,
    conversation_id = excluded.conversation_id,
    agent_name      = excluded.agent_name,
    tool_name       = excluded.tool_name,
    usage_json      = excluded.usage_json,
    cost_usd        = excluded.cost_usd,
    priced          = excluded.priced,
    case_id         = excluded.case_id,
    variant         = excluded.variant,
    is_llm_call     = excluded.is_llm_call,
    is_tool_call    = excluded.is_tool_call,
    attributes      = excluded.attributes,
    received_at     = excluded.received_at
`

const upsertContent = `
INSERT INTO span_content (
    span_id, system_instructions, input_messages, output_messages,
    tool_arguments, tool_result
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(span_id) DO UPDATE SET
    system_instructions = excluded.system_instructions,
    input_messages      = excluded.input_messages,
    output_messages     = excluded.output_messages,
    tool_arguments      = excluded.tool_arguments,
    tool_result         = excluded.tool_result
`

// Column order is shared by refreshTrace and TraceSpans; both scan it.
const selectSpansForRollup = `
SELECT span_id, trace_id, parent_span_id, service_name, name,
       start_unix_nano, end_unix_nano, status_code, request_model,
       response_model, input_tokens, output_tokens, operation,
       agent_name, tool_name, usage_json, cost_usd, priced, conversation_id,
       case_id, variant
FROM spans
WHERE trace_id = ?
ORDER BY start_unix_nano ASC
`

const upsertTrace = `
INSERT INTO traces (
    trace_id, service_name, root_span_id, root_name, agent_name,
    start_unix_nano, end_unix_nano, duration_ns,
    span_count, error_count, llm_calls, tool_calls,
    input_tokens, output_tokens, usage_json, cost_usd, priced,
    session_id, case_id, variant, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(trace_id) DO UPDATE SET
    service_name    = excluded.service_name,
    root_span_id    = excluded.root_span_id,
    root_name       = excluded.root_name,
    agent_name      = excluded.agent_name,
    start_unix_nano = excluded.start_unix_nano,
    end_unix_nano   = excluded.end_unix_nano,
    duration_ns     = excluded.duration_ns,
    span_count      = excluded.span_count,
    error_count     = excluded.error_count,
    llm_calls       = excluded.llm_calls,
    tool_calls      = excluded.tool_calls,
    input_tokens    = excluded.input_tokens,
    output_tokens   = excluded.output_tokens,
    usage_json      = excluded.usage_json,
    cost_usd        = excluded.cost_usd,
    priced          = excluded.priced,
    session_id      = excluded.session_id,
    case_id         = excluded.case_id,
    variant         = excluded.variant,
    updated_at      = excluded.updated_at
`

const selectRecentTraces = `
SELECT trace_id, service_name, root_span_id, root_name, agent_name,
       start_unix_nano, end_unix_nano, span_count, error_count,
       llm_calls, tool_calls, input_tokens, output_tokens,
       usage_json, cost_usd, priced, session_id, case_id, variant
FROM traces
ORDER BY start_unix_nano DESC
LIMIT ?
`

const selectSessions = `
SELECT session_id,
       COUNT(*)              AS traces,
       SUM(span_count)       AS spans,
       SUM(error_count)      AS errors,
       SUM(input_tokens)     AS input_tokens,
       SUM(output_tokens)    AS output_tokens,
       SUM(cost_usd)         AS cost_usd,
       MAX(priced)           AS priced,
       MIN(start_unix_nano)  AS started,
       MAX(end_unix_nano)    AS ended
FROM traces
WHERE session_id <> ''
GROUP BY session_id
ORDER BY started DESC
LIMIT ?
`

// Observations for one case: one row per trace, which is one run.
//
// duration_ns and cost come straight off the rollup; success is the inverse of
// error_count so a 0/1 sample can be averaged into a success rate.
const selectCaseObservations = `
SELECT variant,
       duration_ns,
       cost_usd,
       priced,
       input_tokens + output_tokens AS tokens,
       CASE WHEN error_count = 0 THEN 1.0 ELSE 0.0 END AS success
FROM traces
WHERE case_id = ?
ORDER BY start_unix_nano ASC
`

const selectCases = `
SELECT case_id,
       COUNT(*)                        AS runs,
       COUNT(DISTINCT variant)         AS variants,
       SUM(CASE WHEN error_count = 0 THEN 1 ELSE 0 END) AS succeeded,
       MIN(start_unix_nano)            AS started,
       MAX(end_unix_nano)              AS ended
FROM traces
WHERE case_id <> ''
GROUP BY case_id
ORDER BY started DESC
LIMIT ?
`
