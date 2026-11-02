package evidencehttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidence/memory"
	"github.com/base698/ecg-demo/evidencehttp"
	"github.com/base698/ecg-demo/internal/testfixture"
)

func TestHTTPRoundTripAndDeclaredMissingScenario(t *testing.T) {
	e, _ := evidence.New(memory.New(memory.Options{}), evidence.DefaultPolicy())
	srv := httptest.NewServer(evidencehttp.NewHandler(e, evidencehttp.ServerOptions{Token: "test-token"}))
	defer srv.Close()
	c, err := evidencehttp.NewClient(srv.URL, evidencehttp.ClientOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	o := testfixture.Observation("one")
	if err = c.Record(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if err = c.Record(context.Background(), o); err != nil {
		t.Fatal("identical retry should deduplicate", err)
	}
	d := o.Series.Canonical()
	d.Scenario.Name = "missing"
	reports, err := c.Evaluate(context.Background(), []evidence.Requirement{{Series: o.Series}, {Series: d}})
	if err != nil || len(reports) != 2 || reports[0].Window.Count != 1 || reports[1].Assessment.Status != "missing" {
		t.Fatalf("%+v %v", reports, err)
	}
	o.Failed = true
	if err = c.Record(context.Background(), o); err == nil {
		t.Fatal("conflicting ID accepted")
	}
	unauthorized, _ := evidencehttp.NewClient(srv.URL, evidencehttp.ClientOptions{})
	if err = unauthorized.Record(context.Background(), o); err == nil {
		t.Fatal("token bypass")
	}
}

func TestHTTPRejectsMalformedAndOversizedObservations(t *testing.T) {
	e, _ := evidence.New(memory.New(memory.Options{}), evidence.DefaultPolicy())
	h := evidencehttp.NewHandler(e, evidencehttp.ServerOptions{MaxBodyBytes: 2048})
	o := testfixture.Observation("one")
	body, _ := json.Marshal(o)
	for _, raw := range [][]byte{append(append([]byte{}, body...), []byte(` {}`)...), bytes.Repeat([]byte(" "), 3000), []byte(`{"version":"bogus"}`)} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/observations", bytes.NewReader(raw)))
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/route", nil))
	if w.Code != http.StatusNotFound {
		t.Fatal("observation API exposes deployment routing")
	}
}
