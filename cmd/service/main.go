// A versioned microservice for the todo app. The same binary runs as one of:
//
//	api   – entrypoint, classifies the operation, forwards to tasks
//	tasks – validation / business rules, forwards to store
//	store – persists todos in Redis (the leaf)
//
// Every instance computes a node key from (service, version, contract, config),
// registers with the controller on startup and heartbeats every few seconds.
// Every request extends the path prefix H(prefix_in, node_key), records ECG
// attributes on its OpenTelemetry span, asks the controller which downstream
// instance to call, and propagates everything via W3C trace context + baggage.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/base698/ecg-demo/ecg"
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

type cfg struct {
	service, version, contract, config, next, instance, controller, port string
	artifact                                                             string
	failRate                                                             float64
	latencyMS, timeoutMS, slowMS                                         int
	nodeKey                                                              string
	corrupt                                                              bool
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

var defaultNext = map[string]string{"api": "tasks", "tasks": "store", "store": ""}

func main() {
	c := cfg{
		service:    env("SERVICE", "api"),
		version:    env("VERSION", "1"),
		contract:   env("CONTRACT", "v1"),
		config:     env("CONFIG", "default"),
		port:       env("PORT", "8080"),
		controller: env("CONTROLLER", "http://controller:9000"),
	}
	c.next = env("NEXT", defaultNext[c.service])
	c.instance = env("INSTANCE_ID", fmt.Sprintf("ecg-%s-v%s", c.service, c.version))
	c.failRate, _ = strconv.ParseFloat(env("FAIL_RATE", "0"), 64)
	c.latencyMS, _ = strconv.Atoi(env("LATENCY_MS", "0"))
	c.timeoutMS, _ = strconv.Atoi(env("TIMEOUT_MS", "2000"))
	c.slowMS, _ = strconv.Atoi(env("SLOW_MS", "900"))
	c.corrupt = env("CORRUPT_RESPONSE", "false") == "true"
	artifact, err := ecg.ArtifactID()
	if err != nil {
		log.Fatal(err)
	}
	c.artifact = artifact
	c.config = effectiveConfig(c, artifact)
	c.nodeKey = ecg.NodeKey(c.service, c.version, c.contract, c.config)

	ctx := context.Background()
	shutdown := demotel.SetupOTel(ctx, c.service, c.version, c.instance,
		attribute.String(ecg.AttrNodeKey, c.nodeKey))
	defer shutdown(ctx)

	log.Printf("%s: service=%s version=%s contract=%s node_key=%s next=%q fail_rate=%.2f latency=%dms",
		c.instance, c.service, c.version, c.contract, c.nodeKey, c.next, c.failRate, c.latencyMS)

	var rdb *redis.Client
	if c.service == "store" {
		rdb = redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "redis:6379")})
	}

	// Register (blocking until the controller answers), then heartbeat forever.
	for {
		if role, err := register(c); err == nil {
			if role == "retired" {
				return
			}
			break
		} else {
			log.Printf("register: %v (retrying)", err)
			time.Sleep(2 * time.Second)
		}
	}
	go func() {
		for range time.Tick(3 * time.Second) {
			role, err := register(c)
			if err != nil {
				log.Printf("heartbeat: %v", err)
				continue
			}
			if role == "retired" {
				log.Printf("%s: retired by controller, exiting", c.instance)
				_ = shutdown(context.Background())
				os.Exit(0)
			}
		}
	}()

	var client *http.Client
	if c.next != "" {
		client = &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport,
			otelhttp.WithSpanOptions(trace.WithAttributes(attribute.String(ecg.AttrTarget, c.next))))}
	}

	s := &svc{c: c, client: client, rdb: rdb}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"instance": c.instance, "node_key": c.nodeKey, "version": c.version})
	})
	mux.HandleFunc("/", s.handle)

	h := otelhttp.NewHandler(mux, "ecg."+c.service,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return c.service + " " + r.Method + " " + r.URL.Path }),
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" }))
	log.Printf("listening on :%s", c.port)
	log.Fatal(http.ListenAndServe(":"+c.port, h))
}

type regResp struct {
	Role string `json:"role"`
}

func register(c cfg) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"id": c.instance, "service": c.service, "version": c.version, "contract": c.contract,
		"artifact": c.artifact, "config": c.config, "node_key": c.nodeKey, "addr": env("ADDR", "http://"+c.instance+":"+c.port),
		"next": c.next, "fail_rate": c.failRate, "latency_ms": c.latencyMS,
	})
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(c.controller+"/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("registration %d: %s", resp.StatusCode, b)
	}
	var rr regResp
	if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
		return "", err
	}
	return rr.Role, nil
}

