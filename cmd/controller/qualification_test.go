package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/base698/ecg-demo/ecg"
)

func TestIndependentCandidatesDoNotQueue(t *testing.T) {
	c := newTestController(t)
	b := baselineFixture(t, c)
	seed(t, c, b, "list", 6, false, 5)
	a := registerTest(t, c, "tasks", "2")
	z := registerTest(t, c, "store", "2")
	seen := map[string]int{}
	for range 20 {
		p, err := c.makePlan(testCtx, true)
		if err != nil {
			t.Fatal(err)
		}
		seen[p.Candidate]++
	}
	if seen[a.ID] == 0 || seen[z.ID] == 0 {
		t.Fatalf("candidate starved: %v", seen)
	}
	// The first deployment remains incomplete; the second can finish first.
	path := append([]instance(nil), b...)
	path[2] = z
	seed(t, c, path, "list", 6, false, 5)
	c.promoteReady(testCtx)
	if c.baselineOf(testCtx, "store").ID != z.ID || c.baselineOf(testCtx, "tasks").ID != b[1].ID {
		t.Fatal("independent promotion failed")
	}
	ob := c.obligationsFor(testCtx, a, b[1])
	if ob.Status == "satisfied" {
		t.Fatal("dependency change reused old qualification")
	}
}

func TestIsolationRequiredAndBoundToRelease(t *testing.T) {
	c := newTestController(t)
	b := baselineFixture(t, c)
	seed(t, c, b, "list", 6, false, 5)
	in := instance{ID: "tasks-missing", Service: "tasks", Version: "2", Artifact: "new-binary", Contract: "v1", Config: "new-config", Bootstrap: true}
	in.NodeKey = ecg.NodeKey(in.Service, in.Version, in.Contract, in.Config)
	raw, _ := json.Marshal(in)
	w := httptest.NewRecorder()
	c.handleRegister(w, httptest.NewRequest("POST", "/register", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	p, _ := c.makePlan(testCtx, true)
	if p.Candidate != "" {
		t.Fatal("untested candidate admitted")
	}
	qualified := registerTest(t, c, "tasks", "3")
	in.Isolated = qualified.Isolated
	raw, _ = json.Marshal(in)
	w = httptest.NewRecorder()
	c.handleRegister(w, httptest.NewRequest("POST", "/register", bytes.NewReader(raw)))
	if w.Code != 400 {
		t.Fatal("another version's suite accepted", w.Code)
	}
}

func TestShadowEvidenceCannotQualifyOrPoisonLive(t *testing.T) {
	c := newTestController(t)
	b := baselineFixture(t, c)
	seed(t, c, b, "list", 6, false, 5)
	cand := registerTest(t, c, "tasks", "2")
	c.rdb.HSet(testCtx, "inst:"+cand.ID, "exposure", "shadow")
	path := append([]instance(nil), b...)
	path[1] = cand
	spans := traceFixture("shadow-failure", path, "list", true, 5, time.Now().Add(-time.Second))
	spans[0].Attrs["ecg.source"], spans[0].Attrs["ecg.shadow_node"] = "shadow", cand.NodeKey
	c.assemble(testCtx, spans)
	if c.loadRow(testCtx, pathKey(path, "list")).Count != 0 {
		t.Fatal("shadow credited to live gate")
	}
	if c.loadRow(testCtx, "stable-signal:"+b[0].NodeKey).Errors != 0 {
		t.Fatal("shadow poisoned stable evidence")
	}
	if c.loadRow(testCtx, "shadow:"+cand.NodeKey).Errors != 1 {
		t.Fatal("shadow failure lost")
	}
	p, _ := c.makePlan(testCtx, true)
	if p.Candidate != "" {
		t.Fatal("shadow version served a user")
	}
}

func TestContinuousAdmissionAndEvidenceBasedRollback(t *testing.T) {
	c := newTestController(t)
	b := baselineFixture(t, c)
	seed(t, c, b, "list", 6, false, 5)
	cand := registerTest(t, c, "tasks", "2")
	path := append([]instance(nil), b...)
	path[1] = cand
	seed(t, c, path, "list", 6, false, 5)
	c.promoteReady(testCtx)
	// A semantic failure is definitive even with a successful HTTP response.
	spans := traceFixture("new-stable-regression", path, "list", false, 5, time.Now().Add(-time.Second))
	spans[1].Attrs[ecg.AttrAssertion] = "failed"
	c.assemble(testCtx, spans)
	c.maintainAdmissions(testCtx)
	base := c.baselineOf(testCtx, "tasks")
	if base == nil || base.ID != b[1].ID {
		t.Fatalf("fresh predecessor not restored: %+v", base)
	}
	if c.rdb.HGet(testCtx, "inst:"+cand.ID, "role").Val() != "suspended" {
		t.Fatal("regressing release still admitted")
	}
}

func TestExpiredEvidenceWithdrawsWithoutBlindFallback(t *testing.T) {
	c := newTestController(t)
	b := baselineFixture(t, c)
	seed(t, c, b, "list", 6, false, 5)
	c.maintainAdmissions(testCtx)
	for _, in := range b {
		c.rdb.HSet(testCtx, "inst:"+in.ID, "qualified", "true")
	}
	// Remove the fresh window but retain lifetime evidence and version identities.
	keys, _ := c.rdb.Keys(testCtx, "samples:*").Result()
	for _, key := range keys {
		c.rdb.Del(testCtx, key)
	}
	c.maintainAdmissions(testCtx)
	if base := c.baselineOf(testCtx, "tasks"); base != nil {
		t.Fatal("stale release kept stable admission")
	}
	if _, err := c.makePlan(testCtx, false); err == nil {
		t.Fatal("unqualified fallback available")
	}
}

func TestRoutingCacheDoesNotDelayFailure(t *testing.T) {
	c := newTestController(t)
	b := baselineFixture(t, c)
	seed(t, c, b, "list", 6, false, 5)
	cand := registerTest(t, c, "tasks", "2")
	p, _ := c.makePlan(testCtx, true)
	if p.Candidate != cand.ID {
		t.Fatal("candidate not scheduled")
	}
	path := append([]instance(nil), b...)
	path[1] = cand
	spans := traceFixture("cached-failure", path, "list", false, 5, time.Now().Add(-time.Second))
	spans[1].Attrs[ecg.AttrAssertion] = "failed"
	c.assemble(testCtx, spans)
	p, _ = c.makePlan(testCtx, true)
	if p.Candidate != "" {
		t.Fatal("cached qualification hid a new failure")
	}
}
