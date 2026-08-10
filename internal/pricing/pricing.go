// Package pricing turns token usage into money.
//
// Prices come from the LiteLLM dataset, embedded as a snapshot, not from a
// table anyone types by hand. That is a lesson already paid for in
// claudeops-tui: a hand-written table is stale the week after it is written,
// and a stale price produces a confident wrong number that nobody re-checks.
// LiteLLM tracks every provider and is regenerated with scripts/update-pricing.sh.
//
// A per-run override file is still supported, because negotiated or
// enterprise rates are real and are not in any public dataset. It sits on top
// of the snapshot rather than replacing it.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// litellmPricesJSON maps a bare model id to [input, output, cache_read,
// cache_create] in USD per 1,000,000 tokens.
//
//go:embed litellm_prices.json
var litellmPricesJSON []byte

var (
	litellmOnce  sync.Once
	litellmTable map[string]ModelPrice
)

// ModelPrice is the USD cost per 1,000,000 tokens of each class LiteLLM
// publishes.
type ModelPrice struct {
	Input       float64
	Output      float64
	CacheRead   float64
	CacheCreate float64
}

// usageClass maps a `gen_ai.usage.*` counter, minus its prefix, onto a price
// class.
//
// Names differ by SDK for the same thing, so several spellings map to the same
// class. A counter with no mapping contributes nothing rather than failing the
// calculation: a provider inventing a new counter should not zero the cost of
// the ones that are priced.
func usageClass(name string, p ModelPrice) (float64, bool) {
	switch name {
	case "input_tokens", "input", "prompt_tokens":
		return p.Input, true
	case "output_tokens", "output", "completion_tokens":
		return p.Output, true
	case "cache_read_input_tokens", "cache_read", "cached_tokens", "cache_read_tokens":
		return p.CacheRead, true
	case "cache_creation_input_tokens", "cache_write", "cache_creation_tokens":
		return p.CacheCreate, true
	default:
		return 0, false
	}
}

// litellmPrices lazily parses the embedded snapshot.
//
// An entry whose four classes are all zero means LiteLLM publishes no price for
// that model, not that it is free. Skipping it keeps the cost unknown, which is
// the honest answer; reporting $0.0000 would read as a free model.
func litellmPrices() map[string]ModelPrice {
	litellmOnce.Do(func() {
		litellmTable = map[string]ModelPrice{}
		var raw map[string][4]float64
		if err := json.Unmarshal(litellmPricesJSON, &raw); err != nil {
			return
		}
		for name, v := range raw {
			if v[0] == 0 && v[1] == 0 && v[2] == 0 && v[3] == 0 {
				continue
			}
			litellmTable[name] = ModelPrice{
				Input: v[0], Output: v[1], CacheRead: v[2], CacheCreate: v[3],
			}
		}
	})
	return litellmTable
}

// Table prices models: the embedded LiteLLM snapshot, with optional overrides
// layered on top.
type Table struct {
	overrides map[string]ModelPrice
}

// overrideFile is the on-disk shape of the override table. Per million tokens,
// because that is how providers publish prices — a file you can paste numbers
// into without counting zeros.
type overrideFile struct {
	Models map[string]struct {
		PerMillion map[string]float64 `json:"per_million"`
	} `json:"models"`
}

// Load builds a table. An empty path, or a file that does not exist, yields the
// embedded snapshot alone — which is the normal case and not an error.
func Load(path string) (*Table, error) {
	t := &Table{overrides: map[string]ModelPrice{}}
	if path == "" {
		return t, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return t, nil
		}
		return nil, fmt.Errorf("read pricing overrides %s: %w", path, err)
	}

	var parsed overrideFile
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse pricing overrides %s: %w", path, err)
	}

	for name, model := range parsed.Models {
		var price ModelPrice
		for kind, perMillion := range model.PerMillion {
			switch kind {
			case "input_tokens", "input":
				price.Input = perMillion
			case "output_tokens", "output":
				price.Output = perMillion
			case "cache_read_input_tokens", "cache_read":
				price.CacheRead = perMillion
			case "cache_creation_input_tokens", "cache_write":
				price.CacheCreate = perMillion
			}
		}
		t.overrides[name] = price
	}
	return t, nil
}

// Models reports how many models can be priced. Used for the startup log.
func (t *Table) Models() int {
	return len(litellmPrices()) + len(t.overrides)
}

// Overrides reports how many models the override file replaced.
func (t *Table) Overrides() int { return len(t.overrides) }

// Cost prices one span's usage. The second return is false when no table
// covered the model, which callers must keep distinct from a cost of zero.
func (t *Table) Cost(model string, usage map[string]int64) (float64, bool) {
	if model == "" || len(usage) == 0 {
		return 0, false
	}

	price, ok := t.lookup(model)
	if !ok {
		return 0, false
	}

	var total float64
	for name, tokens := range usage {
		perMillion, known := usageClass(name, price)
		if !known {
			continue
		}
		total += float64(tokens) * perMillion / 1_000_000
	}
	return total, true
}

// lookup resolves a model name: overrides first, then the snapshot, each by
// exact match and then by longest prefix.
//
// The prefix pass exists because providers answer with a resolved snapshot —
// ask for `claude-sonnet-4-5` and the response says
// `claude-sonnet-4-5-20260101`. Requiring exact matches would mean pricing
// silently stops working every time a snapshot rolls.
func (t *Table) lookup(model string) (ModelPrice, bool) {
	if p, ok := t.overrides[model]; ok {
		return p, true
	}
	if p, ok := litellmPrices()[model]; ok {
		return p, true
	}
	if p, ok := longestPrefix(t.overrides, model); ok {
		return p, true
	}
	return longestPrefix(litellmPrices(), model)
}

func longestPrefix(table map[string]ModelPrice, model string) (ModelPrice, bool) {
	best := ""
	for name := range table {
		if strings.HasPrefix(model, name) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return ModelPrice{}, false
	}
	return table[best], true
}
