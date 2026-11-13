package stats

import (
	"math"
	"testing"
)

func near(got, want, tol float64) bool {
	if math.IsNaN(got) || math.IsNaN(want) {
		return math.IsNaN(got) && math.IsNaN(want)
	}
	return math.Abs(got-want) <= tol
}

// The t quantile is the one piece of arithmetic here that cannot be checked by
// inspection, and a wrong value would silently narrow every interval the tool
// prints. Checked against published two-tailed 95% table values.
func TestStudentTQuantileMatchesPublishedTables(t *testing.T) {
	tests := []struct {
		name string
		df   float64
		want float64
	}{
		{"df=1", 1, 12.706},
		{"df=2", 2, 4.303},
		{"df=5", 5, 2.571},
		{"df=10", 10, 2.228},
		{"df=20", 20, 2.086},
		{"df=30", 30, 2.042},
		{"df=60", 60, 2.000},
		{"df=120", 120, 1.980},
		{"df=1000 approaches the normal", 1000, 1.962},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := studentTQuantile(0.975, tt.df)
			if !near(got, tt.want, 0.002) {
				t.Errorf("t(0.975, %v) = %.4f, want %.3f", tt.df, got, tt.want)
			}
		})
	}
}

func TestDescriptiveStatistics(t *testing.T) {
	s := Sample{Values: []float64{2, 4, 4, 4, 5, 5, 7, 9}}

	if !near(s.Mean(), 5, 1e-9) {
		t.Errorf("mean = %v, want 5", s.Mean())
	}
	// Sample variance (n-1) of this classic set is 32/7.
	if !near(s.Variance(), 32.0/7.0, 1e-9) {
		t.Errorf("variance = %v, want %v", s.Variance(), 32.0/7.0)
	}
	if !near(s.StdErr(), s.StdDev()/math.Sqrt(8), 1e-12) {
		t.Errorf("stderr = %v", s.StdErr())
	}
}

// Dividing by n instead of n-1 understates the spread, which makes differences
// look more real than they are. This pins the choice.
func TestVarianceUsesNMinusOne(t *testing.T) {
	s := Sample{Values: []float64{1, 2, 3, 4, 5}}
	const populationVariance = 2.0 // /n
	const sampleVariance = 2.5     // /(n-1)

	if near(s.Variance(), populationVariance, 1e-9) {
		t.Fatal("variance is dividing by n; it must divide by n-1")
	}
	if !near(s.Variance(), sampleVariance, 1e-9) {
		t.Errorf("variance = %v, want %v", s.Variance(), sampleVariance)
	}
}

func TestConfidenceIntervalWidensWithSpreadAndNarrowsWithN(t *testing.T) {
	tight := Sample{Values: []float64{10, 10.1, 9.9, 10, 10.05, 9.95, 10, 10.02}}
	loose := Sample{Values: []float64{10, 14, 6, 12, 8, 11, 9, 10}}

	tightCI := ConfidenceInterval(tight, 0.95)
	looseCI := ConfidenceInterval(loose, 0.95)

	if tightCI.Margin >= looseCI.Margin {
		t.Errorf("a tighter sample must give a smaller margin: %v vs %v",
			tightCI.Margin, looseCI.Margin)
	}

	more := Sample{Values: append(append([]float64{}, loose.Values...), loose.Values...)}
	if ConfidenceInterval(more, 0.95).Margin >= looseCI.Margin {
		t.Error("doubling the observations must shrink the margin")
	}
}

// The headline claim of the project: two means that differ on paper are not
// necessarily different.
func TestCompareDistinguishesRealDifferenceFromNoise(t *testing.T) {
	tests := []struct {
		name            string
		a, b            []float64
		wantSignificant bool
	}{
		{
			name:            "clearly separated groups",
			a:               []float64{1.0, 1.1, 0.9, 1.05, 0.95, 1.02, 0.98, 1.01},
			b:               []float64{2.0, 2.1, 1.9, 2.05, 1.95, 2.02, 1.98, 2.01},
			wantSignificant: true,
		},
		{
			name:            "different means, spread far too wide to tell",
			a:               []float64{1, 5, -3, 8, -2, 4, 0, 6},
			b:               []float64{2, 6, -2, 9, -1, 5, 1, 7},
			wantSignificant: false,
		},
		{
			name:            "identical samples",
			a:               []float64{1, 2, 3, 4, 5},
			b:               []float64{1, 2, 3, 4, 5},
			wantSignificant: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Compare(Sample{tt.a}, Sample{tt.b}, 0.95)
			if c.Significant() != tt.wantSignificant {
				t.Errorf("significant = %v, want %v (diff %.3f ± %.3f)",
					c.Significant(), tt.wantSignificant, c.Difference.Mean, c.Difference.Margin)
			}
		})
	}
}

func TestCompareReportsTheDifferenceAsBMinusA(t *testing.T) {
	a := Sample{Values: []float64{1, 1, 1, 1}}
	b := Sample{Values: []float64{3, 3, 3, 3}}

	c := Compare(a, b, 0.95)
	if !near(c.Difference.Mean, 2, 1e-9) {
		t.Errorf("difference = %v, want +2 (B − A)", c.Difference.Mean)
	}
}

// Every run gave the same answer in both groups. The difference is exact, and
// the honest margin is zero rather than NaN from dividing by zero.
func TestCompareHandlesZeroVariance(t *testing.T) {
	a := Sample{Values: []float64{1, 1, 1, 1}}
	b := Sample{Values: []float64{2, 2, 2, 2}}

	c := Compare(a, b, 0.95)
	if !near(c.Difference.Margin, 0, 1e-12) {
		t.Errorf("margin = %v, want 0", c.Difference.Margin)
	}
	if !c.Significant() {
		t.Error("an exact, non-zero difference should not be called noise")
	}
}

