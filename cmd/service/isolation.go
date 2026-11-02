package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/internal/democontract"
	"github.com/base698/ecg-demo/sdk"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
)

type fixtureTransport struct{ allowed map[string]bool }

// Even a misconfigured NEXT/controller cannot send fixture requests off-host.
func (t fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !t.allowed[r.URL.Host] {
		return nil, fmt.Errorf("fixture destination is not isolated")
	}
	r = r.Clone(r.Context())
	propagation.Baggage{}.Inject(r.Context(), propagation.HeaderCarrier(r.Header))
	return http.DefaultTransport.RoundTrip(r)
}

// Run the actual candidate handlers with an in-memory Redis emulator and local
// peer handlers from the same binary. This is a contract suite, not evidence of
// compatibility with whatever versions are currently serving production.
func isolatedChecks(candidate cfg) (democontract.IsolationRun, error) {
	release := ecg.Release{Service: candidate.service, Version: candidate.version, Artifact: candidate.artifact, Contract: candidate.contract, Config: candidate.config}
	run := democontract.IsolationRun{Release: release, Suite: democontract.IsolationSuite}
	rdb, err := miniredis.Run()
	if err != nil {
		return run, err
	}
	defer rdb.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: rdb.Addr()})
	defer redisClient.Close()
	routes := map[string]routeResp{}
	allowed := map[string]bool{}
	transport := fixtureTransport{allowed: allowed}
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, ok := routes[r.URL.Query().Get("service")]
		if !ok {
			http.Error(w, "unknown fixture service", 404)
			return
		}
		json.NewEncoder(w).Encode(rt)
	}))
	defer router.Close()
	allowed[strings.TrimPrefix(router.URL, "http://")] = true
	for _, service := range []string{"store", "tasks", "api"} {
		c := cfg{service: service, version: "fixture", contract: "v1", config: "fixture", artifact: candidate.artifact, next: defaultNext[service], timeoutMS: 400, slowMS: 900}
		if service == "api" {
			c.timeoutMS = 3000
		}
		if service == candidate.service {
			c = candidate
		}
		c.instance = "fixture-" + service
		c.controller = router.URL
		c.nodeKey = ecg.NodeKey(c.service, c.version, c.contract, c.config)
		s := &svc{c: c, client: client, rdb: redisClient, routeClient: client}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := propagation.Baggage{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			s.handle(w, r.WithContext(ctx))
		}))
		defer server.Close()
		allowed[strings.TrimPrefix(server.URL, "http://")] = true
		routes[service] = routeResp{Addr: server.URL, Instance: c.instance, NodeKey: c.nodeKey}
	}
	recorder := sdk.RecorderFunc(func(_ context.Context, o evidence.Observation) error {
		run.Observations = append(run.Observations, o)
		return nil
	})
	observer, err := sdk.New(sdk.Options{Release: release, Recorder: recorder})
	if err != nil {
		return run, err
	}
	for _, requirement := range democontract.IsolationRequirements(release) {
		series := requirement.Series
		tenant := "fixture-off"
		for n := 0; ; n++ {
			tenant = fmt.Sprintf("fixture-%d", n)
			if evalFlags(tenant) == "express="+series.Flags["express"] {
				break
			}
		}
		redisClient.FlushDB(context.Background())
		// A nonempty fixture makes response corruption observable on reads.
		seed, _ := json.Marshal(todo{ID: 7, Title: "fixture", Priority: "normal"})
		redisClient.HSet(context.Background(), "todos:"+tenant, "7", seed)
		redisClient.Set(context.Background(), "todos:"+tenant+":seq", 7, 0)
		method, path, body := "GET", "/todos", ""
		switch series.Scenario.Name {
		case "add":
			method, body = "POST", `{"title":" !fixture "}`
		case "add-invalid":
			method, body = "POST", `{"title":" "}`
		case "complete":
			method, path = "POST", "/todos/7/complete"
		case "delete":
			method, path = "DELETE", "/todos/7"
		}
		ctx, op, startErr := observer.Start(context.Background(), series)
		if startErr != nil {
			return run, startErr
		}
		member, _ := baggage.NewMember(ecg.BagTenant, tenant)
		bag, _ := baggage.New(member)
		ctx = baggage.ContextWithBaggage(ctx, bag)
		req, _ := http.NewRequestWithContext(ctx, method, routes["api"].Addr+path, strings.NewReader(body))
		if series.Scenario.Name == "list~slow" {
			req.Header.Set("X-Ecg-Chaos", "slow-store")
		}
		resp, callErr := client.Do(req)
		check, reason := "failed", "fixture request failed"
		if callErr == nil {
			var out apiResp
			decodeErr := json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			if decodeErr == nil {
				check, reason = checkResponse(method, path, []byte(body), resp.StatusCode, out)
			}
		}
		if err := op.Check(evidence.CheckResult{Definition: series.Checks[0], Status: check, Reason: reason}); err != nil {
			return run, err
		}
		if err := op.End(context.Background(), sdk.Outcome{Complete: true, Failed: callErr != nil}); err != nil {
			return run, err
		}
	}
	return run, nil
}
