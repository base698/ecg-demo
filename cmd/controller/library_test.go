package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidence/redisstore"
	"github.com/base698/ecg-demo/evidencehttp"
)

func TestDemoTraceAdapterExercisesPublicLibraries(t *testing.T) {
	c := newTestController(t)
	insts := baselineFixture(t, c)
	for _, in := range insts {
		c.rdb.HSet(testCtx, "identity:"+in.ID, "artifact", "fixture-binary")
	}
	store, err := redisstore.New(c.rdb, redisstore.Options{Namespace: "library-demo-test"})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := evidence.New(store, evidence.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(evidencehttp.NewHandler(engine, evidencehttp.ServerOptions{}))
	defer server.Close()
	t.Setenv("ECG_EVIDENCE_ENDPOINT", server.URL)
	if err = c.startLibrary(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.libraryExport.Close(context.Background())
	for i := 0; i < 6; i++ {
		spans := traceFixture("library-"+string(rune('a'+i)), insts, "list", false, 3, time.Now().Add(-time.Second))
		spans[0].Attrs["ecg.source"] = "synthetic"
		c.assemble(testCtx, spans)
		c.assemble(testCtx, spans) // OTLP replay must not duplicate library observations.
	}
	broken := traceFixture("semantic", insts, "add", false, 3, time.Now().Add(-time.Second))
	broken[0].Attrs["ecg.source"] = "browser"
	broken[1].Attrs[ecg.AttrAssertion] = "failed"
	c.assemble(testCtx, broken)
	if err = c.libraryExport.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.libraryErr != "" {
		t.Fatal(c.libraryErr)
	}
	reports, err := c.libraryClient.Series(testCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("%+v", reports)
	}
	for _, r := range reports {
		if r.Series.Source == "synthetic" {
			if r.Window.Count != 6 || r.Assessment.Status != "satisfied" {
				t.Fatal(r)
			}
			missing := r.Series.Canonical()
			missing.Source = "browser"
			report, err := engine.Evaluate(testCtx, evidence.Requirement{Series: missing})
			if err != nil || report.Assessment.Status != "missing" {
				t.Fatal(report, err)
			}
		} else if r.Assessment.Status != "failed" {
			t.Fatal(r)
		}
	}
	if c.libraryExport.Stats().Delivered != 7 {
		t.Fatal(c.libraryExport.Stats())
	}
	w := httptest.NewRecorder()
	c.handleLibrary(w, httptest.NewRequest("GET", "/api/library", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
