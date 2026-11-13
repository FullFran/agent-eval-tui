package experiment_test

import (
	"strings"
	"testing"
	"time"

	"github.com/FullFran/agent-eval-tui/internal/experiment"
	"github.com/FullFran/agent-eval-tui/internal/store/sqlite"
)

func obs(variant string, seconds float64, cost float64, priced bool, success float64) sqlite.Observation {
	return sqlite.Observation{
		Variant:  variant,
		Duration: time.Duration(seconds * float64(time.Second)),
		Cost:     cost,
		Priced:   priced,
		Tokens:   1000,
		Success:  success,
	}
}

func runs(variant string, seconds []float64) []sqlite.Observation {
	out := make([]sqlite.Observation, 0, len(seconds))
	for _, s := range seconds {
		out = append(out, obs(variant, s, 0.01, true, 1))
	}
	return out
}

func TestAnalyseGroupsByVariant(t *testing.T) {
	all := append(
		runs("a", []float64{1, 1.1, 0.9, 1.05}),
		runs("b", []float64{2, 2.1, 1.9, 2.05})...,
	)

	res := experiment.Analyse("case", experiment.MetricLatency, 0.95, all)

	if len(res.Variants) != 2 {
		t.Fatalf("got %d variants, want 2", len(res.Variants))
	}
	for _, v := range res.Variants {
		if v.Interval.N != 4 {
			t.Errorf("variant %s has n=%d, want 4", v.Variant, v.Interval.N)
		}
	}
	if res.Comparison == nil {
		t.Fatal("two variants must produce a comparison")
	}
}

// The headline behaviour: a difference on paper is not a difference.
func TestAnalyseRefusesToCallNoiseAResult(t *testing.T) {
	tests := []struct {
		name string
		a, b []float64
		want bool
	}{
		{
			name: "clearly slower",
			a:    []float64{1.0, 1.1, 0.9, 1.05, 0.95, 1.02, 0.98, 1.01},
			b:    []float64{4.0, 4.1, 3.9, 4.05, 3.95, 4.02, 3.98, 4.01},
			want: true,
		},
		{
			name: "means differ but the spread swamps it",
			a:    []float64{1, 9, 2, 8, 3, 7, 4, 6},
			b:    []float64{2, 10, 3, 9, 4, 8, 5, 7},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			all := append(runs("a", tt.a), runs("b", tt.b)...)
			res := experiment.Analyse("case", experiment.MetricLatency, 0.95, all)

			if res.Comparison == nil {
				t.Fatal("expected a comparison")
			}
			if got := res.Comparison.Significant(); got != tt.want {
				t.Errorf("significant = %v, want %v — verdict was:\n%s",
					got, tt.want, res.Verdict())
			}
		})
	}
}

// An unpriced run must not be averaged in as $0. Doing so drags the mean toward
// free, which is a wrong number that looks like a measurement.
func TestUnpricedRunsAreSkippedNotCountedAsZero(t *testing.T) {
	all := []sqlite.Observation{
		obs("a", 1, 0.10, true, 1),
		obs("a", 1, 0.10, true, 1),
		obs("a", 1, 0, false, 1), // no price for this model
	}

	res := experiment.Analyse("case", experiment.MetricCost, 0.95, all)

	if len(res.Variants) != 1 {
		t.Fatalf("got %d variants, want 1", len(res.Variants))
	}
	v := res.Variants[0]
	if v.Interval.N != 2 {
		t.Errorf("n = %d, want 2 — the unpriced run must not be a sample", v.Interval.N)
	}
	if v.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 — dropping it silently would hide missing data", v.Skipped)
	}
	if v.Interval.Mean != 0.10 {
		t.Errorf("mean = %v, want 0.10; a zero was averaged in", v.Interval.Mean)
	}
}

// Latency is always measurable, so nothing is skipped even when cost is not.
func TestUnpricedRunsStillCountForLatency(t *testing.T) {
	all := []sqlite.Observation{
		obs("a", 1, 0, false, 1),
		obs("a", 3, 0, false, 1),
	}

	res := experiment.Analyse("case", experiment.MetricLatency, 0.95, all)
	if res.Variants[0].Interval.N != 2 || res.Variants[0].Skipped != 0 {
		t.Errorf("n=%d skipped=%d, want 2 and 0",
			res.Variants[0].Interval.N, res.Variants[0].Skipped)
	}
}

