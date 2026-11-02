package evidence

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrConflict = errors.New("observation ID already has different content")
	ErrCapacity = errors.New("evidence storage capacity reached")
)

type Snapshot struct {
	Total   int64
	LastMS  int64
	Samples []Sample
}

// Store must atomically deduplicate IDs and update the corresponding series.
// Duplicate delivery returns added=false; conflicting reuse returns ErrConflict.
// Snapshot must be a consistent read, not separate counters read at different times.
type Store interface {
	Append(context.Context, Observation) (added bool, err error)
	Snapshot(context.Context, Series, time.Time, time.Time) (Snapshot, error)
	Series(context.Context) ([]Series, error)
}

type Requirement struct {
	Series   Series  `json:"series"`
	Baseline *Series `json:"baseline,omitempty"`
}
type Report struct {
	Key         string     `json:"key"`
	Series      Series     `json:"series"`
	Window      Window     `json:"window"`
	Assessment  Assessment `json:"assessment"`
	Mode        string     `json:"mode"`
	Assumptions string     `json:"assumptions"`
}

type Engine struct {
	store  Store
	policy Policy
	now    func() time.Time
}

func New(store Store, policy Policy) (*Engine, error) {
	if store == nil {
		return nil, fmt.Errorf("evidence store is required")
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &Engine{store: store, policy: policy, now: time.Now}, nil
}

func (e *Engine) Observe(ctx context.Context, o Observation) (bool, error) {
	if err := o.Validate(); err != nil {
		return false, err
	}
	if o.CompletedAt.After(e.now().Add(time.Second)) {
		return false, fmt.Errorf("observation completion is in the future")
	}
	return e.store.Append(ctx, o.Canonical())
}

func (e *Engine) Series(ctx context.Context) ([]Series, error) { return e.store.Series(ctx) }

func (e *Engine) Evaluate(ctx context.Context, req Requirement) (Report, error) {
	r := Report{Key: req.Series.Key(), Series: req.Series.Canonical(), Mode: e.policy.Mode(), Assumptions: "95% bound assumes independent representative requests; no sequential or system-wide guarantee"}
	if err := req.Series.Validate(); err != nil {
		return r, err
	}
	if req.Baseline != nil {
		if err := req.Baseline.Validate(); err != nil {
			return r, err
		}
		a, b := req.Series.Canonical(), req.Baseline.Canonical()
		if len(a.Path) != len(b.Path) {
			return r, fmt.Errorf("baseline path shape must match")
		}
		for i := range a.Path {
			if a.Path[i].Service != b.Path[i].Service {
				return r, fmt.Errorf("baseline service order must match")
			}
		}
		b.Path = a.Path
		if a.Key() != b.Key() {
			return r, fmt.Errorf("baseline must use identical scenario, checks, flags, source, environment and schema")
		}
	}
	now := e.now()
	since := now.Add(-time.Duration(e.policy.EvidenceTTL) * time.Second)
	snapshot, err := e.store.Snapshot(ctx, req.Series, since, now)
	if err != nil {
		r.Window.Unavailable = true
		r.Assessment = e.policy.Assess(r.Window, nil)
		return r, err
	}
	r.Window = window(snapshot)
	var baseline *Window
	if req.Baseline != nil {
		s, loadErr := e.store.Snapshot(ctx, *req.Baseline, since, now)
		b := window(s)
		b.Unavailable = loadErr != nil
		baseline = &b
		err = loadErr
	}
	r.Assessment = e.policy.Assess(r.Window, baseline)
	return r, err
}

func window(s Snapshot) Window {
	r := Window{Total: s.Total, LastMS: s.LastMS}
	for _, v := range s.Samples {
		r.Count++
		r.LatSum += v.LatencyMS
		if v.Failed {
			r.Errors++
		}
		if v.Checked {
			r.Checked++
		}
		if v.AssertionFailed {
			r.AssertionFailures++
		}
		if v.Incomplete {
			r.Incomplete++
		}
	}
	return r
}
