package testfixture

import (
	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"time"
)

func Observation(id string) evidence.Observation {
	check := evidence.Definition{Name: "label-response", Version: "1"}
	return evidence.Observation{Version: evidence.WireVersion, ID: id, CompletedAt: time.Now().Add(-time.Second), DurationMS: 4, Complete: true,
		Series: evidence.Series{Path: []ecg.Release{{Service: "label-service", Version: "2", Artifact: "binary-digest", Contract: "labels/v1", Config: "config-digest"}}, Scenario: evidence.Definition{Name: "labels.render", Version: "1"}, Checks: []evidence.Definition{check}, Flags: map[string]string{"format": "text", "provider": "mock"}, Source: "synthetic", Environment: "test", Schema: "schema-1"}, Checks: []evidence.CheckResult{{Definition: check, Status: "passed"}}}
}
