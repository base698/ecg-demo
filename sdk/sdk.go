// Package sdk records application-owned evidence and optional OpenTelemetry
// spans. It never configures global tracing, reads environment variables, starts
// a server, retries business operations or terminates the host application.
package sdk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type Recorder interface {
	Record(context.Context, evidence.Observation) error
}
type RecorderFunc func(context.Context, evidence.Observation) error

func (f RecorderFunc) Record(ctx context.Context, o evidence.Observation) error { return f(ctx, o) }

type Options struct {
	Release  ecg.Release
	Tracer   trace.Tracer
	Recorder Recorder
}
type Client struct{ options Options }

func New(o Options) (*Client, error) {
	if err := o.Release.Validate(); err != nil {
		return nil, err
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer("ecg")
	}
	return &Client{options: o}, nil
}

type Outcome struct {
	Failed   bool
	Complete bool
}
type Operation struct {
	mu          sync.Mutex
	ended       bool
	start       time.Time
	observation evidence.Observation
	span        trace.Span
	recorder    Recorder
	checks      map[evidence.Definition]evidence.CheckResult
}

// Start creates one observation for an application-defined execution. If Path
// is omitted it contains this release only. It does not infer remote releases.
func (c *Client) Start(ctx context.Context, series evidence.Series) (context.Context, *Operation, error) {
	if len(series.Path) == 0 {
		series.Path = []ecg.Release{c.options.Release}
	}
	if err := series.Validate(); err != nil {
		return ctx, nil, err
	}
	series = series.Canonical()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ctx, nil, err
	}
	flags, _ := json.Marshal(series.Flags)
	ctx, span := c.options.Tracer.Start(ctx, series.Scenario.Name, trace.WithAttributes(
		attribute.String("ecg.release", c.options.Release.Key()), attribute.String(ecg.AttrScenario, series.Scenario.Name),
		attribute.String("ecg.scenario.version", series.Scenario.Version), attribute.String(ecg.AttrFlags, string(flags)),
		attribute.String("ecg.source", series.Source), attribute.String("ecg.environment", series.Environment), attribute.String("ecg.schema", series.Schema)))
	o := &Operation{start: time.Now(), span: span, recorder: c.options.Recorder, checks: map[evidence.Definition]evidence.CheckResult{}, observation: evidence.Observation{Version: evidence.WireVersion, ID: hex.EncodeToString(id[:]), Series: series, Checks: []evidence.CheckResult{}}}
	return ctx, o, nil
}

// Check accepts only declared, versioned checks. A recorded failure is sticky.
func (o *Operation) Check(result evidence.CheckResult) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ended {
		return fmt.Errorf("operation already ended")
	}
	test := o.observation
	test.CompletedAt = time.Now()
	test.Checks = []evidence.CheckResult{result}
	if err := test.Validate(); err != nil {
		return err
	}
	if prior, ok := o.checks[result.Definition]; ok && prior.Status == "failed" {
		return nil
	}
	o.checks[result.Definition] = result
	o.span.AddEvent("ecg.check", trace.WithAttributes(attribute.String("check.name", result.Name), attribute.String("check.version", result.Version), attribute.String("check.status", result.Status)))
	return nil
}

// End records once. Recorder errors describe telemetry, not the application's
// business result. Use Async for bounded, nonblocking export on request paths.
func (o *Operation) End(ctx context.Context, result Outcome) error {
	o.mu.Lock()
	if o.ended {
		o.mu.Unlock()
		return fmt.Errorf("operation already ended")
	}
	o.ended = true
	obs := o.observation
	obs.CompletedAt = time.Now()
	obs.DurationMS = float64(time.Since(o.start)) / float64(time.Millisecond)
	obs.Complete = result.Complete
	obs.Failed = result.Failed
	for _, check := range o.checks {
		obs.Checks = append(obs.Checks, check)
	}
	obs = obs.Canonical()
	sample := obs.Sample()
	state := "unknown"
	if sample.Checked {
		state = "passed"
	}
	if sample.AssertionFailed {
		state = "failed"
	}
	o.span.SetAttributes(attribute.String(ecg.AttrAssertion, state), attribute.Bool(ecg.AttrFailed, sample.Failed))
	o.span.End()
	o.mu.Unlock()
	if o.recorder == nil {
		return nil
	}
	return o.recorder.Record(ctx, obs)
}
