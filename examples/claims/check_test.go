package claims_test

import (
	"context"
	"testing"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidence/memory"
	"github.com/base698/ecg-demo/examples/claims"
	"github.com/base698/ecg-demo/sdk"
)

func TestSuccessfulTransportDoesNotProveAnAnswer(t *testing.T) {
	for _, tt := range []struct {
		name, claim         string
		fact                claims.RuntimeFact
		status, correctness string
	}{
		{"grounded", "fixture-stream/v1", claims.RuntimeFact{API: "fixture-stream/v1", Verified: true}, "satisfied", "checks_passed"},
		{"invented_identity", "fictional-api/v9", claims.RuntimeFact{API: "fixture-stream/v1", Verified: true}, "failed", "checks_failed"},
		{"vague_answer", "", claims.RuntimeFact{API: "fixture-stream/v1", Verified: true}, "inconclusive", "not_checked"},
		{"unverified_fact", "fixture-stream/v1", claims.RuntimeFact{API: "fixture-stream/v1"}, "inconclusive", "not_checked"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := evidence.New(memory.New(memory.Options{}), evidence.DefaultPolicy())
			if err != nil {
				t.Fatal(err)
			}
			release := ecg.Release{Service: "voice-fixture", Version: "1", Artifact: "fixture-artifact", Contract: "answer/v1", Config: "fixture-model-prompt-tools/v1"}
			client, err := sdk.New(sdk.Options{Release: release, Recorder: sdk.RecorderFunc(func(ctx context.Context, o evidence.Observation) error { _, err := engine.Observe(ctx, o); return err })})
			if err != nil {
				t.Fatal(err)
			}
			series := evidence.Series{Path: []ecg.Release{release}, Scenario: evidence.Definition{Name: "assistant.runtime_api_answer", Version: "1"}, Checks: []evidence.Definition{{Name: "runtime-api-grounding", Version: "1"}}, Source: "synthetic", Environment: "mock", Schema: "answer/v1"}
			for range 6 {
				ctx, op, err := client.Start(context.Background(), series)
				if err != nil {
					t.Fatal(err)
				}
				if err = op.Check(claims.CheckAPIClaim(tt.claim, tt.fact)); err != nil {
					t.Fatal(err)
				}
				// The application finished successfully; correctness is a separate result.
				if err = op.End(ctx, sdk.Outcome{Complete: true, Failed: false}); err != nil {
					t.Fatal(err)
				}
			}
			report, err := engine.Evaluate(context.Background(), evidence.Requirement{Series: series})
			if err != nil {
				t.Fatal(err)
			}
			if report.Assessment.Status != tt.status || report.Assessment.Correctness != tt.correctness {
				t.Fatal(report)
			}
			if report.Assessment.Coverage != "covered" {
				t.Fatal(report)
			}
			if report.Assessment.Confidence != "insufficient" {
				t.Fatal(report)
			}
			next := series.Canonical()
			next.Checks[0].Version = "2"
			report, err = engine.Evaluate(context.Background(), evidence.Requirement{Series: next})
			if err != nil || report.Assessment.Status != "missing" {
				t.Fatal(report, err)
			}
		})
	}
}
