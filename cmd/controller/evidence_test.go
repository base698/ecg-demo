package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/internal/democontract"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

var testCtx = context.Background()

func newTestController(t *testing.T) *controller {
	t.Helper()
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	t.Cleanup(func() { client.Close() })
	return &controller{rdb: client, pol: policy{Threshold: 6, MinErrors: 2, MaxErrRate: .05, EvidenceTTL: 300, RequireAssertions: true, MaxLatencyIncreaseMS: 100, MaxLatencyRatio: 2}}
}
func registerTest(t *testing.T, c *controller, service, version string) instance {
	t.Helper()
	in := instance{ID: service + "-" + version, Service: service, Version: version, Contract: "v1", Config: "default", Addr: "http://" + service + version, Next: map[string]string{"api": "tasks", "tasks": "store"}[service]}
	in.NodeKey = ecg.NodeKey(in.Service, in.Version, in.Contract, in.Config)
	b, _ := json.Marshal(in)
	w := httptest.NewRecorder()
	c.handleRegister(w, httptest.NewRequest("POST", "/register", bytes.NewReader(b)))
	if w.Code != 200 {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	return in
}
func baselineFixture(t *testing.T, c *controller) []instance {
	return []instance{registerTest(t, c, "api", "1"), registerTest(t, c, "tasks", "1"), registerTest(t, c, "store", "1")}
}
func traceFixture(id string, insts []instance, scenario string, failed bool, latency float64, when time.Time) []span {
	result := []span{}
	prefix := ""
	parent := ""
	for i, in := range insts {
		cid, sid := fmt.Sprintf("c%d", i), fmt.Sprintf("s%d", i)
		start := uint64(when.UnixNano())
		end := start + uint64(latency*1e6)
		result = append(result, span{TraceID: id, SpanID: cid, ParentID: parent, Kind: 3, StartNs: start, EndNs: end, Attrs: map[string]string{ecg.AttrTarget: in.Service, ecg.AttrIntended: in.ID}})
		next := ecg.Chain(prefix, in.NodeKey)
		result = append(result, span{TraceID: id, SpanID: sid, ParentID: cid, Kind: 2, StartNs: start, EndNs: end, Error: failed, Attrs: map[string]string{ecg.AttrService: in.Service, ecg.AttrVersion: in.Version, ecg.AttrNodeKey: in.NodeKey, ecg.AttrInstance: in.ID, ecg.AttrIntended: in.ID, ecg.AttrPrefixIn: prefix, ecg.AttrPrefixOut: next, ecg.AttrScenario: scenario, ecg.AttrFlags: "express=on", ecg.AttrOracle: democontract.Oracle, ecg.AttrAssertion: "passed"}})
		prefix = next
		parent = sid
	}
	return result
}
func pathKey(insts []instance, scenario string) string {
	p := ""
	for _, in := range insts {
		p = ecg.Chain(p, in.NodeKey)
	}
	return "path:" + p + "|" + scenario + "|express=on"
}
func seed(t *testing.T, c *controller, insts []instance, scenario string, n int, failed bool, latency float64) {
	t.Helper()
	for i := 0; i < n; i++ {
		c.assemble(testCtx, traceFixture(fmt.Sprintf("%s-%d-%d", scenario, time.Now().UnixNano(), i), insts, scenario, failed, latency, time.Now().Add(-time.Second)))
	}
	if c.ledgerErr != "" {
		t.Fatal(c.ledgerErr)
	}
}

func TestEvidenceDimensions(t *testing.T) {
	c := newTestController(t)
	cases := []struct {
		name   string
		r      row
		status string
	}{
		{"missing", row{}, "missing"}, {"stale", row{Total: 6}, "stale"},
		{"partial", row{Count: 5, Checked: 5}, "partial"},
		{"coverage is not confidence", row{Count: 6, Checked: 6}, "satisfied"},
		{"single error cannot pass", row{Count: 6, Checked: 6, Errors: 1}, "inconclusive"},
		{"regression", row{Count: 6, Checked: 6, Errors: 2}, "failed"},
		{"unchecked", row{Count: 6}, "inconclusive"},
		{"semantic error", row{Count: 6, Checked: 6, AssertionFailures: 1}, "failed"},
		{"telemetry uncertainty", row{Count: 6, Checked: 6, Incomplete: 1}, "unknown"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.assess(tt.r, nil); got.Status != tt.status {
				t.Fatalf("%+v", got)
			}
		})
	}
	a := c.assess(row{Count: 6, Checked: 6}, nil)
	if a.Confidence != "insufficient" || math.Abs(a.ErrorUpper95-.393037769) > 1e-6 {
		t.Fatalf("unexpected bound: %+v", a)
	}
	c.pol.RequireConfidence = true
	if c.rowStatus(row{Count: 6, Checked: 6}) == "satisfied" {
		t.Fatal("weak confidence passed strict gate")
	}
	if c.rowStatus(row{Count: 59, Checked: 59}) != "satisfied" {
		t.Fatal("59 successes should meet 5% bound")
	}
	if errorUpper95(100, 1) <= errorUpper95(100, 0) {
		t.Fatal("error must increase the bound")
	}
	base := row{Count: 6, Checked: 6, LatSum: 42}
	if a := c.assess(row{Count: 6, Checked: 6, LatSum: 984}, &base); a.Performance != "regressed" || a.Status != "failed" {
		t.Fatalf("latency regression: %+v", a)
	}
}

