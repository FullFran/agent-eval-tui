// Package experiment turns stored runs into an answer.
//
// It sits between the store and the CLI so the arithmetic is testable without
// a database and the CLI stays a printer.
package experiment

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/FullFran/agent-eval-tui/internal/stats"
	"github.com/FullFran/agent-eval-tui/internal/store/sqlite"
)

// Metric is what is being compared.
type Metric string

const (
	MetricLatency Metric = "latency"
	MetricCost    Metric = "cost"
	MetricTokens  Metric = "tokens"
	MetricSuccess Metric = "success"
)

// Metrics lists the supported metrics, for flag help and validation.
var Metrics = []Metric{MetricLatency, MetricCost, MetricTokens, MetricSuccess}

// ParseMetric validates a metric name.
func ParseMetric(s string) (Metric, error) {
	for _, m := range Metrics {
		if string(m) == s {
			return m, nil
		}
	}
	return "", fmt.Errorf("unknown metric %q (want one of %v)", s, Metrics)
}

// value extracts the metric from one observation.
func value(o sqlite.Observation, m Metric) (float64, bool) {
	switch m {
	case MetricLatency:
		return o.Duration.Seconds(), true
	case MetricCost:
		// An unpriced run contributes nothing rather than a zero. Averaging in
		// a zero for "we do not know what this cost" drags the mean toward
		// free, which is a wrong number that looks like a measurement.
		return o.Cost, o.Priced
	case MetricTokens:
		return float64(o.Tokens), true
	case MetricSuccess:
		return o.Success, true
	default:
		return 0, false
	}
}

// VariantResult is one arm of the experiment.
type VariantResult struct {
	Variant  string
	Interval stats.Interval
	Skipped  int // observations the metric could not be read from
}

// Result is a whole comparison of one case.
type Result struct {
	CaseID   string
	Metric   Metric
	Level    float64
	Variants []VariantResult

	// Comparison is populated only when there are exactly two variants.
	// Comparing three arms pairwise multiplies the chance of finding a
	// difference that is not there, and correcting for that honestly is a
	// bigger decision than this function should make on its own.
	Comparison *stats.Comparison
	Baseline   string
	Candidate  string
}

// Analyse groups observations by variant and computes intervals, plus the
// difference when there are exactly two.
func Analyse(caseID string, metric Metric, level float64, obs []sqlite.Observation) Result {
	res := Result{CaseID: caseID, Metric: metric, Level: level}

	byVariant := map[string][]float64{}
	skipped := map[string]int{}
	for _, o := range obs {
		name := o.Variant
		if name == "" {
			// Runs tagged with a case but no variant are still observations of
			// that case; calling them "(untagged)" keeps them visible instead
			// of dropping data the user believes they collected.
			name = "(untagged)"
		}
		v, ok := value(o, metric)
		if !ok {
			skipped[name]++
			continue
		}
		byVariant[name] = append(byVariant[name], v)
	}

	names := make([]string, 0, len(byVariant))
	for name := range byVariant {
		names = append(names, name)
	}
	for name := range skipped {
		if _, ok := byVariant[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		res.Variants = append(res.Variants, VariantResult{
			Variant:  name,
			Interval: interval(metric, stats.Sample{Values: byVariant[name]}, level),
			Skipped:  skipped[name],
		})
	}

	if len(res.Variants) == 2 {
		a := stats.Sample{Values: byVariant[res.Variants[0].Variant]}
		b := stats.Sample{Values: byVariant[res.Variants[1].Variant]}
		c := compare(metric, a, b, level)
		res.Comparison = &c
		res.Baseline = res.Variants[0].Variant
		res.Candidate = res.Variants[1].Variant
	}

	return res
}

// interval picks the interval for the metric. Success is a proportion of 0/1
// outcomes and gets Wilson; the rest are means and get the t interval.
func interval(m Metric, s stats.Sample, level float64) stats.Interval {
	if m == MetricSuccess {
		return stats.ProportionInterval(s, level)
	}
	return stats.ConfidenceInterval(s, level)
}

func compare(m Metric, a, b stats.Sample, level float64) stats.Comparison {
	if m == MetricSuccess {
		return stats.CompareProportions(a, b, level)
	}
	return stats.Compare(a, b, level)
}

// FormatInterval renders an interval's uncertainty: "± margin" when it is
// symmetric, explicit "[low, high]" bounds when it is not.
func (m Metric) FormatInterval(i stats.Interval) string {
	if math.IsNaN(i.Margin) {
		return "?"
	}
	if i.Symmetric() {
		return "± " + m.FormatMargin(i.Margin)
	}
	return "[" + m.Format(i.Low()) + ", " + m.Format(i.High()) + "]"
}

// Format renders a value in the metric's own units.
func (m Metric) Format(v float64) string {
	if math.IsNaN(v) {
		return "-"
	}
	switch m {
	case MetricLatency:
		return shortDuration(time.Duration(v * float64(time.Second)))
	case MetricCost:
		return fmt.Sprintf("$%.4f", v)
	case MetricTokens:
		return fmt.Sprintf("%.0f", v)
	case MetricSuccess:
		return fmt.Sprintf("%.1f%%", v*100)
	default:
		return fmt.Sprintf("%.4f", v)
	}
}

// FormatMargin renders a ± margin. Separate from Format because a margin of
// "$0.0012" reads correctly while a margin of "12.5%" must not be rendered as
// an absolute percentage of anything.
func (m Metric) FormatMargin(v float64) string {
	if math.IsNaN(v) {
		return "?"
	}
	switch m {
	case MetricSuccess:
		return fmt.Sprintf("%.1f pp", v*100)
	default:
		return m.Format(v)
	}
}

func shortDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "-" + shortDuration(-d)
	case d < time.Millisecond:
		return fmt.Sprintf("%.0fµs", float64(d.Nanoseconds())/1000)
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

// Verdict is the sentence a human reads.
//
// Deliberately narrow. It answers whether the interval excludes zero at the
// chosen level, and says nothing about whether the difference is worth acting
// on: a change can be real and irrelevant, and conflating the two is how
// benchmarks end up driving bad decisions.
func (r Result) Verdict() string {
	if r.Comparison == nil {
		switch len(r.Variants) {
		case 0:
			return "no runs recorded for this case"
		case 1:
			return "only one variant — nothing to compare it against"
		default:
			return fmt.Sprintf("%d variants; comparison is reported for exactly two", len(r.Variants))
		}
	}

	d := r.Comparison.Difference
	if math.IsNaN(d.Margin) {
		return "not enough runs to say anything — at least two per variant"
	}

	sign := ""
	if d.Mean > 0 {
		sign = "+"
	}
	var head string
	if r.Comparison.Method == stats.MethodNewcombe {
		head = fmt.Sprintf("%s − %s = %s%s %s   (n=%d, Newcombe)",
			r.Candidate, r.Baseline,
			sign, r.Metric.FormatMargin(d.Mean),
			"["+r.Metric.FormatMargin(d.Low())+", "+r.Metric.FormatMargin(d.High())+"]", d.N)
	} else {
		head = fmt.Sprintf("%s − %s = %s%s ± %s   (n=%d, df=%.1f)",
			r.Candidate, r.Baseline,
			sign, r.Metric.Format(d.Mean), r.Metric.FormatMargin(d.Margin),
			d.N, r.Comparison.DF)
	}

	if r.Comparison.Significant() {
		return head + fmt.Sprintf("\n  the difference is real at %.0f%% confidence", r.Level*100)
	}
	return head + fmt.Sprintf(
		"\n  the interval includes zero: at %.0f%% confidence this is indistinguishable from noise",
		r.Level*100)
}
