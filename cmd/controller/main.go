// The ECG controller is the control plane of the demo. It holds:
//
//   - the registry: which instances exist, their node keys and roles
//     (baseline / candidate / draining / retired)
//   - the router: given (service, prefix_in, eligible) it computes the
//     prospective key H(prefix_in, node_key) and rotates eligible requests
//     across independently qualifying candidates
//   - the ledger: an OTLP/HTTP receiver that assembles traces into versioned
//     path attestations (see ledger.go)
//   - obligations & promotion: derived from the baseline's evidence (state.go)
//   - the ingress for the todo app and the dashboard (app.go, dashboard.go)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidencehttp"
	"github.com/base698/ecg-demo/internal/democontract"
	"github.com/base698/ecg-demo/internal/demotel"
	"github.com/base698/ecg-demo/sdk"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type policy = evidence.Policy

type controller struct {
	mu               sync.Mutex // serializes registry, evidence, policy, and promotion decisions
	ledgerErr        string
	libraryClient    *evidencehttp.Client
	libraryExport    *sdk.Async
	libraryErr       string
	rdb              *redis.Client
	pol              policy
	canary           map[string]bool
	tracer           trace.Tracer
	client           *http.Client
	rolloutCursor    uint64
	routeAssessments map[string]routingAssessment
	shadowCursor     uint64
	shadowSlots      chan struct{}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	ctx := context.Background()
	shutdown := demotel.SetupOTel(ctx, "ingress", "1", "controller")
	defer shutdown(ctx)

	c := &controller{
		rdb:         redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "redis:6379")}),
		canary:      map[string]bool{},
		shadowSlots: make(chan struct{}, 4),
		tracer:      otel.Tracer("ingress"),
		client: &http.Client{Timeout: 15 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport,
			otelhttp.WithSpanOptions(trace.WithAttributes(attribute.String(ecg.AttrTarget, "api"))))},
	}
	c.pol.Threshold, _ = strconv.Atoi(env("THRESHOLD", "6"))
	c.pol.MaxErrRate, _ = strconv.ParseFloat(env("MAX_ERR_RATE", "0.05"), 64)
	c.pol.MinErrors, _ = strconv.Atoi(env("MIN_ERRORS", "2"))
	c.pol.EvidenceTTL, _ = strconv.Atoi(env("EVIDENCE_TTL_S", "300"))
	c.pol.RequireAssertions = true
	c.pol.RequireConfidence = env("REQUIRE_CONFIDENCE", "false") == "true"
	c.pol.MaxLatencyIncreaseMS, _ = strconv.ParseFloat(env("MAX_LATENCY_INCREASE_MS", "100"), 64)
	c.pol.MaxLatencyRatio, _ = strconv.ParseFloat(env("MAX_LATENCY_RATIO", "2"), 64)
	if c.pol.Threshold < 1 || c.pol.EvidenceTTL < 1 || c.pol.MinErrors < 1 || c.pol.MaxErrRate <= 0 || c.pol.MaxErrRate >= 1 || c.pol.MaxLatencyIncreaseMS < 0 || c.pol.MaxLatencyRatio < 1 {
		log.Fatal("invalid evidence policy")
	}
	for _, t := range strings.Split(env("CANARY_TENANTS", "acme,initech,hooli"), ",") {
		c.canary[strings.TrimSpace(t)] = true
	}

	for {
		if err := c.rdb.Ping(ctx).Err(); err == nil {
			break
		}
		log.Println("waiting for redis…")
		time.Sleep(time.Second)
	}

	if err := c.startLibrary(ctx); err != nil {
		log.Fatal(err)
	}
	go c.finalizerLoop()
	go c.promotionLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/register", c.handleRegister)
	mux.HandleFunc("/route", c.handleRoute)
	mux.HandleFunc("/v1/traces", c.handleOTLP)
	mux.HandleFunc("/api/state", c.handleState)
	mux.HandleFunc("/api/library", c.handleLibrary)
	mux.HandleFunc("/api/retire", c.handleRetire)
	mux.HandleFunc("/api/exposure", c.handleExposure)
	mux.HandleFunc("/api/reset", c.handleReset)
	mux.HandleFunc("/api/policy", c.handlePolicy)
	mux.HandleFunc("/app/api/", c.handleIngress)
	mux.HandleFunc("/app", c.handleApp)
	mux.HandleFunc("/app/", c.handleApp)
	mux.HandleFunc("/", c.handleDashboard)

	port := env("PORT", "9000")
	log.Printf("controller listening on :%s (threshold=%d max_err=%.0f%% canary=%v)", port, c.pol.Threshold, c.pol.MaxErrRate*100, c.canary)
	server := &http.Server{Addr: ":" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	var serverErr error
	select {
	case serverErr = <-done:
	case <-signalCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
	if c.libraryExport != nil {
		flushCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := c.libraryExport.Close(flushCtx); err != nil {
			log.Printf("evidence export shutdown: %v", err)
		}
	}
	_ = c.rdb.Close()
	if serverErr != nil && !errors.Is(serverErr, http.ErrServerClosed) {
		log.Fatal(serverErr)
	}
}

// ---- registry ---------------------------------------------------------------

type instance struct {
	ID                string                     `json:"id"`
	Service           string                     `json:"service"`
	Version           string                     `json:"version"`
	Contract          string                     `json:"contract"`
	Config            string                     `json:"config"`
	Artifact          string                     `json:"artifact"`
	NodeKey           string                     `json:"node_key"`
	Addr              string                     `json:"addr"`
	Next              string                     `json:"next"`
	FailRate          float64                    `json:"fail_rate"`
	LatencyMS         int                        `json:"latency_ms"`
	Role              string                     `json:"role"`
	Registered        int64                      `json:"registered"`
	LastSeen          int64                      `json:"last_seen"`
	Routes            int64                      `json:"routes"`
	Bootstrap         bool                       `json:"bootstrap,omitempty"`
	RequestedExposure string                     `json:"requested_exposure,omitempty"`
	Isolated          *democontract.IsolationRun `json:"isolated,omitempty"`
	IsolationStatus   string                     `json:"isolation_status"`
	Exposure          string                     `json:"exposure"`
	ShadowStatus      string                     `json:"shadow_status"`
	ShadowCount       int64                      `json:"shadow_count"`
	AdmissionSeq      int64                      `json:"admission_seq"`
	Reason            string                     `json:"reason,omitempty"`
}

const instTTL = 12 * time.Second

func (c *controller) handleRegister(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx := r.Context()
	var in instance
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil || in.ID == "" || in.Service == "" {
		http.Error(w, "bad registration", 400)
		return
	}
	if err := in.release().Validate(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if in.NodeKey != ecg.NodeKey(in.Service, in.Version, in.Contract, in.Config) {
		http.Error(w, "node identity does not match registration", 400)
		return
	}
	key := "inst:" + in.ID
	now := time.Now().UnixMilli()
	existing, err := c.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		http.Error(w, "registry unavailable", 503)
		return
	}
	if existing["node_key"] != "" && (existing["node_key"] != in.NodeKey || existing["artifact"] != in.Artifact) && existing["role"] != "retired" {
		http.Error(w, "instance identity changed; use a new INSTANCE_ID", 409)
		return
	}
	isolation, err := c.recordIsolation(ctx, in)
	if err != nil {
		http.Error(w, "invalid or unavailable isolated evidence: "+err.Error(), 400)
		return
	}
	if in.RequestedExposure == "" {
		in.RequestedExposure = "canary"
	}
	if in.RequestedExposure != "canary" && in.RequestedExposure != "shadow" {
		http.Error(w, "invalid exposure", 400)
		return
	}
	role := existing["role"]
	switch {
	case role == "retired":
		// Tell it to exit, then forget it so the same id can be redeployed.
		c.rdb.Del(ctx, key)
		json.NewEncoder(w).Encode(map[string]string{"role": "retired"})
		return
	case role == "":
		c.routeAssessments = nil
		// New instance: first alive instance of a service becomes baseline,
		// every later one is a candidate that must earn promotion.
		if b := c.baselineOf(ctx, in.Service); b == nil && isolation == "passed" && c.rdb.Exists(ctx, "revoked:"+in.NodeKey).Val() == 0 && ((in.Bootstrap && c.rdb.Exists(ctx, "baseline:"+in.Service).Val() == 0) || c.rdb.Get(ctx, "baseline:"+in.Service).Val() == in.NodeKey) {
			role = "baseline"
			c.rdb.Set(ctx, "baseline:"+in.Service, in.NodeKey, 0)
			c.event(ctx, "bootstrap", fmt.Sprintf("%s registered as baseline of %s (node_key %s)", in.ID, in.Service, in.NodeKey))
		} else if b != nil && b.NodeKey == in.NodeKey && isolation == "passed" {
			role = "baseline"
		} else {
			role = "candidate"
			c.event(ctx, "deploy", fmt.Sprintf("%s registered as candidate of %s (node_key %s); obligations derived from %s", in.ID, in.Service, in.NodeKey, "current baseline"))
		}
		c.rdb.HSet(ctx, key, "registered", now, "exposure", in.RequestedExposure)
	}
	c.rdb.HSet(ctx, key, map[string]any{
		"id": in.ID, "service": in.Service, "version": in.Version, "contract": in.Contract, "config": in.Config,
		"artifact": in.Artifact, "node_key": in.NodeKey, "addr": in.Addr, "next": in.Next, "fail_rate": in.FailRate, "latency_ms": in.LatencyMS,
		"role": role, "last_seen": now, "isolation_status": isolation,
	})
	c.rdb.Expire(ctx, key, instTTL)
	c.rdb.HSet(ctx, "identity:"+in.ID, "node_key", in.NodeKey, "service", in.Service, "version", in.Version, "contract", in.Contract, "config", in.Config, "artifact", in.Artifact)
	c.rdb.Del(ctx, "inv:"+in.NodeKey)
	if role == "baseline" {
		c.rdb.Set(ctx, "approved:"+in.NodeKey, 1, 0)
	}
	c.rdb.SAdd(ctx, "services", in.Service)
	if in.Next != "" {
		c.rdb.HSet(ctx, "topology", in.Service, in.Next)
	}
	json.NewEncoder(w).Encode(map[string]string{"role": role})
}

func (c *controller) instances(ctx context.Context) []instance {
	var out []instance
	iter := c.rdb.Scan(ctx, 0, "inst:*", 100).Iterator()
	for iter.Next(ctx) {
		m, err := c.rdb.HGetAll(ctx, iter.Val()).Result()
		if err != nil || len(m) == 0 {
			continue
		}
		var in instance
		in.ID, in.Service, in.Version, in.Contract, in.Config = m["id"], m["service"], m["version"], m["contract"], m["config"]
		in.Artifact = m["artifact"]
		in.NodeKey, in.Addr, in.Next, in.Role = m["node_key"], m["addr"], m["next"], m["role"]
		in.FailRate, _ = strconv.ParseFloat(m["fail_rate"], 64)
		in.LatencyMS, _ = strconv.Atoi(m["latency_ms"])
		in.AdmissionSeq, _ = strconv.ParseInt(m["admission_seq"], 10, 64)
		in.Registered, _ = strconv.ParseInt(m["registered"], 10, 64)
		in.LastSeen, _ = strconv.ParseInt(m["last_seen"], 10, 64)
		in.IsolationStatus, in.Exposure, in.Reason = m["isolation_status"], m["exposure"], m["reason"]
		in.Routes, _ = c.rdb.Get(ctx, "routes:"+in.ID).Int64()
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Registered < out[j].Registered
	})
	return out
}