func TestUniqueCompleteFreshEvidence(t *testing.T) {
	t.Run("replay counted once", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		sp := traceFixture("same", b, "list", false, 5, time.Now().Add(-time.Second))
		for i := 0; i < 6; i++ {
			c.assemble(testCtx, sp)
		}
		if r := c.loadRow(testCtx, pathKey(b, "list")); r.Count != 1 {
			t.Fatalf("count=%d", r.Count)
		}
	})
	t.Run("fresh request cannot revive expired samples", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		for i := 0; i < 6; i++ {
			c.assemble(testCtx, traceFixture(fmt.Sprint(i), b, "list", false, 5, time.Now().Add(-time.Hour)))
		}
		seed(t, c, b, "list", 1, false, 5)
		r := c.loadRow(testCtx, pathKey(b, "list"))
		if r.Count != 1 || r.Total != 7 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("orphan rejected but uncertainty retained", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		sp := traceFixture("orphan", b, "list", false, 5, time.Now().Add(-time.Second))
		sp[3].ParentID = "missing"
		c.assemble(testCtx, sp)
		if r := c.loadRow(testCtx, pathKey(b, "list")); r.Count != 0 {
			t.Fatal("orphan credited")
		}
		keys, _ := c.rdb.SMembers(testCtx, "signals:"+b[1].NodeKey).Result()
		if len(keys) == 0 || c.loadRow(testCtx, keys[0]).Incomplete != 1 {
			t.Fatal("uncertainty lost")
		}
	})
	t.Run("cycle terminates without credit", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		sp := traceFixture("cycle", b, "list", false, 5, time.Now().Add(-time.Second))
		sp[0].ParentID = "s2"
		c.assemble(testCtx, sp)
		if c.loadRow(testCtx, pathKey(b, "list")).Count != 0 {
			t.Fatal("cycle credited")
		}
	})
	t.Run("mismatched hash rejected", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		sp := traceFixture("hash", b, "list", false, 5, time.Now().Add(-time.Second))
		sp[3].Attrs[ecg.AttrPrefixOut] = "forged"
		c.assemble(testCtx, sp)
		if c.loadRow(testCtx, pathKey(b, "list")).Count != 0 {
			t.Fatal("forged prefix credited")
		}
	})
	t.Run("unregistered identity rejected", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		sp := traceFixture("identity", b, "list", false, 5, time.Now().Add(-time.Second))
		sp[3].Attrs[ecg.AttrInstance] = "other"
		c.assemble(testCtx, sp)
		if c.loadRow(testCtx, pathKey(b, "list")).Count != 0 {
			t.Fatal("unknown identity credited")
		}
	})
}

