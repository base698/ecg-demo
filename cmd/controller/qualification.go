package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"github.com/base698/ecg-demo/evidence/memory"
	"github.com/base698/ecg-demo/internal/democontract"
	"github.com/redis/go-redis/v9"
)

func (in instance) release() ecg.Release {
	return ecg.Release{Service: in.Service, Version: in.Version, Artifact: in.Artifact, Contract: in.Contract, Config: in.Config}
}

func (c *controller) recordIsolation(ctx context.Context, in instance) (string, error) {
	key := "qualification:" + in.release().Key() + ":" + democontract.IsolationSuite
	previous, err := c.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return "unknown", err
	}
	if in.Isolated == nil {
		return previous["status"], nil
	}
	run := in.Isolated
	if run.Release != in.release() || run.Suite != democontract.IsolationSuite || len(run.Observations) != len(democontract.IsolationRequirements(in.release())) {
		return "unknown", fmt.Errorf("isolated suite must match the exact release and every declared case")
	}
	raw, _ := json.Marshal(run)
	digest := ecg.Digest(raw)
	if previous["digest"] == digest {
		return previous["status"], nil
	}
	p := evidence.DefaultPolicy()
	p.Threshold, p.RequireConfidence = 1, false
	e, _ := evidence.New(memory.New(memory.Options{}), p)
	expected := map[string]bool{}
	requirements := democontract.IsolationRequirements(in.release())
	for _, req := range requirements {
		expected[req.Series.Key()] = true
	}
	for _, o := range run.Observations {
		if !expected[o.Series.Key()] {
			return "unknown", fmt.Errorf("unexpected or repeated isolated case")
		}
		delete(expected, o.Series.Key())
		if _, err := e.Observe(ctx, o); err != nil {
			return "unknown", err
		}
	}
	status := "passed"
	for _, req := range requirements {
		r, err := e.Evaluate(ctx, req)
		if err != nil {
			return "unknown", err
		}
		if r.Assessment.Status != "satisfied" {
			status = "failed"
		}
	}
	// A repeat of the same immutable build cannot erase a known failing run.
	if previous["status"] == "failed" {
		status = "failed"
	}
	if err := c.rdb.HSet(ctx, key, "status", status, "digest", digest, "checked_ms", time.Now().UnixMilli()).Err(); err != nil {
		return "unknown", err
	}
	for _, o := range run.Observations {
		if c.libraryExport != nil {
			if err := c.libraryExport.Record(ctx, o); err != nil {
				c.libraryErr = "isolated evidence export failed"
			}
		}
	}
	c.event(ctx, "qualify", in.ID+": isolated checks "+status)
	return status, nil
}

func (c *controller) handleExposure(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	mode := r.URL.Query().Get("mode")
	if mode != "canary" && mode != "shadow" {
		http.Error(w, "choose canary or shadow", 400)
		return
	}
	for _, in := range c.instances(r.Context()) {
		if in.ID == r.URL.Query().Get("id") && in.Role == "candidate" && in.IsolationStatus == "passed" {
			shadow, _ := c.shadowStatus(r.Context(), in)
			if mode == "canary" && (shadow == "failed" || shadow == "unknown" || shadow == "inconclusive") {
				http.Error(w, "resolve shadow failures before canary admission", 409)
				return
			}
			if err := c.rdb.HSet(r.Context(), "inst:"+in.ID, "exposure", mode).Err(); err != nil {
				http.Error(w, "registry unavailable", 503)
				return
			}
			c.routeAssessments = nil
			c.event(r.Context(), "exposure", in.ID+" now receives "+mode+" traffic")
			w.WriteHeader(204)
			return
		}
	}
	http.Error(w, "candidate must pass isolated checks", 409)
}

// Reassess every stable version. Failure removes admission; rollback is allowed
// only to a still-running predecessor with fresh evidence for current peers.
// There is no blind restoration of a previously good version.
func (c *controller) baselineContext(ctx context.Context, replacement *instance) string {
	bases := map[string]instance{}
	for _, in := range c.instances(ctx) {
		if in.Role == "baseline" {
			bases[in.Service] = in
		}
	}
	if replacement != nil {
		bases[replacement.Service] = *replacement
	}
	prefix := ""
	for _, svc := range c.serviceOrder(ctx) {
		prefix = ecg.Chain(prefix, bases[svc].NodeKey)
	}
	return prefix
}