type routeResp struct {
	Instance string `json:"instance"`
	Addr     string `json:"addr"`
	NodeKey  string `json:"node_key"`
	Reason   string `json:"reason"`
}

func route(ctx context.Context, c cfg, service, prefix string, eligible bool) (routeResp, error) {
	q := url.Values{"service": {service}, "prefix": {prefix}, "eligible": {strconv.FormatBool(eligible)}, "plan": {baggage.FromContext(ctx).Member(ecg.BagPlan).Value()}}
	req, err := http.NewRequestWithContext(ctx, "GET", c.controller+"/route?"+q.Encode(), nil)
	if err != nil {
		return routeResp{}, err
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return routeResp{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return routeResp{}, fmt.Errorf("route %s: %s", service, strings.TrimSpace(string(b)))
	}
	var rr routeResp
	err = json.NewDecoder(resp.Body).Decode(&rr)
	return rr, err
}

// ---------------------------------------------------------------------------

type svc struct {
	c      cfg
	client *http.Client
	rdb    *redis.Client
}

// hop is the per-request ECG context carried in baggage.
type hop struct {
	scenario, flags, tenant, prefixIn, prefixOut string
	eligible                                     bool
	chaos                                        string
}

// ecgMeta is appended to every API response so the UI can show which
// versioned path served the request.
type ecgMeta struct {
	Path     string `json:"path"`
	Prefix   string `json:"prefix"`
	Instance string `json:"instance"`
	Scenario string `json:"scenario"`
	Flags    string `json:"flags"`
	Eligible bool   `json:"eligible"`
	Outcome  string `json:"outcome"`
	Retries  int    `json:"retries"`
}

type apiResp struct {
	Todos []todo         `json:"todos,omitempty"`
	Todo  *todo          `json:"todo,omitempty"`
	Error string         `json:"error,omitempty"`
	Ecg   ecgMeta        `json:"ecg"`
	Extra map[string]any `json:"extra,omitempty"`
}

type todo struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Done     bool   `json:"done"`
	Priority string `json:"priority,omitempty"`
	Created  int64  `json:"created"`
}

func (s *svc) handle(w http.ResponseWriter, r *http.Request) {
	c := s.c
	ctx := r.Context()
	span := trace.SpanFromContext(ctx)
	bag := baggage.FromContext(ctx)

	h := hop{
		scenario: bag.Member(ecg.BagScenario).Value(),
		flags:    bag.Member(ecg.BagFlags).Value(),
		tenant:   bag.Member(ecg.BagTenant).Value(),
		eligible: bag.Member(ecg.BagEligible).Value() == "1",
		prefixIn: bag.Member(ecg.BagPrefix).Value(),
		chaos:    r.Header.Get("X-Ecg-Chaos"),
	}
	if h.tenant == "" {
		h.tenant = "anon"
	}
	body, _ := io.ReadAll(r.Body)

	// The entrypoint classifies the request into a path family and evaluates
	// feature flags. Both ride along in baggage from here on and become part of
	// the attestation key (not of the node key, which is static).
	if h.prefixIn == "" {
		h.scenario = classify(r.Method, r.URL.Path, body, h.chaos)
		h.flags = evalFlags(h.tenant)
	}
	ctx, prefix, annotateErr := sdk.AnnotateHop(ctx, sdk.Hop{
		Service: c.service, Version: c.version, Instance: c.instance, NodeKey: c.nodeKey,
		Scenario: h.scenario, Flags: h.flags, Tenant: h.tenant, Eligible: h.eligible,
		Intended: r.Header.Get(ecg.HdrIntended),
	})
	if annotateErr != nil {
		s.fail(w, span, h, "invalid execution metadata")
		return
	}
	h.prefixIn, h.prefixOut = prefix.In, prefix.Out

	if c.latencyMS > 0 {
		time.Sleep(time.Duration(c.latencyMS) * time.Millisecond)
	}
	if c.failRate > 0 && rand.Float64() < c.failRate {
		s.fail(w, span, h, "injected failure in "+c.instance)
		return
	}

	var status int
	var out apiResp
	var err error
	switch c.service {
	case "api":
		status, out, err = s.apiLogic(ctx, h, r, body)
	case "tasks":
		status, out, err = s.tasksLogic(ctx, h, r, body)
	case "store":
		status, out, err = s.storeLogic(ctx, h, r, body)
	default:
		err = fmt.Errorf("unknown service %q", c.service)
	}
	if err != nil {
		s.fail(w, span, h, err.Error())
		return
	}
	if c.corrupt {
		if out.Todo != nil {
			out.Todo.Priority = "corrupted"
		}
		if len(out.Todos) > 0 {
			out.Todos[0].Priority = "corrupted"
		}
	}
	if c.service == "api" {
		assertion, reason := checkResponse(r.Method, r.URL.Path, body, status, out)
		_ = sdk.AnnotateCheck(ctx, democontract.Oracle, assertion, reason)
	}
	me := ecg.Hop(c.service, c.version)
	if out.Ecg.Path == "" {
		out.Ecg.Path = me
	} else {
		out.Ecg.Path = me + "→" + out.Ecg.Path
	}
	if out.Ecg.Outcome == "" {
		out.Ecg.Outcome = "ok"
	}
	out.Ecg.Prefix, out.Ecg.Instance, out.Ecg.Scenario, out.Ecg.Flags, out.Ecg.Eligible =
		h.prefixOut, c.instance, h.scenario, h.flags, h.eligible
	span.SetAttributes(attribute.String(ecg.AttrOutcome, out.Ecg.Outcome), attribute.Int(ecg.AttrRetries, out.Ecg.Retries))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(out)
}

