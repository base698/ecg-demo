package evidence_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidence/memory"
	"github.com/base698/ecg-demo/evidence/redisstore"
	"github.com/base698/ecg-demo/internal/testfixture"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func stores(t *testing.T) map[string]evidence.Store {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { client.Close() })
	rs, err := redisstore.New(client, redisstore.Options{Namespace: "contract"})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]evidence.Store{"memory": memory.New(memory.Options{}), "redis": rs}
}
func engine(t *testing.T, s evidence.Store) *evidence.Engine {
	p := evidence.DefaultPolicy()
	p.Threshold = 2
	e, err := evidence.New(s, p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestStoreContract(t *testing.T) {
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			e := engine(t, s)
			o := testfixture.Observation("same-request")
			var added atomic.Int32
			var wg sync.WaitGroup
			for range 16 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					ok, err := e.Observe(ctx, o)
					if err != nil {
						t.Error(err)
					}
					if ok {
						added.Add(1)
					}
				}()
			}
			wg.Wait()
			if added.Load() != 1 {
				t.Fatal("duplicate counted", added.Load())
			}
			changed := o
			changed.Failed = true
			if _, err := e.Observe(ctx, changed); !errors.Is(err, evidence.ErrConflict) {
				t.Fatal("conflict silently accepted", err)
			}
			o.ID = "second"
			if _, err := e.Observe(ctx, o); err != nil {
				t.Fatal(err)
			}
			r, err := e.Evaluate(ctx, evidence.Requirement{Series: o.Series})
			if err != nil || r.Window.Count != 2 || r.Assessment.Status != "satisfied" || r.Assessment.Confidence != "insufficient" {
				t.Fatalf("%+v %v", r, err)
			}
			old := o
			old.ID = "old"
			old.CompletedAt = time.Now().Add(-time.Hour)
			if _, err := e.Observe(ctx, old); err != nil {
				t.Fatal(err)
			}
			r, err = e.Evaluate(ctx, evidence.Requirement{Series: o.Series})
			if err != nil || r.Window.Count != 2 || r.Window.Total != 3 {
				t.Fatalf("stale evidence revived: %+v %v", r, err)
			}
			for _, source := range []string{"production", "synthetic"} {
				d := o.Series.Canonical()
				d.Source = source
				r, err = e.Evaluate(ctx, evidence.Requirement{Series: d})
				if err != nil {
					t.Fatal(err)
				}
				if source == "production" && r.Assessment.Status != "missing" {
					t.Fatal("synthetic evidence credited to production")
				}
			}
			o.ID = "bad-response"
			o.Checks[0].Status = "failed"
			if _, err = e.Observe(ctx, o); err != nil {
				t.Fatal(err)
			}
			r, err = e.Evaluate(ctx, evidence.Requirement{Series: o.Series})
			if err != nil || r.Assessment.Status != "failed" {
				t.Fatalf("semantic failure did not block: %+v %v", r, err)
			}
		})
	}
}

func TestContextAndDefinitionsPartitionEvidence(t *testing.T) {
	base := testfixture.Observation("id").Series
	changes := map[string]func(*evidence.Series){
		"artifact":         func(s *evidence.Series) { s.Path[0].Artifact = "new-binary" },
		"config":           func(s *evidence.Series) { s.Path[0].Config = "other-config" },
		"scenario_version": func(s *evidence.Series) { s.Scenario.Version = "2" },
		"check_version":    func(s *evidence.Series) { s.Checks[0].Version = "2" },
		"flag":             func(s *evidence.Series) { s.Flags["format"] = "GIF" },
		"source":           func(s *evidence.Series) { s.Source = "production" },
		"environment":      func(s *evidence.Series) { s.Environment = "live" },
		"schema":           func(s *evidence.Series) { s.Schema = "schema-2" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			s := base.Canonical()
			change(&s)
			if s.Key() == base.Key() {
				t.Fatal("collapsed context")
			}
		})
	}
	a := base.Canonical()
	a.Checks = append(a.Checks, evidence.Definition{Name: "stored", Version: "1"})
	b := a.Canonical()
	b.Checks[0], b.Checks[1] = b.Checks[1], b.Checks[0]
	if a.Key() != b.Key() {
		t.Fatal("declaration order changed key")
	}
}