func (c *controller) baselineOf(ctx context.Context, service string) *instance {
	for _, in := range c.instances(ctx) {
		if in.Service == service && in.Role == "baseline" {
			return &in
		}
	}
	return nil
}

func (c *controller) event(ctx context.Context, kind, msg string) {
	b, _ := json.Marshal(map[string]any{"ts": time.Now().UnixMilli(), "kind": kind, "msg": msg})
	c.rdb.LPush(ctx, "events", b)
	c.rdb.LTrim(ctx, "events", 0, 99)
	log.Printf("[%s] %s", kind, msg)
}

// ---- router -----------------------------------------------------------------

type routeDecision struct {
	Instance string `json:"instance"`
	Addr     string `json:"addr"`
	NodeKey  string `json:"node_key"`
	Reason   string `json:"reason"`
	Prefix   string `json:"prospective_prefix"`
}

func (c *controller) handleRoute(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	q := r.URL.Query()
	p, err := decodePlan(q.Get("plan"))
	if err != nil {
		http.Error(w, "invalid routing plan", 400)
		return
	}
	d, err := c.routeInPlan(r.Context(), q.Get("service"), q.Get("prefix"), q.Get("eligible") == "true", p)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	json.NewEncoder(w).Encode(d)
}

// ---- ingress for the todo app ------------------------------------------------