// One observation cannot produce a spread, and the tool must say so instead of
// inventing a confident zero.
func TestSingleObservationHasNoInterval(t *testing.T) {
	ci := ConfidenceInterval(Sample{Values: []float64{42}}, 0.95)
	if !math.IsNaN(ci.Margin) {
		t.Errorf("margin = %v, want NaN for n=1", ci.Margin)
	}
	if ci.ExcludesZero() {
		t.Error("an interval with no margin must not claim to exclude zero")
	}

	c := Compare(Sample{Values: []float64{1}}, Sample{Values: []float64{5}}, 0.95)
	if c.Significant() {
		t.Error("one run against one run is never a result")
	}
}

func TestPercentileUsesNearestRank(t *testing.T) {
	s := Sample{Values: []float64{5, 1, 4, 2, 3}}

	tests := []struct {
		name string
		p    float64
		want float64
	}{
		{"minimum", 0, 1},
		{"median", 0.5, 3},
		{"p95 of five values is the largest", 0.95, 5},
		{"maximum", 1, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.Percentile(tt.p); !near(got, tt.want, 1e-9) {
				t.Errorf("p%.0f = %v, want %v", tt.p*100, got, tt.want)
			}
		})
	}
}

func TestEmptySampleIsNaNNotZero(t *testing.T) {
	var s Sample
	if !math.IsNaN(s.Mean()) {
		t.Error("the mean of nothing must be NaN, not 0 — 0 is a measurement")
	}
	if !math.IsNaN(s.Variance()) {
		t.Error("the variance of nothing must be NaN")
	}
}

// Wilson score bounds against published values (Newcombe 1998, Stat Med 17,
// table I, method 3) and the closed forms at the boundaries.
func TestWilsonMatchesPublishedValues(t *testing.T) {
	tests := []struct {
		name         string
		k, n         int
		lower, upper float64
	}{
		{"81/263", 81, 263, 0.2553, 0.3662},
		{"15/148", 15, 148, 0.0624, 0.1605},
		{"0/20", 0, 20, 0, 0.1611},
		{"1/29", 1, 29, 0.0061, 0.1718},
		// All successes: lower bound is n/(n+z²), never 1.
		{"30/30", 30, 30, 30 / (30 + 1.959964*1.959964), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := Wilson(tt.k, tt.n, 0.95)
			if !near(i.Low(), tt.lower, 0.0005) || !near(i.High(), tt.upper, 0.0005) {
				t.Errorf("Wilson(%d,%d) = [%.4f, %.4f], want [%.4f, %.4f]",
					tt.k, tt.n, i.Low(), i.High(), tt.lower, tt.upper)
			}
			if !near(i.Mean, float64(tt.k)/float64(tt.n), 1e-12) {
				t.Errorf("mean = %v, want the observed proportion", i.Mean)
			}
		})
	}
}

// The regression that motivated Wilson: every run succeeding is not certainty.
func TestUnanimousSuccessIsNotCertainty(t *testing.T) {
	i := ProportionInterval(Sample{Values: ones(30)}, 0.95)
	if i.Low() >= 0.9 || i.High() != 1 {
		t.Errorf("30/30 gave [%.4f, %.4f], want a lower bound well below 1", i.Low(), i.High())
	}
	if i.Symmetric() {
		t.Error("a Wilson interval at the boundary must not claim to be symmetric")
	}
}

// Newcombe's hybrid score interval for a difference of proportions, checked
// against his worked example (56/70 vs 48/80, method 10: 0.0524 to 0.3339).
func TestCompareProportionsMatchesNewcombe(t *testing.T) {
	a := Sample{Values: bernoulli(48, 80)}
	b := Sample{Values: bernoulli(56, 70)}
	c := CompareProportions(a, b, 0.95)
	d := c.Difference
	if !near(d.Mean, 0.2, 1e-9) || !near(d.Low(), 0.0524, 0.0005) || !near(d.High(), 0.3339, 0.0005) {
		t.Errorf("difference = %.4f [%.4f, %.4f], want 0.2000 [0.0524, 0.3339]", d.Mean, d.Low(), d.High())
	}
	if !c.Significant() {
		t.Error("an interval excluding zero must be reported as significant")
	}
	if c.Method != MethodNewcombe {
		t.Errorf("method = %q, want %q", c.Method, MethodNewcombe)
	}
}

// Both arms unanimous and equal must not read as an exact, certain zero.
func TestCompareProportionsUnanimousArmsKeepUncertainty(t *testing.T) {
	c := CompareProportions(Sample{Values: ones(30)}, Sample{Values: ones(30)}, 0.95)
	if c.Difference.Low() >= 0 || c.Difference.High() <= 0 || c.Significant() {
		t.Errorf("30/30 vs 30/30 gave [%.4f, %.4f], want an interval around zero",
			c.Difference.Low(), c.Difference.High())
	}
}

// The mean path keeps its documented zero-variance behaviour.
func TestCompareMeansZeroVarianceUnchanged(t *testing.T) {
	c := Compare(Sample{Values: []float64{2, 2, 2}}, Sample{Values: []float64{2, 2, 2}}, 0.95)
	if c.Difference.Margin != 0 || c.Method != MethodWelch {
		t.Errorf("margin = %v method = %q, want 0 and %q", c.Difference.Margin, c.Method, MethodWelch)
	}
}

func ones(n int) []float64 { return bernoulli(n, n) }

func bernoulli(k, n int) []float64 {
	out := make([]float64, n)
	for i := 0; i < k; i++ {
		out[i] = 1
	}
	return out
}
