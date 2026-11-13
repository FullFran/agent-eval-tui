// Package stats is the arithmetic behind "is this difference real?".
//
// Deliberately hand-written rather than pulled from gonum. What is needed here
// is a mean, a variance, a t quantile and Welch's test — a few hundred lines
// against a numerical library that would dwarf the rest of the binary. The
// tests check the t quantile against published table values, which is the part
// worth being suspicious of.
package stats

import (
	"math"
	"sort"
)

// Sample is a set of measurements of one thing: the latency of 30 runs of the
// same case, the cost of 30 runs, the 0/1 success of 30 runs.
type Sample struct {
	Values []float64
}

// N is the number of observations.
func (s Sample) N() int { return len(s.Values) }

// Mean is the arithmetic mean. NaN for an empty sample, which propagates rather
// than silently reading as zero.
func (s Sample) Mean() float64 {
	if len(s.Values) == 0 {
		return math.NaN()
	}
	var sum float64
	for _, v := range s.Values {
		sum += v
	}
	return sum / float64(len(s.Values))
}

// Variance is the sample variance, dividing by n-1.
//
// n-1, not n: these are samples from a process that could have gone otherwise,
// not a whole population. Dividing by n underestimates the spread, which is
// exactly the error that makes a difference look more real than it is.
func (s Sample) Variance() float64 {
	n := len(s.Values)
	if n < 2 {
		return math.NaN()
	}
	mean := s.Mean()
	var sum float64
	for _, v := range s.Values {
		d := v - mean
		sum += d * d
	}
	return sum / float64(n-1)
}

// StdDev is the sample standard deviation.
func (s Sample) StdDev() float64 { return math.Sqrt(s.Variance()) }

// StdErr is the standard error of the mean: how much the mean itself would
// move if you ran the whole thing again.
func (s Sample) StdErr() float64 {
	if len(s.Values) < 2 {
		return math.NaN()
	}
	return s.StdDev() / math.Sqrt(float64(len(s.Values)))
}

// Percentile returns the value at p (0..1) by nearest rank.
//
// Nearest rank rather than interpolation: an interpolated p95 latency is a
// number no run actually produced, and for a tail statistic that is the wrong
// kind of tidy.
func (s Sample) Percentile(p float64) float64 {
	if len(s.Values) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), s.Values...)
	sort.Float64s(sorted)

	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Interval is a mean and how far it might be from the truth.
type Interval struct {
	Mean   float64
	Margin float64 // half-width; the interval is Mean ± Margin
	N      int
	Level  float64 // e.g. 0.95

	// Explicit bounds for intervals that are not symmetric around Mean, such
	// as Wilson's. Margin then holds the half-width, for callers that only
	// need a size; Low and High are the truth.
	lower, upper float64
	bounded      bool
}

// Low and High are the interval bounds.
func (i Interval) Low() float64 {
	if i.bounded {
		return i.lower
	}
	return i.Mean - i.Margin
}

func (i Interval) High() float64 {
	if i.bounded {
		return i.upper
	}
	return i.Mean + i.Margin
}

// Symmetric reports whether the interval is Mean ± Margin. When it is not,
// printing it as "± Margin" misstates one of the bounds.
func (i Interval) Symmetric() bool { return !i.bounded }

// boundedInterval builds an interval from explicit bounds.
func boundedInterval(mean, lower, upper float64, n int, level float64) Interval {
	return Interval{
		Mean: mean, Margin: (upper - lower) / 2, N: n, Level: level,
		lower: lower, upper: upper, bounded: true,
	}
}

// ExcludesZero reports whether the interval is entirely above or below zero.
//
// For a difference of means this is the whole question: an interval containing
// zero is a difference you cannot distinguish from having run it twice.
func (i Interval) ExcludesZero() bool {
	if math.IsNaN(i.Mean) || math.IsNaN(i.Margin) {
		return false
	}
	return i.Low() > 0 || i.High() < 0
}

// ConfidenceInterval returns the mean and its margin at the given level.
//
// Uses the t distribution rather than the normal, because the population
// variance is estimated from the same small sample. With n = 10 that
// distinction is roughly 15% of the margin — the difference between a result
// and an overclaim.
func ConfidenceInterval(s Sample, level float64) Interval {
	out := Interval{Mean: s.Mean(), Margin: math.NaN(), N: s.N(), Level: level}
	if s.N() < 2 {
		return out
	}
	df := float64(s.N() - 1)
	out.Margin = studentTQuantile(1-(1-level)/2, df) * s.StdErr()
	return out
}

// Comparison is the answer to "is B different from A?".
type Comparison struct {
	Method     string // MethodWelch or MethodNewcombe
	A, B       Interval
	Difference Interval // B − A, with its own interval
	Welch      float64  // the t statistic
	DF         float64  // Welch–Satterthwaite degrees of freedom
}

// Significant reports whether the difference interval excludes zero.
//
// Named for what it does rather than "is significant", because it answers one
// narrow question at one level and nothing about whether the difference
// matters. A difference can be real and irrelevant.
func (c Comparison) Significant() bool { return c.Difference.ExcludesZero() }

