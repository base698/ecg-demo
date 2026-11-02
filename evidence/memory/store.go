// Package memory provides a bounded, process-local evidence store for tests and
// small applications. Restarting the process discards all evidence.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/base698/ecg-demo/evidence"
)

type Options struct {
	MaxSeries, MaxObservations int
	Retention                  time.Duration
}
type entry struct {
	payload string
	key     string
	expires time.Time
}
type series struct {
	definition   evidence.Series
	total, last  int64
	observations map[string]evidence.Observation
}
type Store struct {
	mu        sync.Mutex
	options   Options
	rows      map[string]*series
	seen      map[string]entry
	nextPrune time.Time
}

func New(o Options) *Store {
	if o.MaxSeries <= 0 {
		o.MaxSeries = 1000
	}
	if o.MaxObservations <= 0 {
		o.MaxObservations = 100000
	}
	if o.Retention <= 0 {
		o.Retention = 24 * time.Hour
	}
	return &Store{options: o, rows: map[string]*series{}, seen: map[string]entry{}}
}

func (s *Store) Append(ctx context.Context, o evidence.Observation) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := o.Validate(); err != nil {
		return false, err
	}
	o = o.Canonical()
	raw, _ := json.Marshal(o)
	key := o.Series.Key()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if !now.Before(s.nextPrune) {
		for id, old := range s.seen {
			if !old.expires.After(now) {
				delete(s.seen, id)
				delete(s.rows[old.key].observations, id)
			}
		}
		s.nextPrune = now.Add(time.Second)
	}
	if old, ok := s.seen[o.ID]; ok {
		if old.payload != string(raw) {
			return false, evidence.ErrConflict
		}
		return false, nil
	}
	r := s.rows[key]
	if len(s.seen) >= s.options.MaxObservations || (r == nil && len(s.rows) >= s.options.MaxSeries) {
		return false, evidence.ErrCapacity
	}
	if r == nil {
		r = &series{definition: o.Series, observations: map[string]evidence.Observation{}}
		s.rows[key] = r
	}
	r.observations[o.ID] = o
	r.total++
	r.last = max(r.last, o.CompletedAt.UnixMilli())
	s.seen[o.ID] = entry{string(raw), key, now.Add(s.options.Retention)}
	return true, nil
}

func (s *Store) Snapshot(ctx context.Context, d evidence.Series, since, until time.Time) (evidence.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return evidence.Snapshot{}, err
	}
	if until.Sub(since) > s.options.Retention {
		return evidence.Snapshot{}, fmt.Errorf("requested window exceeds store retention")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := evidence.Snapshot{Samples: []evidence.Sample{}}
	if r := s.rows[d.Key()]; r != nil {
		out.Total, out.LastMS = r.total, r.last
		for _, o := range r.observations {
			if !o.CompletedAt.Before(since) && !o.CompletedAt.After(until) {
				out.Samples = append(out.Samples, o.Sample())
			}
		}
	}
	return out, nil
}

func (s *Store) Series(ctx context.Context) ([]evidence.Series, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []evidence.Series{}
	for _, r := range s.rows {
		out = append(out, r.definition.Canonical())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}
