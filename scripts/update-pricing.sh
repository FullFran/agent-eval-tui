#!/usr/bin/env bash
#
# Regenerate internal/pricing/litellm_prices.json from the upstream LiteLLM
# pricing dataset, which is the community-maintained record of what every
# provider charges.
#
# Prices are not hand-written here for the same reason they are not hand-written
# in claudeops-tui: a table someone types by hand is stale the week after, and a
# stale price produces a confident wrong number that nobody re-checks. LiteLLM
# tracks every provider and is updated when they move.
#
# Output shape: { "<bare-model-id>": [input, output, cache_read, cache_create] }
# in USD per 1,000,000 tokens. Provider-qualified upstream keys ("gemini/...",
# "azure/...") are reduced to their bare last segment; bare keys win on
# collision.
#
# Usage:
#   ./scripts/update-pricing.sh
#   LITELLM_URL=<url> ./scripts/update-pricing.sh   # pin a different source
#
set -euo pipefail

LITELLM_URL="${LITELLM_URL:-https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json}"

for tool in curl jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "error: '$tool' is required" >&2; exit 1; }
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$repo_root/internal/pricing/litellm_prices.json"

raw="$(mktemp)"
converted="$(mktemp)"
trap 'rm -f "$raw" "$converted"' EXIT

echo "Fetching LiteLLM pricing dataset..."
curl -fsSL "$LITELLM_URL" -o "$raw"

echo "Transforming to USD per 1M tokens..."
jq -c '
  def price($v): [
    (($v.input_cost_per_token            // 0) * 1000000),
    (($v.output_cost_per_token           // 0) * 1000000),
    (($v.cache_read_input_token_cost     // 0) * 1000000),
    (($v.cache_creation_input_token_cost // 0) * 1000000)
  ];
  # Pass 1: bare keys (no "/") that publish an input price.
  (reduce (to_entries[]
           | select((.key | test("/")) | not)
           | select(.value.input_cost_per_token != null)) as $e ({};
     .[$e.key] = price($e.value))) as $bare
  # Pass 2: provider-qualified keys -> bare last segment, only if absent.
  | reduce (to_entries[]
            | select(.key | test("/"))
            | select(.value.input_cost_per_token != null)) as $e ($bare;
      ($e.key | split("/") | last) as $k
      | if has($k) then . else .[$k] = price($e.value) end)
' "$raw" > "$converted"

# Validate before overwriting: the upstream format has changed before, and a
# silently empty table would price every model at nothing.
count="$(jq 'keys | length' "$converted")"
if [ "$count" -lt 100 ]; then
  echo "error: refusing to write — only $count models parsed (source format changed?)" >&2
  exit 1
fi

mv "$converted" "$out"
trap 'rm -f "$raw"' EXIT
echo "Wrote $out with $count models."