func TestUnknownChecksMissingRequirementsAndBaseline(t *testing.T) {
	ctx := context.Background()
	e := engine(t, memory.New(memory.Options{}))
	o := testfixture.Observation("one")
	o.Checks = nil
	for i := range 2 {
		o.ID = fmt.Sprint(i)
		if _, err := e.Observe(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	r, _ := e.Evaluate(ctx, evidence.Requirement{Series: o.Series})
	if r.Assessment.Status != "inconclusive" {
		t.Fatal(r.Assessment)
	}
	missing := o.Series.Canonical()
	missing.Scenario.Name = "labels.recover"
	r, _ = e.Evaluate(ctx, evidence.Requirement{Series: missing})
	if r.Assessment.Status != "missing" {
		t.Fatal("unexercised requirement passed")
	}
	baseline := o.Series.Canonical()
	baseline.Path[0].Version = "1"
	baseline.Source = "production"
	if _, err := e.Evaluate(ctx, evidence.Requirement{Series: o.Series, Baseline: &baseline}); err == nil {
		t.Fatal("different contexts compared")
	}
	baseline.Source = o.Series.Source
	r, err := e.Evaluate(ctx, evidence.Requirement{Series: o.Series, Baseline: &baseline})
	if err != nil || r.Assessment.Performance != "insufficient_baseline" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestCapacityAndNamespaceIsolation(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	a, _ := redisstore.New(client, redisstore.Options{Namespace: "a", MaxSeries: 1, MaxSamplesPerSeries: 1})
	b, _ := redisstore.New(client, redisstore.Options{Namespace: "b"})
	o := testfixture.Observation("one")
	for name, s := range map[string]evidence.Store{"memory": memory.New(memory.Options{MaxObservations: 1}), "redis": a} {
		t.Run(name, func(t *testing.T) {
			e := engine(t, s)
			if _, err := e.Observe(context.Background(), o); err != nil {
				t.Fatal(err)
			}
			copy := o
			copy.ID = "two"
			if _, err := e.Observe(context.Background(), copy); !errors.Is(err, evidence.ErrCapacity) {
				t.Fatal("unbounded storage", err)
			}
		})
	}
	if ds, err := b.Series(context.Background()); err != nil || len(ds) != 0 {
		t.Fatal("namespace leak", err)
	}
}

func TestValidationAndStoreOutage(t *testing.T) {
	e := engine(t, memory.New(memory.Options{}))
	o := testfixture.Observation("one")
	for _, change := range []func(*evidence.Observation){func(o *evidence.Observation) { o.Version = "wrong" }, func(o *evidence.Observation) { o.CompletedAt = time.Now().Add(time.Hour) }, func(o *evidence.Observation) { o.DurationMS = math.NaN() }, func(o *evidence.Observation) { o.Series.Source = "" }} {
		copy := o.Canonical()
		change(&copy)
		if _, err := e.Observe(context.Background(), copy); err == nil {
			t.Fatal("invalid observation accepted")
		}
	}
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	s, _ := redisstore.New(client, redisstore.Options{})
	e = engine(t, s)
	client.Close()
	report, err := e.Evaluate(context.Background(), evidence.Requirement{Series: o.Series})
	if err == nil || report.Assessment.Status != "unknown" {
		t.Fatal("unavailable store passed", report, err)
	}
}

func TestRedisBoundsDedupIDsEvenForOldObservations(t *testing.T) {
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	store, err := redisstore.New(client, redisstore.Options{Namespace: "old", MaxObservations: 1})
	if err != nil {
		t.Fatal(err)
	}
	first := testfixture.Observation("first-old")
	first.CompletedAt = time.Now().Add(-48 * time.Hour)
	if _, err = store.Append(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "second-old"
	if _, err = store.Append(context.Background(), second); !errors.Is(err, evidence.ErrCapacity) {
		t.Fatal("old samples bypassed dedup capacity", err)
	}
}

func TestPublicAssessmentFailsClosed(t *testing.T) {
	p := evidence.DefaultPolicy()
	for _, w := range []evidence.Window{{Count: -1}, {Count: 6, Errors: 7}, {Count: 6, LatSum: math.NaN()}} {
		if a := p.Assess(w, nil); a.Status != "unknown" || a.ErrorUpper95 != 1 {
			t.Fatal(a)
		}
	}
	w := evidence.Window{Count: 100, Checked: 100, Incomplete: 1}
	if a := p.Assess(w, nil); a.Status != "unknown" || a.Coverage != "incomplete" || a.Confidence != "insufficient" {
		t.Fatal(a)
	}
	if a := p.Assess(evidence.Window{Count: 6, Checked: 6}, &evidence.Window{Unavailable: true}); a.Status != "unknown" {
		t.Fatal(a)
	}
}
