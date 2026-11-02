package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidence/memory"
	"github.com/base698/ecg-demo/internal/democontract"
	"go.opentelemetry.io/otel/baggage"
)

func TestIsolatedSuiteExercisesActualHandlers(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		c := cfg{service: "store", version: "2", contract: "v1", artifact: "test-binary", config: "test-config", instance: "store-2", slowMS: 900, corrupt: corrupt}
		run, err := isolatedChecks(c)
		if err != nil {
			t.Fatal(err)
		}
		p := evidence.DefaultPolicy()
		p.Threshold = 1
		e, _ := evidence.New(memory.New(memory.Options{}), p)
		for _, o := range run.Observations {
			if _, err := e.Observe(context.Background(), o); err != nil {
				t.Fatal(err)
			}
		}
		passed, failed := 0, 0
		for _, req := range democontract.IsolationRequirements(run.Release) {
			r, err := e.Evaluate(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if r.Assessment.Status == "satisfied" {
				passed++
			}
			if r.Assessment.Status == "failed" {
				failed++
			}
		}
		if !corrupt && passed != len(democontract.IsolationRequirements(run.Release)) {
			t.Fatalf("healthy suite: %d passed", passed)
		}
		if corrupt && failed == 0 {
			t.Fatal("corruption escaped isolated checks")
		}
	}
}

func TestShadowRejectsWritesBeforeTouchingStorage(t *testing.T) {
	m, _ := baggage.NewMember("ecg.mode", "shadow")
	bag, _ := baggage.New(m)
	s := &svc{} // nil clients make any accidental store/forward access fail this test.
	for _, method := range []string{"POST", "DELETE", "PATCH"} {
		r := httptest.NewRequest(method, "/todos", nil)
		r = r.WithContext(baggage.ContextWithBaggage(r.Context(), bag))
		w := httptest.NewRecorder()
		s.handle(w, r)
		if w.Code != 405 {
			t.Fatal("shadow write was accepted", w.Code)
		}
	}
}