// Compare runs Welch's t-test for the difference of two means.
//
// Welch rather than Student's pooled test: the pooled version assumes both
// groups share a variance, and two prompts, two models or two agent versions
// routinely differ in how *consistent* they are, not just in their average. The
// pooled test's price for that assumption is a confidence interval that is too
// narrow, which is the failure mode that matters.
func Compare(a, b Sample, level float64) Comparison {
	out := Comparison{
		Method:     MethodWelch,
		A:          ConfidenceInterval(a, level),
		B:          ConfidenceInterval(b, level),
		Difference: Interval{Mean: math.NaN(), Margin: math.NaN(), Level: level},
		Welch:      math.NaN(),
		DF:         math.NaN(),
	}
	if a.N() < 2 || b.N() < 2 {
		return out
	}

	va, vb := a.Variance(), b.Variance()
	na, nb := float64(a.N()), float64(b.N())

	seSquared := va/na + vb/nb
	se := math.Sqrt(seSquared)

	diff := b.Mean() - a.Mean()
	out.Difference.Mean = diff
	out.Difference.N = a.N() + b.N()

	// Zero variance in both groups: every run gave the same answer. The
	// difference is exact, and dividing by zero to say so would produce NaN
	// where the honest answer is "no uncertainty".
	if seSquared == 0 {
		out.Difference.Margin = 0
		out.DF = na + nb - 2
		if diff != 0 {
			out.Welch = math.Inf(sign(diff))
		} else {
			out.Welch = 0
		}
		return out
	}

	// Welch–Satterthwaite.
	num := seSquared * seSquared
	den := (va*va)/(na*na*(na-1)) + (vb*vb)/(nb*nb*(nb-1))
	df := num / den

	out.DF = df
	out.Welch = diff / se
	out.Difference.Margin = studentTQuantile(1-(1-level)/2, df) * se
	return out
}

func sign(x float64) int {
	if x < 0 {
		return -1
	}
	return 1
}

// studentTQuantile returns the value t such that P(T <= t) = p, for the
// Student t distribution with df degrees of freedom.
//
// Hill's algorithm (CACM 1970, alg. 396). Compact, no dependency, and accurate
// to well under a thousandth across the range that matters here. The tests
// check it against published table values precisely because a wrong quantile
// would silently narrow every interval in the tool.
func studentTQuantile(p, df float64) float64 {
	if df <= 0 || math.IsNaN(p) {
		return math.NaN()
	}
	if p <= 0 || p >= 1 {
		return math.NaN()
	}
	// Symmetric: solve for the upper tail and mirror.
	if p < 0.5 {
		return -studentTQuantile(1-p, df)
	}

	// Two-tailed probability, which is what Hill's formulation takes.
	a := 2 * (1 - p)

	if df == 1 {
		return 1 / math.Tan(math.Pi*a/2)
	}
	if df == 2 {
		return math.Sqrt(2/(a*(2-a)) - 2)
	}

	adj := 1 / (df - 0.5)
	b := 48 / (adj * adj)
	c := ((20700*adj/b-98)*adj-16)*adj + 96.36
	d := ((94.5/(b+c))*adj-3)*adj + 1
	x := d * math.Sqrt(adj*math.Pi/2) * df

	y := math.Pow(x*a, 2/df)

	if y > 0.05+adj {
		// Normal-tail branch.
		x = normalQuantile(a / 2)
		y = x * x
		if df < 5 {
			c += 0.3 * (df - 4.5) * (x + 0.6)
		}
		c = (((0.05*d*x-5)*x-7)*x-2)*x + b + c
		y = (((((0.4*y+6.3)*y+36)*y+94.5)/c-y-3)/b + 1) * x
		y = adj * y * y
		if y > 0.002 {
			y = math.Exp(y) - 1
		} else {
			y += 0.5 * y * y
		}
	} else {
		y = ((1/(((df+6)/(df*y)-0.089*d-0.822)*(df+2)*3)+0.5/(df+4))*y-1)*
			(df+1)/(df+2) + 1/y
	}

	return math.Sqrt(df * y)
}

// normalQuantile is the inverse standard normal CDF for the upper tail
// probability p, via Acklam's rational approximation.
func normalQuantile(p float64) float64 {
	if p <= 0 || p >= 1 {
		return math.NaN()
	}
	// Acklam solves for the lower tail; this helper is called with an upper
	// tail probability, so mirror.
	return -acklam(p)
}

func acklam(p float64) float64 {
	a := [6]float64{-3.969683028665376e+01, 2.209460984245205e+02, -2.759285104469687e+02,
		1.383577518672690e+02, -3.066479806614716e+01, 2.506628277459239e+00}
	b := [5]float64{-5.447609879822406e+01, 1.615858368580409e+02, -1.556989798598866e+02,
		6.680131188771972e+01, -1.328068155288572e+01}
	c := [6]float64{-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e+00,
		-2.549732539343734e+00, 4.374664141464968e+00, 2.938163982698783e+00}
	d := [4]float64{7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e+00,
		3.754408661907416e+00}

	const plow = 0.02425
	const phigh = 1 - plow

	switch {
	case p < plow:
		q := math.Sqrt(-2 * math.Log(p))
		return (((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	case p > phigh:
		q := math.Sqrt(-2 * math.Log(1-p))
		return -(((((c[0]*q+c[1])*q+c[2])*q+c[3])*q+c[4])*q + c[5]) /
			((((d[0]*q+d[1])*q+d[2])*q+d[3])*q + 1)
	default:
		q := p - 0.5
		r := q * q
		return (((((a[0]*r+a[1])*r+a[2])*r+a[3])*r+a[4])*r + a[5]) * q /
			(((((b[0]*r+b[1])*r+b[2])*r+b[3])*r+b[4])*r + 1)
	}
}
