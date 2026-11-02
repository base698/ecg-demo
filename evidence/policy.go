// Package evidence evaluates versioned application observations. It does not control deployments.
package evidence

import "math"

type Window struct {
	Count             int64   `json:"count"`
	Total             int64   `json:"total"`
	Errors            int64   `json:"errors"`
	Checked           int64   `json:"checked"`
	AssertionFailures int64   `json:"assertion_failures"`
	Incomplete        int64   `json:"incomplete"`
	LatSum            float64 `json:"-"`
	LastMS            int64   `json:"last_ms"`
	Unavailable       bool    `json:"unavailable"`
}

type Sample struct {
	ID              string  `json:"id"`
	MS              int64   `json:"ms"`
	Failed          bool    `json:"failed"`
	Checked         bool    `json:"checked"`
	AssertionFailed bool    `json:"assertion_failed"`
	Incomplete      bool    `json:"incomplete"`
	LatencyMS       float64 `json:"latency_ms"`
	Source          string  `json:"source"`
}

type Assessment struct {
	Coverage      string   `json:"coverage"`
	Correctness   string   `json:"correctness"`
	Confidence    string   `json:"confidence"`
	Freshness     string   `json:"freshness"`
	Performance   string   `json:"performance"`
	ErrorUpper95  float64  `json:"error_upper_95"`
	AvgMS         float64  `json:"avg_ms"`
	BaselineAvgMS float64  `json:"baseline_avg_ms"`
	Status        string   `json:"status"`
	Reasons       []string `json:"reasons"`
}

type Policy struct {
	Threshold            int     `json:"threshold"`    // minimum fresh samples per series
	MaxErrRate           float64 `json:"max_err_rate"` // maximum observed error rate
	MinErrors            int     `json:"min_errors"`   // need at least this many errors to call it a regression
	EvidenceTTL          int     `json:"evidence_ttl_s"`
	RequireAssertions    bool    `json:"require_assertions"`
	RequireConfidence    bool    `json:"require_confidence"`
	MaxLatencyIncreaseMS float64 `json:"max_latency_increase_ms"`
	MaxLatencyRatio      float64 `json:"max_latency_ratio"`
}

// One-sided exact Clopper-Pearson upper bound. This describes iid Bernoulli
// samples, not arbitrary sampled production traffic or a sequential guarantee.
func ErrorUpper95(n, k int64) float64 {
	if n <= 0 || k < 0 || k >= n {
		return 1
	}
	if k == 0 {
		return -math.Expm1(math.Log(.05) / float64(n))
	}
	cdf := func(p float64) float64 {
		term := float64(n) * math.Log1p(-p)
		sum := term
		for i := int64(1); i <= k; i++ {
			term += math.Log(float64(n-i+1)/float64(i)) + math.Log(p) - math.Log1p(-p)
			high := math.Max(sum, term)
			sum = high + math.Log(math.Exp(sum-high)+math.Exp(term-high))
		}
		return math.Exp(sum)
	}
	lo, hi := float64(k)/float64(n), 1.0
	for i := 0; i < 60; i++ {
		mid := (lo + hi) / 2
		if cdf(mid) > .05 {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

func (p Policy) Assess(r Window, base *Window) Assessment {
	a := Assessment{Coverage: "missing", Correctness: "not_checked", Confidence: "insufficient", Freshness: "missing", Performance: "not_compared", ErrorUpper95: ErrorUpper95(r.Count, r.Errors), Status: "missing", Reasons: []string{}}
	if p.Validate() != nil || !validWindow(r) || (base != nil && !validWindow(*base)) {
		a.Status = "unknown"
		a.ErrorUpper95 = 1
		a.Reasons = append(a.Reasons, "invalid policy or evidence counts")
		return a
	}
	if r.Unavailable {
		a.Status = "unknown"
		a.Reasons = append(a.Reasons, "evidence store unavailable")
		return a
	}
	if r.Count == 0 {
		if r.Total > 0 {
			a.Status = "stale"
			a.Freshness = "stale"
			a.Reasons = append(a.Reasons, "all samples are outside the evidence window")
		} else {
			a.Reasons = append(a.Reasons, "no complete observations")
		}
		return a
	}
	a.Freshness = "fresh"
	a.Coverage = "partial"
	a.Status = "partial"
	a.AvgMS = r.LatSum / float64(r.Count)
	if r.Count >= int64(p.Threshold) {
		a.Coverage = "covered"
		a.Status = "satisfied"
	} else {
		a.Reasons = append(a.Reasons, "coverage sample minimum not reached")
	}
	if r.Checked == r.Count {
		a.Correctness = "checks_passed"
	} else if r.Checked > 0 {
		a.Correctness = "partially_checked"
	}
	if r.AssertionFailures > 0 {
		a.Correctness = "checks_failed"
		a.Status = "failed"
		a.Reasons = append(a.Reasons, "response contract assertion failed")
	}
	if r.Errors > 0 && float64(r.Errors)/float64(r.Count) > p.MaxErrRate {
		if r.Errors >= int64(p.MinErrors) {
			a.Status = "failed"
		} else if a.Status != "failed" {
			a.Status = "inconclusive"
		}
		a.Reasons = append(a.Reasons, "observed error rate exceeds policy")
	}
	if r.Incomplete > 0 {
		a.Coverage = "incomplete"
		a.ErrorUpper95 = 1
		if a.Status != "failed" {
			a.Status = "unknown"
		}
		a.Reasons = append(a.Reasons, "incomplete or mismatched telemetry")
	}
	if a.ErrorUpper95 <= p.MaxErrRate {
		a.Confidence = "supported_under_iid_assumption"
	}
	if p.RequireAssertions && r.Checked != r.Count && a.Status == "satisfied" {
		a.Status = "inconclusive"
		a.Reasons = append(a.Reasons, "some requests have no recognized response check")
	}
	if p.RequireConfidence && a.Confidence == "insufficient" && a.Status == "satisfied" {
		a.Status = "inconclusive"
		a.Reasons = append(a.Reasons, "95% error upper bound exceeds policy")
	}
	if base != nil {
		if base.Unavailable || base.Count < int64(p.Threshold) {
			a.Performance = "insufficient_baseline"
			if base.Unavailable && a.Status != "failed" {
				a.Status = "unknown"
			} else if a.Status == "satisfied" {
				a.Status = "inconclusive"
			}
			a.Reasons = append(a.Reasons, "fresh baseline samples required for latency comparison")
		} else {
			a.BaselineAvgMS = base.LatSum / float64(base.Count)
			allowance := math.Max(p.MaxLatencyIncreaseMS, a.BaselineAvgMS*(p.MaxLatencyRatio-1))
			a.Performance = "within_budget"
			if a.AvgMS > a.BaselineAvgMS+allowance {
				a.Performance = "regressed"
				if r.Count >= int64(p.Threshold) {
					a.Status = "failed"
				} else if a.Status == "satisfied" {
					a.Status = "inconclusive"
				}
				a.Reasons = append(a.Reasons, "mean latency exceeds baseline budget")
			}
		}
	}
	return a
}

// Counts are validated at the public evaluation boundary as well as ingestion.
func validWindow(r Window) bool {
	return r.Count >= 0 && r.Total >= 0 && r.Errors >= 0 && r.Errors <= r.Count &&
		r.Checked >= 0 && r.Checked <= r.Count && r.AssertionFailures >= 0 &&
		r.AssertionFailures <= r.Count && r.Incomplete >= 0 && r.Incomplete <= r.Count &&
		r.LatSum >= 0 && !math.IsNaN(r.LatSum) && !math.IsInf(r.LatSum, 0)
}
