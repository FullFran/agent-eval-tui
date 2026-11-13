package stats

import "math"

// Comparison methods.
const (
	MethodWelch    = "welch"
	MethodNewcombe = "newcombe"
)

// Wilson returns the Wilson score interval for k successes in n trials.
//
// Not the normal approximation p ± z·√(p(1−p)/n): that one collapses to zero
// width when every run agrees, so 30 successes out of 30 would read as a
// certain 100%. Agents that work are exactly the ones near the boundary, which
// is where the normal approximation is worst. Wilson stays inside [0, 1] and
// keeps a real lower bound at 30/30 (about 88.6% at 95%).
func Wilson(k, n int, level float64) Interval {
	if n <= 0 || k < 0 || k > n {
		return Interval{Mean: math.NaN(), Margin: math.NaN(), N: n, Level: level}
	}
	z := normalQuantile((1 - level) / 2)
	nf := float64(n)
	p := float64(k) / nf
	z2 := z * z
	denom := 1 + z2/nf
	centre := (p + z2/(2*nf)) / denom
	half := z / denom * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf))
	lower, upper := centre-half, centre+half
	// The closed forms are exact at the boundaries; floating point is not.
	if k == 0 {
		lower = 0
	}
	if k == n {
		upper = 1
	}
	return boundedInterval(p, lower, upper, n, level)
}

// successes counts the ones in a 0/1 sample.
func successes(s Sample) int {
	k := 0
	for _, v := range s.Values {
		if v >= 0.5 {
			k++
		}
	}
	return k
}

// ProportionInterval is Wilson over a sample of 0/1 outcomes.
func ProportionInterval(s Sample, level float64) Interval {
	return Wilson(successes(s), s.N(), level)
}

// CompareProportions returns the difference B − A of two success rates with
// Newcombe's hybrid score interval (method 10, Stat Med 1998).
//
// It combines each arm's Wilson bounds instead of a pooled standard error, so
// two unanimous arms (30/30 against 30/30) still get an interval around zero
// rather than a difference "known exactly".
func CompareProportions(a, b Sample, level float64) Comparison {
	ia, ib := ProportionInterval(a, level), ProportionInterval(b, level)
	out := Comparison{
		Method:     MethodNewcombe,
		A:          ia,
		B:          ib,
		Difference: Interval{Mean: math.NaN(), Margin: math.NaN(), Level: level},
		Welch:      math.NaN(),
		DF:         math.NaN(),
	}
	if a.N() == 0 || b.N() == 0 {
		return out
	}
	pa, pb := ia.Mean, ib.Mean
	d := pb - pa
	lower := d - math.Sqrt(sq(pb-ib.Low())+sq(ia.High()-pa))
	upper := d + math.Sqrt(sq(ib.High()-pb)+sq(pa-ia.Low()))
	out.Difference = boundedInterval(d, lower, upper, a.N()+b.N(), level)
	return out
}

func sq(x float64) float64 { return x * x }
