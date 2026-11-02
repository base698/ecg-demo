package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidencehttp"
	"github.com/base698/ecg-demo/sdk"
)

func (c *controller) startLibrary(ctx context.Context) error {
	endpoint := env("ECG_EVIDENCE_ENDPOINT", "")
	if endpoint == "" {
		return nil
	}
	client, err := evidencehttp.NewClient(endpoint, evidencehttp.ClientOptions{Token: env("ECG_API_TOKEN", "")})
	if err != nil {
		return err
	}
	exporter, err := sdk.NewAsync(ctx, client, sdk.AsyncOptions{Capacity: 256, Timeout: 2 * time.Second})
	if err != nil {
		return err
	}
	c.libraryClient, c.libraryExport = client, exporter
	return nil
}

// The demo's linear OTLP adapter establishes a path before it hands a versioned
// observation to the general-purpose library. The API never infers this path.
func (c *controller) libraryObservation(ctx context.Context, hops []hopRec, scenario, flags, assertion string, s sample) (evidence.Observation, error) {
	o := evidence.Observation{Version: evidence.WireVersion, ID: "todo:" + s.ID, CompletedAt: time.UnixMilli(s.MS), DurationMS: s.LatencyMS, Failed: s.Failed, Complete: !s.Incomplete}
	o.Series = evidence.Series{Scenario: evidence.Definition{Name: scenario, Version: "1"}, Checks: []evidence.Definition{{Name: "todo-response", Version: "1"}}, Source: s.Source, Environment: "local-demo", Schema: "todo/v1", Flags: map[string]string{}}
	for _, h := range hops {
		identity, err := c.rdb.HGetAll(ctx, "identity:"+h.Instance).Result()
		if err != nil {
			return o, err
		}
		if identity["node_key"] != h.NodeKey {
			return o, fmt.Errorf("unregistered observed release")
		}
		o.Series.Path = append(o.Series.Path, ecg.Release{Service: h.Service, Version: h.Version, Artifact: identity["artifact"], Contract: identity["contract"], Config: identity["config"]})
	}
	name, value, ok := strings.Cut(flags, "=")
	if !ok {
		return o, fmt.Errorf("unrecognized demo flag evaluation")
	}
	o.Series.Flags[name] = value
	if assertion != "passed" && assertion != "failed" {
		assertion = "unknown"
	}
	o.Checks = []evidence.CheckResult{{Definition: o.Series.Checks[0], Status: assertion}}
	return o, o.Validate()
}

// Called with c.mu held. Exporting never blocks the application's HTTP request
// on the evidence service. Delivery failures remain visible in the dashboard.
func (c *controller) exportLibraryObservation(ctx context.Context, hops []hopRec, scenario, flags, assertion string, s sample) {
	if c.libraryExport == nil {
		return
	}
	o, err := c.libraryObservation(ctx, hops, scenario, flags, assertion, s)
	if err == nil {
		err = c.libraryExport.Record(ctx, o)
	}
	if err != nil {
		c.libraryErr = "observation could not be exported: " + err.Error()
	}
}

func (c *controller) handleLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	c.mu.Lock()
	client, exporter, problem := c.libraryClient, c.libraryExport, c.libraryErr
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if client == nil {
		json.NewEncoder(w).Encode(map[string]any{"enabled": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	reports, err := client.Series(ctx)
	if err != nil {
		problem = "evidence service unavailable"
	}
	json.NewEncoder(w).Encode(map[string]any{"enabled": true, "export": exporter.Stats(), "reports": reports, "error": problem, "policy": "observation service uses its own fixed policy; demo rollout controls use the shared evaluator with the dashboard policy"})
}
