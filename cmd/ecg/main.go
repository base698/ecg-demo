// ecg runs the standalone, observation-only evidence API.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidence/memory"
	"github.com/base698/ecg-demo/evidence/redisstore"
	"github.com/base698/ecg-demo/evidencehttp"
	"github.com/redis/go-redis/v9"
)

func main() {
	address := flag.String("listen", "127.0.0.1:9100", "HTTP listen address")
	redisAddress := flag.String("redis", "", "Redis address; empty uses bounded in-memory storage")
	namespace := flag.String("namespace", "default", "Redis namespace")
	window := flag.Int("window", 300, "fresh evidence window in seconds")
	threshold := flag.Int("threshold", 6, "minimum observations per requirement")
	confidence := flag.Bool("confidence", false, "also require the conditional 95% error bound")
	flag.Parse()
	policy := evidence.DefaultPolicy()
	policy.EvidenceTTL = *window
	policy.Threshold = *threshold
	policy.RequireConfidence = *confidence
	if err := policy.Validate(); err != nil {
		log.Fatal(err)
	}
	if *window > 86400 {
		log.Fatal("window cannot exceed the configured 24-hour retention")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var store evidence.Store = memory.New(memory.Options{})
	if *redisAddress != "" {
		client := redis.NewClient(&redis.Options{Addr: *redisAddress, Password: os.Getenv("ECG_REDIS_PASSWORD")})
		defer client.Close()
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := client.Ping(pingCtx).Err()
		cancel()
		if err != nil {
			log.Fatal("Redis unavailable: ", err)
		}
		store, err = redisstore.New(client, redisstore.Options{Namespace: *namespace})
		if err != nil {
			log.Fatal(err)
		}
	}
	engine, err := evidence.New(store, policy)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: *address, Handler: evidencehttp.NewHandler(engine, evidencehttp.ServerOptions{Token: os.Getenv("ECG_API_TOKEN")}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("ECG observation API listening on %s; no deployment control", *address)
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}
}
