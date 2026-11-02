package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"time"

	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidence/memory"
	"github.com/base698/ecg-demo/evidencehttp"
	"github.com/base698/ecg-demo/examples/labels"
	"github.com/base698/ecg-demo/sdk"
)

func main() {
	endpoint := flag.String("endpoint", "", "ECG evidence endpoint; empty uses in-memory engine")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var recorder sdk.Recorder
	var evaluate func(context.Context, []evidence.Requirement) ([]evidence.Report, error)
	if *endpoint != "" {
		client, err := evidencehttp.NewClient(*endpoint, evidencehttp.ClientOptions{Token: os.Getenv("ECG_API_TOKEN")})
		if err != nil {
			log.Fatal(err)
		}
		recorder, evaluate = client, client.Evaluate
	} else {
		engine, err := evidence.New(memory.New(memory.Options{}), evidence.DefaultPolicy())
		if err != nil {
			log.Fatal(err)
		}
		recorder = sdk.RecorderFunc(func(ctx context.Context, o evidence.Observation) error { _, err := engine.Observe(ctx, o); return err })
		evaluate = func(ctx context.Context, requirements []evidence.Requirement) ([]evidence.Report, error) {
			out := []evidence.Report{}
			for _, req := range requirements {
				r, err := engine.Evaluate(ctx, req)
				if err != nil {
					return nil, err
				}
				out = append(out, r)
			}
			return out, nil
		}
	}
	result, err := labels.Run(ctx, recorder)
	if err != nil {
		log.Fatal(err)
	}
	reports, err := evaluate(ctx, result.Requirements)
	if err != nil {
		log.Fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(struct {
		Run     labels.RunResult  `json:"run"`
		Reports []evidence.Report `json:"reports"`
	}{result, reports}); err != nil {
		log.Fatal(err)
	}
}
