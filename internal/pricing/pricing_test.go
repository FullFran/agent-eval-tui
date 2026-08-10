package pricing_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/FullFran/agent-eval-tui/internal/pricing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "overrides.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The whole point of embedding the LiteLLM snapshot is that a fresh binary
// prices real models with no configuration at all.
func TestEmbeddedSnapshotPricesWithoutConfiguration(t *testing.T) {
	tbl, err := pricing.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Models() < 100 {
		t.Fatalf("embedded snapshot has %d models, expected hundreds", tbl.Models())
	}

	cost, ok := tbl.Cost("claude-sonnet-4-5", map[string]int64{
		"input_tokens":  1_000_000,
		"output_tokens": 1_000_000,
	})
	if !ok {
		t.Fatal("a mainstream model must be priced out of the box")
	}
	if cost <= 0 {
		t.Errorf("cost = %v, want a positive number", cost)
	}
}

// Providers answer with a resolved snapshot, so exact-match-only pricing would
// stop working every time a snapshot rolls.
func TestSnapshotModelResolvesByPrefix(t *testing.T) {
	tbl, _ := pricing.Load("")

	family, okFamily := tbl.Cost("claude-sonnet-4-5", map[string]int64{"input_tokens": 1_000_000})
	dated, okDated := tbl.Cost("claude-sonnet-4-5-20260101", map[string]int64{"input_tokens": 1_000_000})

	if !okFamily || !okDated {
		t.Fatalf("both should price: family=%v dated=%v", okFamily, okDated)
	}
	if family != dated {
		t.Errorf("dated snapshot priced %v, family priced %v", dated, family)
	}
}

// Cached input is billed at a fraction of fresh input. An agent reusing a long
// system prompt has a cost profile two integer columns cannot express.
func TestCacheReadIsPricedSeparately(t *testing.T) {
	tbl, _ := pricing.Load("")

	fresh, _ := tbl.Cost("claude-sonnet-4-5", map[string]int64{"input_tokens": 1_000_000})
	cached, ok := tbl.Cost("claude-sonnet-4-5", map[string]int64{"cache_read_input_tokens": 1_000_000})

	if !ok {
		t.Fatal("cache read should be priced")
	}
	if cached >= fresh {
		t.Errorf("cached input (%v) should cost less than fresh (%v)", cached, fresh)
	}
}

// Negotiated rates are real and are in no public dataset, so an override has to
// beat the snapshot.
func TestOverrideBeatsSnapshot(t *testing.T) {
	path := write(t, `{"models":{"claude-sonnet-4-5":{"per_million":{"input_tokens":1.0,"output_tokens":2.0}}}}`)
	tbl, err := pricing.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Overrides() != 1 {
		t.Fatalf("overrides = %d, want 1", tbl.Overrides())
	}

	cost, ok := tbl.Cost("claude-sonnet-4-5", map[string]int64{"input_tokens": 1_000_000})
	if !ok {
		t.Fatal("should be priced")
	}
	if cost != 1.0 {
		t.Errorf("cost = %v, want the override's 1.0", cost)
	}
}

// A counter nobody has priced must not zero the counters that are priced.
func TestUnknownUsageKindIsSkipped(t *testing.T) {
	tbl, _ := pricing.Load("")

	withOnly, _ := tbl.Cost("claude-sonnet-4-5", map[string]int64{"input_tokens": 1_000_000})
	withExtra, ok := tbl.Cost("claude-sonnet-4-5", map[string]int64{
		"input_tokens": 1_000_000,
		"moon_tokens":  500,
	})
	if !ok || withExtra != withOnly {
		t.Errorf("extra counter changed the cost: %v vs %v", withExtra, withOnly)
	}
}

// "Not priced" must stay distinguishable from "free".
func TestUnknownModelIsNotPriced(t *testing.T) {
	tbl, _ := pricing.Load("")
	if _, ok := tbl.Cost("definitely-not-a-real-model-xyzzy", map[string]int64{"input_tokens": 10}); ok {
		t.Error("unknown model must report not-priced")
	}
}

// A missing override file is the normal case, not an error.
func TestMissingOverrideFileIsNotAnError(t *testing.T) {
	tbl, err := pricing.Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing override file must not fail: %v", err)
	}
	if tbl.Overrides() != 0 {
		t.Errorf("overrides = %d, want 0", tbl.Overrides())
	}
	if _, ok := tbl.Cost("claude-sonnet-4-5", map[string]int64{"input_tokens": 1}); !ok {
		t.Error("the embedded snapshot must still price without an override file")
	}
}