func (s *svc) fail(w http.ResponseWriter, span trace.Span, h hop, msg string) {
	span.SetAttributes(attribute.Bool(ecg.AttrFailed, true))
	span.SetStatus(codes.Error, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(apiResp{Error: msg, Ecg: ecgMeta{Path: ecg.Hop(s.c.service, s.c.version), Instance: s.c.instance, Outcome: "error", Scenario: h.scenario}})
}

// classify maps an entrypoint operation to a path family. This is the
// "classify its likely path family from entrypoint, operation…" step.
func classify(method, path string, body []byte, chaos string) string {
	var sc string
	switch {
	case method == "GET" && path == "/todos":
		sc = "list"
	case method == "POST" && path == "/todos":
		var t struct{ Title string }
		json.Unmarshal(body, &t)
		if strings.TrimSpace(t.Title) == "" || len(strings.TrimSpace(t.Title)) > 80 {
			sc = "add-invalid"
		} else {
			sc = "add"
		}
	case method == "POST" && strings.HasSuffix(path, "/complete"):
		sc = "complete"
	case method == "DELETE" && strings.HasPrefix(path, "/todos/"):
		sc = "delete"
	default:
		sc = "other"
	}
	if chaos == "slow-store" {
		sc += "~slow"
	}
	return sc
}

func evalFlags(tenant string) string {
	f := fnv.New32a()
	f.Write([]byte(tenant))
	if f.Sum32()%2 == 0 {
		return "express=on"
	}
	return "express=off"
}

// forward pins the selected instance and retries timed-out reads once. Writes
// are not replayed without an idempotency key.
func (s *svc) forward(ctx context.Context, h hop, r *http.Request, body []byte) (int, apiResp, int, error) {
	c := s.c
	rt, err := route(ctx, c, c.next, h.prefixOut, h.eligible)
	if err != nil {
		return 0, apiResp{}, 0, err
	}
	retries := 0
	for attempt := 1; attempt <= 2; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, time.Duration(c.timeoutMS)*time.Millisecond)
		req, _ := http.NewRequestWithContext(cctx, r.Method, rt.Addr+r.URL.RequestURI(), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(ecg.HdrIntended, rt.Instance)
		req.Header.Set(ecg.HdrAttempt, strconv.Itoa(attempt))
		if h.chaos != "" {
			req.Header.Set("X-Ecg-Chaos", h.chaos)
		}
		dispatchCtx, dispatch := otel.Tracer("ecg.dispatch").Start(req.Context(), "dispatch "+c.next, trace.WithAttributes(attribute.String(ecg.AttrIntended, rt.Instance), attribute.String(ecg.AttrTarget, c.next)))
		req = req.WithContext(dispatchCtx)
		resp, err := s.client.Do(req)
		if err != nil {
			dispatch.SetStatus(codes.Error, err.Error())
			dispatch.End()
			cancel()
			if errors.Is(err, context.DeadlineExceeded) && attempt == 1 && r.Method == "GET" {
				retries++
				continue
			}
			return 0, apiResp{}, retries, fmt.Errorf("downstream %s: %v", c.next, err)
		}
		var out apiResp
		decodeErr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		dispatch.End()
		cancel()
		if decodeErr != nil {
			return 0, apiResp{}, retries, fmt.Errorf("invalid downstream response: %w", decodeErr)
		}
		if resp.StatusCode >= 500 {
			return 0, out, retries, fmt.Errorf("downstream %s returned %d: %s", c.next, resp.StatusCode, out.Error)
		}
		out.Ecg.Retries += retries
		if retries > 0 && out.Ecg.Outcome == "ok" {
			out.Ecg.Outcome = "retried"
		}
		return resp.StatusCode, out, retries, nil
	}
	return 0, apiResp{}, retries, errors.New("unreachable")
}

