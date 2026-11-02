package evidence

import (
	"fmt"
	"math"
)

func DefaultPolicy() Policy {
	return Policy{Threshold: 6, MaxErrRate: .05, MinErrors: 2, EvidenceTTL: 300,
		RequireAssertions: true, MaxLatencyIncreaseMS: 100, MaxLatencyRatio: 2}
}

func (p Policy) Validate() error {
	if p.Threshold < 1 || p.EvidenceTTL < 1 || p.MinErrors < 1 ||
		math.IsNaN(p.MaxErrRate) || p.MaxErrRate <= 0 || p.MaxErrRate >= 1 ||
		math.IsNaN(p.MaxLatencyIncreaseMS) || math.IsInf(p.MaxLatencyIncreaseMS, 0) || p.MaxLatencyIncreaseMS < 0 ||
		math.IsNaN(p.MaxLatencyRatio) || math.IsInf(p.MaxLatencyRatio, 0) || p.MaxLatencyRatio < 1 {
		return fmt.Errorf("invalid evidence policy")
	}
	return nil
}

func (p Policy) Mode() string {
	if p.RequireConfidence {
		return "confidence"
	}
	return "coverage"
}