// Runs tagged with a case but no variant are still observations of that case.
// Dropping them would lose data the user believes they collected.
func TestUntaggedRunsAreKeptVisible(t *testing.T) {
	res := experiment.Analyse("case", experiment.MetricLatency, 0.95, runs("", []float64{1, 2, 3}))

	if len(res.Variants) != 1 {
		t.Fatalf("got %d variants, want 1", len(res.Variants))
	}
	if res.Variants[0].Variant != "(untagged)" {
		t.Errorf("variant = %q, want (untagged)", res.Variants[0].Variant)
	}
}

// Three arms compared pairwise multiply the chance of finding a difference that
// is not there. Refusing is more honest than quietly picking two.
func TestThreeVariantsReportNoComparison(t *testing.T) {
	all := append(append(
		runs("a", []float64{1, 2}),
		runs("b", []float64{3, 4})...),
		runs("c", []float64{5, 6})...)

	res := experiment.Analyse("case", experiment.MetricLatency, 0.95, all)

	if res.Comparison != nil {
		t.Error("three variants must not produce a two-sample comparison")
	}
	if !strings.Contains(res.Verdict(), "exactly two") {
		t.Errorf("verdict should explain why: %q", res.Verdict())
	}
}

func TestVerdictsExplainWhyThereIsNoAnswer(t *testing.T) {
	tests := []struct {
		name string
		obs  []sqlite.Observation
		want string
	}{
		{"no runs", nil, "no runs recorded"},
		{"one variant", runs("a", []float64{1, 2}), "only one variant"},
		{
			name: "one run each",
			obs:  append(runs("a", []float64{1}), runs("b", []float64{5})...),
			want: "not enough runs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := experiment.Analyse("case", experiment.MetricLatency, 0.95, tt.obs)
			if got := res.Verdict(); !strings.Contains(got, tt.want) {
				t.Errorf("verdict = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func TestParseMetricRejectsUnknown(t *testing.T) {
	for _, name := range []string{"latency", "cost", "tokens", "success"} {
		if _, err := experiment.ParseMetric(name); err != nil {
			t.Errorf("%s should be valid: %v", name, err)
		}
	}
	if _, err := experiment.ParseMetric("vibes"); err == nil {
		t.Error("an unknown metric must be an error, not a silent default")
	}
}

// A success rate margin is in percentage points, not a percentage of the rate.
func TestSuccessMarginIsInPercentagePoints(t *testing.T) {
	if got := experiment.MetricSuccess.FormatMargin(0.05); !strings.Contains(got, "pp") {
		t.Errorf("margin = %q, want percentage points", got)
	}
}

// Success is a proportion: unanimous runs get a Wilson interval, not ± 0.
func TestSuccessUsesProportionIntervals(t *testing.T) {
	var all []sqlite.Observation
	for i := 0; i < 30; i++ {
		all = append(all, obs("a", 1, 0.01, true, 1))
		s := 1.0
		if i < 6 {
			s = 0
		}
		all = append(all, obs("b", 1, 0.01, true, s))
	}

	res := experiment.Analyse("case", experiment.MetricSuccess, 0.95, all)

	a := res.Variants[0].Interval
	if a.High() != 1 || a.Low() >= 0.9 {
		t.Errorf("30/30 interval = [%.4f, %.4f], want Wilson bounds", a.Low(), a.High())
	}
	if res.Comparison == nil || res.Comparison.Method != "newcombe" {
		t.Fatalf("comparison = %+v, want a newcombe comparison", res.Comparison)
	}
	v := res.Verdict()
	if strings.Contains(v, "± 0.0 pp") || !strings.Contains(v, "[") {
		t.Errorf("verdict should show explicit bounds, got:\n%s", v)
	}
}

// Latency verdicts keep the existing ± form.
func TestLatencyVerdictKeepsPlusMinus(t *testing.T) {
	all := append(runs("a", []float64{1, 1.1, 0.9, 1.05}), runs("b", []float64{2, 2.1, 1.9, 2.05})...)
	v := experiment.Analyse("case", experiment.MetricLatency, 0.95, all).Verdict()
	if !strings.Contains(v, "±") || !strings.Contains(v, "df=") {
		t.Errorf("latency verdict changed shape:\n%s", v)
	}
}
