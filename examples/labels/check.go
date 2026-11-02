package labels

import (
	"github.com/base698/ecg-demo/evidence"
	"strings"
)

// checkLabels validates a fictional response, not a physical print operation.
func checkLabels(expected int, response labelResponse, callErr error) evidence.CheckResult {
	result := evidence.CheckResult{Definition: evidence.Definition{Name: "label-response", Version: "1"}, Status: "unknown"}
	if callErr != nil || expected < 1 {
		return result
	}
	result.Status = "failed"
	result.Reason = "expected distinct nonempty labels matching the requested count"
	if len(response.Labels) != expected {
		return result
	}
	seen := map[string]bool{}
	for _, label := range response.Labels {
		if strings.TrimSpace(label) == "" || len(label) > 512 || seen[label] {
			return result
		}
		seen[label] = true
	}
	result.Status, result.Reason = "passed", ""
	return result
}
