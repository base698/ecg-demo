// Package claims demonstrates an application-owned semantic check. It is not
// an automatic transcript analyzer or a universal factuality checker.
package claims

import (
	"github.com/base698/ecg-demo/evidence"
	"strings"
)

// RuntimeFact must come from trusted application configuration or a verified
// runtime result, not from the model's response or a generated transcript.
type RuntimeFact struct {
	API      string
	Verified bool
}

// CheckAPIClaim compares a structured claim with one trusted fact. Extraction
// from speech/text, authorization to disclose the fact, and truthful rendering
// of the checked claim are the application's responsibility.
func CheckAPIClaim(claim string, runtime RuntimeFact) evidence.CheckResult {
	result := evidence.CheckResult{Definition: evidence.Definition{Name: "runtime-api-grounding", Version: "1"}, Status: "unknown"}
	claim = strings.TrimSpace(claim)
	if !runtime.Verified || strings.TrimSpace(runtime.API) == "" {
		result.Reason = "trusted runtime fact is unavailable"
		return result
	}
	if claim == "" {
		result.Reason = "no explicit verifiable answer"
		return result
	}
	if claim != runtime.API {
		result.Status = "failed"
		result.Reason = "claim contradicts the trusted runtime fact"
		return result
	}
	result.Status = "passed"
	return result
}
