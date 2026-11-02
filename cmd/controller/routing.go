package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/base698/ecg-demo/ecg"
)

// A per-request plan pins all versions. At most one candidate is tested, and
// concurrent deployments wait their turn instead of certifying one another.
type routePlan struct {
	Instances map[string]string `json:"instances"`
	Candidate string            `json:"candidate,omitempty"`
	CreatedMS int64             `json:"created_ms"`
}

func (c *controller) makePlan(ctx context.Context, eligible bool) (routePlan, error) {
	p := routePlan{Instances: map[string]string{}, CreatedMS: time.Now().UnixMilli()}
	insts := c.instances(ctx)
	bases := map[string]instance{}
	for _, in := range insts {
		if in.Role == "baseline" {
			bases[in.Service] = in
			p.Instances[in.Service] = in.ID
		}
	}
	if len(bases) == 0 {
		return p, fmt.Errorf("no baseline available")
	}
	if !eligible || c.ledgerErr != "" {
		return p, nil
	}
	sort.Slice(insts, func(i, j int) bool {
		if insts[i].Registered == insts[j].Registered {
			return insts[i].ID < insts[j].ID
		}
		return insts[i].Registered < insts[j].Registered
	})
	for _, cand := range insts {
		if cand.Role != "candidate" {
			continue
		}
		base, ok := bases[cand.Service]
		if !ok {
			continue
		}
		ob := c.obligationsFor(ctx, cand, base)
		if ob.Status == "failed" || ob.Status == "unknown" {
			continue
		}
		prefix := ""
		for _, service := range c.serviceOrder(ctx) {
			if service == cand.Service {
				break
			}
			b, ok := bases[service]
			if !ok {
				return p, fmt.Errorf("missing baseline for %s", service)
			}
			prefix = ecg.Chain(prefix, b.NodeKey)
		}
		if ob.deficitFor(prefix, c.pol.Threshold) > 0 {
			p.Instances[cand.Service] = cand.ID
			p.Candidate = cand.ID
		}
		// Oldest viable candidate owns the rollout slot even while its baseline
		// warms up or it waits for the promotion loop.
		break
	}
	return p, nil
}

func encodePlan(p routePlan) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodePlan(s string) (routePlan, error) {
	var p routePlan
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

func (c *controller) routeInPlan(ctx context.Context, service, prefix string, eligible bool, p routePlan) (*routeDecision, error) {
	if len(p.Instances) == 0 || time.Now().UnixMilli()-p.CreatedMS > 60000 || p.CreatedMS > time.Now().Add(time.Second).UnixMilli() {
		return nil, fmt.Errorf("missing or expired routing plan")
	}
	insts := map[string]instance{}
	for _, in := range c.instances(ctx) {
		insts[in.ID] = in
	}
	candidateCount := 0
	for _, id := range p.Instances {
		in, ok := insts[id]
		if !ok || in.Role == "retired" {
			return nil, fmt.Errorf("planned instance unavailable")
		}
		if in.Role == "candidate" {
			candidateCount++
			if !eligible || id != p.Candidate {
				return nil, fmt.Errorf("candidate not authorized by routing plan")
			}
		}
	}
	if candidateCount > 1 {
		return nil, fmt.Errorf("multiple candidates in one request are unsupported")
	}
	in, ok := insts[p.Instances[service]]
	if !ok || in.Service != service {
		return nil, fmt.Errorf("no planned instance of %s", service)
	}
	reason := "pinned baseline"
	if in.ID == p.Candidate {
		reason = "coverage experiment"
	}
	if err := c.rdb.Incr(ctx, "routes:"+in.ID).Err(); err != nil {
		return nil, err
	}
	return &routeDecision{Instance: in.ID, Addr: in.Addr, NodeKey: in.NodeKey, Reason: reason, Prefix: ecg.Chain(prefix, in.NodeKey)}, nil
}

// Kept as a small entry point for callers that only need one route decision.
func (c *controller) pickRoute(ctx context.Context, service, prefix string, eligible bool) (*routeDecision, error) {
	p, err := c.makePlan(ctx, eligible)
	if err != nil {
		return nil, err
	}
	return c.routeInPlan(ctx, service, prefix, eligible, p)
}
