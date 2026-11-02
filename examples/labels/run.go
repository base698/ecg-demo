// Package labels demonstrates ECG with a fictional, local-only label service.
package labels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/sdk"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
	otelsdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type RunResult struct {
	Requirements         []evidence.Requirement `json:"requirements"`
	Observations         int                    `json:"observations"`
	TraceContextObserved bool                   `json:"trace_context_observed"`
}

type labelRequest struct {
	Count int `json:"count"`
}
type labelResponse struct {
	Labels []string `json:"labels"`
}

func release(service, version string) ecg.Release {
	return ecg.Release{Service: service, Version: version, Artifact: ecg.Digest([]byte(service + version)), Contract: "label-response/v1", Config: "mock-fixture/v1"}
}

// Run uses only a loopback mock. It produces healthy single/multiple-label rows,
// a malformed HTTP 200 row, and a declared but unobserved production requirement.
func Run(ctx context.Context, recorder sdk.Recorder) (RunResult, error) {
	result := RunResult{Requirements: []evidence.Requirement{}}
	provider := otelsdk.NewTracerProvider(otelsdk.WithSampler(otelsdk.AlwaysSample()))
	defer provider.Shutdown(context.Background())
	client, err := sdk.New(sdk.Options{Release: release("label-client", "fixture-1"), Tracer: provider.Tracer("labels-example"), Recorder: recorder})
	if err != nil {
		return result, err
	}
	var propagated atomic.Bool
	for _, test := range []struct {
		count  int
		broken bool
	}{{1, false}, {2, false}, {1, true}} {
		version, scenario := "healthy-fixture", "labels.render.single"
		if test.broken {
			version = "missing-label-fixture"
		}
		if test.count > 1 {
			scenario = "labels.render.multiple"
		}
		series := evidence.Series{
			Path:     []ecg.Release{release("label-client", "fixture-1"), release("label-service", version)},
			Scenario: evidence.Definition{Name: scenario, Version: "1"},
			Checks:   []evidence.Definition{{Name: "label-response", Version: "1"}},
			Flags:    map[string]string{"format": "text", "provider": "mock"}, Source: "synthetic", Environment: "mock", Schema: "fixture-1",
		}
		result.Requirements = append(result.Requirements, evidence.Requirement{Series: series})
		server := httptest.NewServer(otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if trace.SpanContextFromContext(r.Context()).IsValid() && r.Header.Get("traceparent") != "" {
				propagated.Store(true)
			}
			if r.Method != http.MethodPost || r.URL.Path != "/labels" {
				http.NotFound(w, r)
				return
			}
			var in labelRequest
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in) != nil || in.Count < 1 || in.Count > 10 {
				http.Error(w, "invalid fixture", 400)
				return
			}
			out := labelResponse{Labels: []string{}}
			if !test.broken {
				for n := 1; n <= in.Count; n++ {
					out.Labels = append(out.Labels, fmt.Sprintf("fixture-label-%d", n))
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		}), "mock.labels", otelhttp.WithTracerProvider(provider), otelhttp.WithPropagators(propagation.TraceContext{})))
		transport := http.DefaultTransport.(*http.Transport).Clone()
		httpClient := &http.Client{Timeout: 3 * time.Second, Transport: otelhttp.NewTransport(transport, otelhttp.WithTracerProvider(provider), otelhttp.WithPropagators(propagation.TraceContext{}))}
		runErr := func() error {
			defer server.Close()
			defer transport.CloseIdleConnections()
			for range 6 {
				callCtx, op, err := client.Start(ctx, series)
				if err != nil {
					return err
				}
				out, callErr := renderLabels(callCtx, httpClient, server.URL, test.count)
				checkErr := op.Check(checkLabels(test.count, out, callErr))
				exportErr := op.End(ctx, sdk.Outcome{Failed: callErr != nil, Complete: true})
				if checkErr != nil {
					return checkErr
				}
				if exportErr != nil {
					return exportErr
				}
				if callErr != nil {
					return callErr
				}
				result.Observations++
			}
			return nil
		}()
		if runErr != nil {
			return result, runErr
		}
	}
	missing := result.Requirements[0].Series.Canonical()
	missing.Source, missing.Environment, missing.Flags["provider"] = "production", "live", "live"
	result.Requirements = append(result.Requirements, evidence.Requirement{Series: missing})
	result.TraceContextObserved = propagated.Load()
	return result, nil
}

func renderLabels(ctx context.Context, client *http.Client, endpoint string, count int) (labelResponse, error) {
	out := labelResponse{}
	body, err := json.Marshal(labelRequest{Count: count})
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/labels", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return out, fmt.Errorf("fixture returned HTTP %d", res.StatusCode)
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&out)
	return out, err
}