func (c *controller) maintainAdmissions(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	insts := c.instances(ctx)
	// Try the most recently admitted change first. After a rollback, reevaluate
	// the remaining releases against the restored dependency context.
	sort.SliceStable(insts, func(i, j int) bool { return insts[i].AdmissionSeq > insts[j].AdmissionSeq })
	for _, in := range insts {
		if in.Role != "baseline" {
			continue
		}
		ob := c.requirementsFor(ctx, in, in, false)
		health := c.assess(c.loadRow(ctx, "stable-signal:"+c.baselineContext(ctx, nil)), nil)
		bad := ob.Status == "failed" || ob.Status == "unknown" || health.Status == "failed" || health.Status == "unknown" || health.Status == "inconclusive" || in.IsolationStatus != "passed"
		admitted, err := c.rdb.HGet(ctx, "inst:"+in.ID, "qualified").Result()
		if err != nil && err != redis.Nil {
			c.ledgerErr = "admission store unavailable"
			return
		}
		if ob.Status == "satisfied" && !bad {
			if err := c.rdb.HSet(ctx, "inst:"+in.ID, "qualified", "true").Err(); err != nil {
				c.ledgerErr = "admission store unavailable"
				return
			}
			continue
		}
		// Explicit initial bootstrap has a bounded window to gather live evidence.
		if !bad && admitted != "true" && time.Now().UnixMilli()-in.Registered < 90000 {
			continue
		}
		reason := "live requirements " + ob.Status
		if health.Status == "failed" || health.Status == "unknown" || health.Status == "inconclusive" {
			reason = "live health " + health.Status
		}
		if in.IsolationStatus != "passed" {
			reason = "isolated suite " + in.IsolationStatus
		}
		var replacement *instance
		available := c.instances(ctx)
		for i := len(available) - 1; i >= 0; i-- {
			old := available[i]
			if old.Service != in.Service || old.Role != "draining" || old.IsolationStatus != "passed" {
				continue
			}
			fallback := c.requirementsFor(ctx, old, in, true)
			oldHealth := c.assess(c.loadRow(ctx, "stable-signal:"+c.baselineContext(ctx, &old)), nil)
			if fallback.Status != "satisfied" || oldHealth.Status == "failed" || oldHealth.Status == "unknown" || oldHealth.Status == "inconclusive" {
				continue
			}
			replacement = &old
			break
		}
		p := c.rdb.TxPipeline()
		// Withdraw every replica of the same immutable version.
		for _, replica := range available {
			if replica.NodeKey == in.NodeKey && replica.Role == "baseline" {
				p.HSet(ctx, "inst:"+replica.ID, "role", "suspended", "reason", reason)
			}
		}
		p.Set(ctx, "revoked:"+in.NodeKey, 1, 0)
		if replacement != nil {
			p.HSet(ctx, "inst:"+replacement.ID, "role", "baseline", "qualified", "true")
			p.Set(ctx, "baseline:"+in.Service, replacement.NodeKey, 0)
		}
		if _, err := p.Exec(ctx); err != nil {
			c.ledgerErr = "admission transition failed"
			return
		}
		c.routeAssessments = nil
		c.event(ctx, "withdraw", in.ID+": stable admission withdrawn ("+reason+")")
		if replacement != nil {
			c.event(ctx, "rollback", replacement.ID+": restored using fresh evidence")
		}
	}
	// Ninety seconds exceeds the routing plan lifetime plus request timeout.
	for _, old := range c.instances(ctx) {
		if old.Role != "draining" {
			continue
		}
		ms, err := c.rdb.HGet(ctx, "inst:"+old.ID, "draining_since").Int64()
		if err != nil || time.Now().UnixMilli()-ms < 90000 {
			continue
		}
		base := c.baselineOf(ctx, old.Service)
		if base != nil && c.requirementsFor(ctx, *base, *base, false).Status == "satisfied" {
			c.retire(ctx, old.ID, old.NodeKey, old.Role)
		}
	}
}

func (c *controller) shadowStatus(ctx context.Context, in instance) (string, int64) {
	r := c.loadRow(ctx, "shadow:"+in.NodeKey)
	a := c.assess(r, nil)
	return a.Status, r.Count
}