// handleIngress is the edge: it starts the trace, decides eligibility from the
// tenant, routes to an api instance and proxies the call. This is the same
// path the synthetic loadgen takes. The library retains their source labels.
func (c *controller) handleIngress(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/app/api")
	tenant := r.Header.Get("X-Tenant")
	if tenant == "" {
		tenant = "anon"
	}
	eligible := c.canary[tenant]
	source := "browser"
	if r.Header.Get("X-Ecg-Source") == "synthetic" {
		source = "synthetic"
	}
	// A caller cannot label a live write as shadow and bypass live accounting.

	ctx, span := c.tracer.Start(r.Context(), "ingress "+r.Method+" "+path, trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String(ecg.AttrTenant, tenant), attribute.Bool(ecg.AttrEligible, eligible),
			attribute.String("ecg.source", source)))
	defer span.End()

	c.mu.Lock()
	plan, planErr := c.makePlan(ctx, eligible)
	var rt *routeDecision
	if planErr == nil {
		rt, planErr = c.routeInPlan(ctx, "api", "", eligible, plan)
	}
	c.mu.Unlock()
	if planErr != nil {
		http.Error(w, planErr.Error(), 503)
		return
	}
	span.SetAttributes(attribute.String(ecg.AttrIntended, rt.Instance), attribute.String(ecg.AttrTarget, "api"))
	members := []baggage.Member{}
	for k, v := range map[string]string{ecg.BagPlan: encodePlan(plan), ecg.BagTenant: tenant, ecg.BagEligible: map[bool]string{true: "1", false: "0"}[eligible]} {
		m, _ := baggage.NewMember(k, v)
		members = append(members, m)
	}
	bag, _ := baggage.New(members...)
	ctx = baggage.ContextWithBaggage(ctx, bag)

	body, bodyErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if bodyErr != nil {
		http.Error(w, "request too large", 413)
		return
	}
	if r.Method == "GET" && path == "/todos" {
		c.mirrorReads(tenant, r.URL.RawQuery)
	}
	req, _ := http.NewRequestWithContext(ctx, r.Method, rt.Addr+path+querySuffix(r.URL.RawQuery), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(ecg.HdrIntended, rt.Instance)
	if ch := r.Header.Get("X-Ecg-Chaos"); ch != "" {
		req.Header.Set("X-Ecg-Chaos", ch)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, `{"error":"`+err.Error()+`"}`, 502)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Ecg-Route", rt.Instance+" ("+rt.Reason+")")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// ---- admin ------------------------------------------------------------------

func (c *controller) handleRetire(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	ctx := r.Context()
	id := r.URL.Query().Get("id")
	m, _ := c.rdb.HGetAll(ctx, "inst:"+id).Result()
	if len(m) == 0 {
		http.Error(w, "unknown instance", 404)
		return
	}
	otherReplica := false
	for _, in := range c.instances(ctx) {
		if in.ID != id && in.Service == m["service"] && in.NodeKey == m["node_key"] && in.Role == "baseline" {
			otherReplica = true
		}
	}
	if m["role"] == "baseline" && !otherReplica && r.URL.Query().Get("force") != "true" {
		http.Error(w, "refusing to retire the baseline; promote a candidate first", 409)
		return
	}
	c.retire(ctx, id, m["node_key"], m["role"])
	w.WriteHeader(204)
}

// retire changes reachability, preserving historical evidence. A live replica
// with the same identity keeps that identity applicable.
func (c *controller) retire(ctx context.Context, id, nodeKey, role string) {
	c.routeAssessments = nil
	c.rdb.HSet(ctx, "inst:"+id, "role", "retired")
	c.rdb.Expire(ctx, "inst:"+id, instTTL)
	labels := c.labelsForNode(ctx, nodeKey)
	alive := false
	for _, in := range c.instances(ctx) {
		if in.ID != id && in.NodeKey == nodeKey && in.Role != "retired" {
			alive = true
		}
	}
	if !alive {
		c.rdb.Set(ctx, "inv:"+nodeKey, time.Now().UnixMilli(), 0)
	}
	c.event(ctx, "retire", fmt.Sprintf("%s retired (was %s); historical evidence retained for %d prefix(es): %s", id, role, len(labels), strings.Join(labels, ", ")))
}

func (c *controller) handleReset(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routeAssessments = nil
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	ctx := r.Context()
	for _, pat := range []string{"hoprow:*", "hopmeta:*", "hops:*", "rows:*", "path:*", "pend:*", "inv:*", "routes:*", "samples:*", "signals:*", "signal:*"} {
		iter := c.rdb.Scan(ctx, 0, pat, 200).Iterator()
		for iter.Next(ctx) {
			c.rdb.Del(ctx, iter.Val())
		}
	}
	c.rdb.Del(ctx, "paths", "recent", "pending", "stats", "events", "scenarios")
	c.ledgerErr = ""
	c.event(ctx, "reset", "all evidence cleared; replay tombstones retained")
	w.WriteHeader(204)
}

func (c *controller) handlePolicy(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routeAssessments = nil
	if r.Method == "POST" {
		if mode := r.URL.Query().Get("mode"); mode != "" {
			if mode != "coverage" && mode != "confidence" {
				http.Error(w, "invalid mode", 400)
				return
			}
			c.pol.RequireConfidence = mode == "confidence"
		}
		if v, err := strconv.Atoi(r.URL.Query().Get("threshold")); err == nil && v > 0 {
			c.pol.Threshold = v
			c.event(r.Context(), "policy", fmt.Sprintf("threshold set to %d", v))
		}
	}
	json.NewEncoder(w).Encode(c.pol)
}

func querySuffix(q string) string {
	if q != "" {
		return "?" + q
	}
	return ""
}
