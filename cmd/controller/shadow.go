package main

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
)

// Only the demo's read-only list endpoint is mirrored. Candidate output is
// discarded and cannot become a customer response. Concurrency is bounded.
func (c *controller) mirrorReads(tenant, query string) {
	if c.shadowSlots == nil {
		return
	}
	select {
	case c.shadowSlots <- struct{}{}:
	default:
		return
	}
	c.mu.Lock()
	ctx := context.Background()
	var candidates []instance
	basePlan := routePlan{Instances: map[string]string{}, Mode: "shadow", CreatedMS: time.Now().UnixMilli()}
	for _, in := range c.instances(ctx) {
		if in.Role == "baseline" {
			basePlan.Instances[in.Service] = in.ID
		}
		if in.Role == "candidate" && in.Exposure == "shadow" && in.IsolationStatus == "passed" {
			status, _ := c.shadowStatus(ctx, in)
			if status != "failed" && status != "unknown" && status != "inconclusive" {
				candidates = append(candidates, in)
			}
		}
	}
	if len(candidates) == 0 {
		c.mu.Unlock()
		<-c.shadowSlots
		return
	}
	in := candidates[c.shadowCursor%uint64(len(candidates))]
	c.shadowCursor++
	basePlan.Instances[in.Service], basePlan.Candidate = in.ID, in.ID
	rt, err := c.routeInPlan(ctx, "api", "", true, basePlan)
	c.mu.Unlock()
	if err != nil {
		<-c.shadowSlots
		return
	}
	go func() {
		defer func() { <-c.shadowSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		ctx, span := c.tracer.Start(ctx, "shadow GET /todos")
		defer span.End()
		span.SetAttributes(attribute.String("ecg.source", "shadow"), attribute.String("ecg.shadow_node", in.NodeKey), attribute.String(ecg.AttrIntended, rt.Instance), attribute.String(ecg.AttrTarget, "api"))
		var members []baggage.Member
		for k, v := range map[string]string{ecg.BagPlan: encodePlan(basePlan), ecg.BagTenant: tenant, ecg.BagEligible: "1", "ecg.mode": "shadow"} {
			m, _ := baggage.NewMember(k, v)
			members = append(members, m)
		}
		bag, _ := baggage.New(members...)
		ctx = baggage.ContextWithBaggage(ctx, bag)
		req, _ := http.NewRequestWithContext(ctx, "GET", rt.Addr+"/todos"+querySuffix(query), nil)
		req.Header.Set(ecg.HdrIntended, rt.Instance)
		resp, err := c.client.Do(req)
		if err != nil {
			span.SetStatus(codes.Error, "shadow request failed")
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode >= 500 {
			span.SetStatus(codes.Error, "shadow response failed")
		}
	}()
}