// ---- api: entrypoint --------------------------------------------------------

func (s *svc) apiLogic(ctx context.Context, h hop, r *http.Request, body []byte) (int, apiResp, error) {
	if h.scenario == "other" {
		return 404, apiResp{Error: "unknown route"}, nil
	}
	status, out, _, err := s.forward(ctx, h, r, body)
	return status, out, err
}

// ---- tasks: business rules ---------------------------------------------------

func (s *svc) tasksLogic(ctx context.Context, h hop, r *http.Request, body []byte) (int, apiResp, error) {
	if r.Method == "POST" && r.URL.Path == "/todos" {
		var t struct{ Title string }
		json.Unmarshal(body, &t)
		title := strings.TrimSpace(t.Title)
		if title == "" {
			return 422, apiResp{Error: "title is required", Ecg: ecgMeta{Outcome: "rejected"}}, nil
		}
		if len(title) > 80 {
			return 422, apiResp{Error: "title too long (max 80)", Ecg: ecgMeta{Outcome: "rejected"}}, nil
		}
		// A tiny bit of "business logic" so versions can differ in behaviour.
		pri := "normal"
		if strings.HasPrefix(title, "!") {
			pri = "high"
		}
		body, _ = json.Marshal(map[string]string{"title": title, "priority": pri})
	}
	status, out, _, err := s.forward(ctx, h, r, body)
	return status, out, err
}

// ---- store: Redis-backed leaf ---------------------------------------------------

func (s *svc) storeLogic(ctx context.Context, h hop, r *http.Request, body []byte) (int, apiResp, error) {
	attempt, _ := strconv.Atoi(r.Header.Get(ecg.HdrAttempt))
	if h.chaos == "slow-store" && attempt <= 1 {
		// First attempt is slow enough that the caller gives up and retries.
		time.Sleep(time.Duration(s.c.slowMS) * time.Millisecond)
	}
	key := "todos:" + h.tenant
	switch {
	case r.Method == "GET" && r.URL.Path == "/todos":
		m, err := s.rdb.HGetAll(ctx, key).Result()
		if err != nil {
			return 0, apiResp{}, err
		}
		list := []todo{}
		for _, v := range m {
			var t todo
			if json.Unmarshal([]byte(v), &t) == nil {
				list = append(list, t)
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		return 200, apiResp{Todos: list}, nil
	case r.Method == "POST" && r.URL.Path == "/todos":
		var in struct{ Title, Priority string }
		json.Unmarshal(body, &in)
		id, err := s.rdb.Incr(ctx, key+":seq").Result()
		if err != nil {
			return 0, apiResp{}, err
		}
		t := todo{ID: id, Title: in.Title, Priority: in.Priority, Created: time.Now().UnixMilli()}
		b, _ := json.Marshal(t)
		if err := s.rdb.HSet(ctx, key, strconv.FormatInt(id, 10), b).Err(); err != nil {
			return 0, apiResp{}, err
		}
		return 201, apiResp{Todo: &t}, nil
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/complete"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/todos/"), "/complete")
		v, err := s.rdb.HGet(ctx, key, id).Result()
		if err == redis.Nil {
			return 404, apiResp{Error: "not found", Ecg: ecgMeta{Outcome: "not-found"}}, nil
		} else if err != nil {
			return 0, apiResp{}, err
		}
		var t todo
		json.Unmarshal([]byte(v), &t)
		t.Done = true
		b, _ := json.Marshal(t)
		if err := s.rdb.HSet(ctx, key, id, b).Err(); err != nil {
			return 0, apiResp{}, err
		}
		return 200, apiResp{Todo: &t}, nil
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/todos/"):
		id := strings.TrimPrefix(r.URL.Path, "/todos/")
		n, err := s.rdb.HDel(ctx, key, id).Result()
		if err != nil {
			return 0, apiResp{}, err
		}
		if n == 0 {
			return 404, apiResp{Error: "not found", Ecg: ecgMeta{Outcome: "not-found"}}, nil
		}
		return 200, apiResp{Extra: map[string]any{"deleted": id}}, nil
	}
	return 404, apiResp{Error: "unknown route"}, nil
}
