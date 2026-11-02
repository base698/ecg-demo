package labels_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidence/memory"
	"github.com/base698/ecg-demo/evidencehttp"
	"github.com/base698/ecg-demo/examples/labels"
)

func TestGenericLabelFixtureThroughPublicSDKAndHTTP(t *testing.T) {
	engine, err := evidence.New(memory.New(memory.Options{}), evidence.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(evidencehttp.NewHandler(engine, evidencehttp.ServerOptions{Token: "fixture"}))
	defer server.Close()
	client, err := evidencehttp.NewClient(server.URL, evidencehttp.ClientOptions{Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := labels.Run(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if result.Observations != 18 || !result.TraceContextObserved {
		t.Fatal(result)
	}
	reports, err := client.Evaluate(context.Background(), result.Requirements)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"satisfied", "satisfied", "failed", "missing"} {
		if reports[i].Assessment.Status != want {
			t.Fatalf("case %d: %+v", i, reports[i])
		}
		if i < 3 && reports[i].Window.Count != 6 {
			t.Fatal(reports[i])
		}
	}
	if reports[0].Assessment.Confidence != "insufficient" || reports[2].Assessment.Correctness != "checks_failed" {
		t.Fatal(reports)
	}
}