func TestFullPathGateAndHealth(t *testing.T) {
	t.Run("downstream evidence cannot be substituted", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		seed(t, c, b, "list", 6, false, 5)
		cand := registerTest(t, c, "tasks", "2")
		other := registerTest(t, c, "store", "2")
		seed(t, c, []instance{b[0], cand, other}, "list", 6, false, 5)
		if ob := c.obligationsFor(testCtx, cand, b[1]); ob.Status == "satisfied" || len(ob.Rows) != 1 {
			t.Fatalf("%+v", ob)
		}
		seed(t, c, []instance{b[0], cand, b[2]}, "list", 6, false, 5)
		if ob := c.obligationsFor(testCtx, cand, b[1]); ob.Status != "satisfied" {
			t.Fatalf("%+v", ob)
		}
	})
	t.Run("new scenario failures block promotion", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		seed(t, c, b, "list", 6, false, 5)
		cand := registerTest(t, c, "tasks", "2")
		path := []instance{b[0], cand, b[2]}
		seed(t, c, path, "list", 6, false, 5)
		seed(t, c, path, "new-op", 2, true, 5)
		if ob := c.obligationsFor(testCtx, cand, b[1]); ob.Status != "failed" {
			t.Fatalf("%+v", ob)
		}
	})
	t.Run("transport failure before candidate span is retained", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		seed(t, c, b, "list", 6, false, 5)
		cand := registerTest(t, c, "tasks", "2")
		for i := 0; i < 2; i++ {
			sp := traceFixture(fmt.Sprint(i), []instance{b[0], cand}, "list", true, 5, time.Now().Add(-time.Second))
			c.assemble(testCtx, sp[:3])
		}
		if ob := c.obligationsFor(testCtx, cand, b[1]); ob.Status != "failed" {
			t.Fatalf("%+v", ob)
		}
	})
	t.Run("semantic failure blocks successful HTTP", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		seed(t, c, b, "list", 6, false, 5)
		cand := registerTest(t, c, "tasks", "2")
		sp := traceFixture("bad-response", []instance{b[0], cand, b[2]}, "list", false, 5, time.Now().Add(-time.Second))
		sp[1].Attrs[ecg.AttrAssertion] = "failed"
		c.assemble(testCtx, sp)
		if ob := c.obligationsFor(testCtx, cand, b[1]); ob.Status != "failed" {
			t.Fatalf("%+v", ob)
		}
	})
	t.Run("retired ancestor is replaced by current baseline", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		seed(t, c, b, "list", 6, false, 5)
		newAPI := registerTest(t, c, "api", "2")
		c.rdb.HSet(testCtx, "inst:"+b[0].ID, "role", "retired")
		c.rdb.HSet(testCtx, "inst:"+newAPI.ID, "role", "baseline")
		cand := registerTest(t, c, "store", "2")
		ob := c.obligationsFor(testCtx, cand, b[2])
		if len(ob.Rows) != 1 || ob.Rows[0].PrefixIn != ecg.Chain(ecg.Chain("", newAPI.NodeKey), b[1].NodeKey) {
			t.Fatalf("%+v", ob)
		}
		seed(t, c, []instance{newAPI, b[1], b[2]}, "list", 6, false, 5)
		seed(t, c, []instance{newAPI, b[1], cand}, "list", 6, false, 5)
		if ob := c.obligationsFor(testCtx, cand, b[2]); ob.Status != "satisfied" {
			t.Fatalf("%+v", ob)
		}
	})
	t.Run("promotion uses complete requirements", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		seed(t, c, b, "list", 6, false, 5)
		cand := registerTest(t, c, "tasks", "2")
		seed(t, c, []instance{b[0], cand, b[2]}, "list", 5, false, 5)
		c.promoteReady(testCtx)
		if c.baselineOf(testCtx, "tasks").ID != b[1].ID {
			t.Fatal("promoted without coverage")
		}
		seed(t, c, []instance{b[0], cand, b[2]}, "list", 1, false, 5)
		c.promoteReady(testCtx)
		if c.baselineOf(testCtx, "tasks").ID != cand.ID {
			t.Fatal("did not promote")
		}
	})
}

func TestRoutingAndLifecycle(t *testing.T) {
	t.Run("one pinned candidate", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		seed(t, c, b, "list", 6, false, 5)
		registerTest(t, c, "tasks", "2")
		registerTest(t, c, "store", "2")
		p, err := c.makePlan(testCtx, true)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for i, in := range b {
			if p.Instances[in.Service] != b[i].ID {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("plan=%+v", p)
		}
		decoded, err := decodePlan(encodePlan(p))
		if err != nil || decoded.Candidate != p.Candidate {
			t.Fatal("plan roundtrip")
		}
		safe, _ := c.makePlan(testCtx, false)
		if safe.Candidate != "" {
			t.Fatal("ineligible traffic selected candidate")
		}
	})
	t.Run("missing baseline never routes candidate", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		registerTest(t, c, "tasks", "2")
		c.rdb.Del(testCtx, "inst:"+b[1].ID)
		if _, err := c.pickRoute(testCtx, "tasks", "", false); err == nil {
			t.Fatal("unverified fallback")
		}
	})
	t.Run("retiring replica preserves shared identity", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		copy := b[1]
		copy.ID = "tasks-replica"
		raw, _ := json.Marshal(copy)
		w := httptest.NewRecorder()
		c.handleRegister(w, httptest.NewRequest("POST", "/register", bytes.NewReader(raw)))
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		c.retire(testCtx, b[1].ID, b[1].NodeKey, "baseline")
		if c.rdb.Exists(testCtx, "inv:"+b[1].NodeKey).Val() != 0 {
			t.Fatal("living replica invalidated")
		}
	})
	t.Run("same id cannot change fingerprint", func(t *testing.T) {
		c := newTestController(t)
		b := baselineFixture(t, c)
		in := b[1]
		in.Config = "different"
		in.NodeKey = ecg.NodeKey(in.Service, in.Version, in.Contract, in.Config)
		raw, _ := json.Marshal(in)
		w := httptest.NewRecorder()
		c.handleRegister(w, httptest.NewRequest("POST", "/register", bytes.NewReader(raw)))
		if w.Code != 409 {
			t.Fatalf("got %d", w.Code)
		}
	})
	t.Run("state remains valid while policy changes", func(t *testing.T) {
		c := newTestController(t)
		baselineFixture(t, c)
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				c.handlePolicy(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/policy?threshold=6&mode=coverage", nil))
			}()
			go func() {
				defer wg.Done()
				w := httptest.NewRecorder()
				c.handleState(w, httptest.NewRequest("GET", "/api/state", nil))
				if !json.Valid(w.Body.Bytes()) {
					t.Errorf("invalid state: %s", w.Body)
				}
			}()
		}
		wg.Wait()
	})
}

func TestTraceBufferDedupAndAtomicClaim(t *testing.T) {
	c := newTestController(t)
	keys := []string{"pend:t", "pending", "final:t"}
	for i := 0; i < 2; i++ {
		if _, err := bufferSpan.Run(testCtx, c.rdb, keys, "span", "{}", 100, "t").Result(); err != nil {
			t.Fatal(err)
		}
	}
	if c.rdb.HLen(testCtx, "pend:t").Val() != 1 {
		t.Fatal("span not deduplicated")
	}
	if got, err := takeTrace.Run(testCtx, c.rdb, keys, "t", 99).StringSlice(); err != nil || len(got) != 0 {
		t.Fatal("claimed trace before quiet period")
	}
	if got, err := takeTrace.Run(testCtx, c.rdb, keys, "t", 100).StringSlice(); err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if n, err := bufferSpan.Run(testCtx, c.rdb, keys, "span", "{}", 101, "t").Int64(); err != nil || n != 0 {
		t.Fatalf("late replay accepted: %d %v", n, err)
	}
}

func TestRedisReadFailureCannotSatisfy(t *testing.T) {
	c := newTestController(t)
	c.rdb.Close()
	if a := c.assess(c.loadRow(testCtx, "missing"), nil); a.Status != "unknown" {
		t.Fatal(a)
	}
	if !strings.Contains(c.obligationsFor(testCtx, instance{}, instance{}).Status, "unknown") {
		t.Fatal("read failure was not unknown")
	}
}

func TestUnapprovedCandidateDoesNotInventBaselineRequirements(t *testing.T) {
	c := newTestController(t)
	b := baselineFixture(t, c)
	seed(t, c, b, "list", 6, false, 5)
	apiCandidate := registerTest(t, c, "api", "2")
	seed(t, c, []instance{apiCandidate, b[1], b[2]}, "experimental-operation", 6, false, 5)
	tasksCandidate := registerTest(t, c, "tasks", "2")
	ob := c.obligationsFor(testCtx, tasksCandidate, b[1])
	if len(ob.Rows) != 1 || ob.Rows[0].Scenario != "list" {
		t.Fatalf("unapproved behavior polluted requirements: %+v", ob)
	}
}

func TestRetireEndpointAllowsOnlyRedundantBaselineReplica(t *testing.T) {
	c := newTestController(t)
	b := baselineFixture(t, c)
	w := httptest.NewRecorder()
	c.handleRetire(w, httptest.NewRequest("POST", "/api/retire?id="+b[1].ID, nil))
	if w.Code != 409 {
		t.Fatal("last baseline should be protected")
	}
	replica := b[1]
	replica.ID = "tasks-replica"
	raw, _ := json.Marshal(replica)
	w = httptest.NewRecorder()
	c.handleRegister(w, httptest.NewRequest("POST", "/register", bytes.NewReader(raw)))
	w = httptest.NewRecorder()
	c.handleRetire(w, httptest.NewRequest("POST", "/api/retire?id="+b[1].ID, nil))
	if w.Code != 204 || c.rdb.Exists(testCtx, "inv:"+b[1].NodeKey).Val() != 0 {
		t.Fatalf("replica retirement: %d %s", w.Code, w.Body)
	}
}

func TestPinnedPlanSurvivesBaselinePromotion(t *testing.T) {
	c := newTestController(t)
	b := baselineFixture(t, c)
	p, err := c.makePlan(testCtx, false)
	if err != nil {
		t.Fatal(err)
	}
	replacement := registerTest(t, c, "tasks", "2")
	c.rdb.HSet(testCtx, "inst:"+b[1].ID, "role", "draining")
	c.rdb.HSet(testCtx, "inst:"+replacement.ID, "role", "baseline")
	d, err := c.routeInPlan(testCtx, "tasks", ecg.Chain("", b[0].NodeKey), false, p)
	if err != nil || d.Instance != b[1].ID {
		t.Fatalf("in-flight request changed versions: %+v %v", d, err)
	}
}
