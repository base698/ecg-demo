package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/base698/ecg-demo/ecg"
)

type obRow struct {
	PrefixIn  string     `json:"prefix_in"`
	PrefixOld string     `json:"prefix_old"`
	PrefixNew string     `json:"prefix_new"`
	Label     string     `json:"label"`
	Scenario  string     `json:"scenario"`
	Flags     string     `json:"flags"`
	Count     int64      `json:"count"`
	Errors    int64      `json:"errors"`
	Baseline  int64      `json:"baseline_count"`
	Threshold int        `json:"threshold"`
	Status    string     `json:"status"`
	Evidence  assessment `json:"evidence"`
}

type obligation struct {
	Instance  string   `json:"instance"`
	Service   string   `json:"service"`
	Version   string   `json:"version"`
	NodeKey   string   `json:"node_key"`
	Baseline  string   `json:"baseline"`
	Rows      []obRow  `json:"rows"`
	Status    string   `json:"status"`
	Satisfied int      `json:"satisfied"`
	Deficit   int      `json:"deficit"`
	Reasons   []string `json:"reasons"`
}

// Requirements preserve observed successful path shapes and scenarios, while
// substituting the CURRENT baselines and this candidate. Old/draining versions
// are never required. Exact downstream identities remain part of the gate.
func (c *controller) obligationsFor(ctx context.Context, cand, base instance) obligation {
	ob := obligation{Instance: cand.ID, Service: cand.Service, Version: cand.Version, NodeKey: cand.NodeKey, Baseline: base.ID, Rows: []obRow{}, Reasons: []string{}}
	if c.ledgerErr != "" {
		ob.Status = "unknown"
		ob.Reasons = append(ob.Reasons, c.ledgerErr)
		return ob
	}
	bases := map[string]instance{}
	for _, in := range c.instances(ctx) {
		if in.Role == "baseline" {
			bases[in.Service] = in
		}
	}
	paths, err := c.rdb.SMembers(ctx, "paths").Result()
	if err != nil {
		ob.Status = "unknown"
		ob.Reasons = append(ob.Reasons, "cannot read baseline paths")
		return ob
	}
	sort.Strings(paths)
	required := map[string]bool{}
	for _, pk := range paths {
		m, err := c.rdb.HGetAll(ctx, "path:"+pk).Result()
		if err != nil {
			ob.Status = "unknown"
			ob.Reasons = append(ob.Reasons, "cannot read path metadata")
			return ob
		}
		nks := strings.Split(m["node_keys"], ",")
		hasBase := false
		for _, nk := range nks {
			if nk == base.NodeKey {
				hasBase = true
			}
		}
		total, _ := strconv.ParseInt(m["count"], 10, 64)
		failures, _ := strconv.ParseInt(m["errors"], 10, 64)
		approved := true
		for _, nk := range nks {
			ok, e := c.rdb.Exists(ctx, "approved:"+nk).Result()
			if e != nil {
				ob.Status = "unknown"
				ob.Reasons = append(ob.Reasons, "cannot read release history")
				return ob
			}
			if ok == 0 {
				approved = false
			}
		}
		if !approved || !hasBase || total <= failures {
			continue
		}
		services := strings.Split(m["services"], ",")
		if len(services) != len(nks) {
			ob.Status = "unknown"
			ob.Reasons = append(ob.Reasons, "path metadata is incomplete")
			continue
		}
		oldPrefix, newPrefix, pIn := "", "", ""
		labels := []string{}
		valid := true
		for _, service := range services {
			b, ok := bases[service]
			if !ok {
				valid = false
				break
			}
			oldPrefix = ecg.Chain(oldPrefix, b.NodeKey)
			chosen := b
			if service == cand.Service {
				pIn = newPrefix
				chosen = cand
			}
			newPrefix = ecg.Chain(newPrefix, chosen.NodeKey)
			labels = append(labels, ecg.Hop(chosen.Service, chosen.Version))
		}
		if !valid {
			ob.Status = "unknown"
			ob.Reasons = append(ob.Reasons, "a required service has no baseline")
			continue
		}
		sf := m["scenario"] + "|" + m["flags"]
		if required[newPrefix+"|"+sf] {
			continue
		}
		required[newPrefix+"|"+sf] = true
		r := c.loadRow(ctx, "path:"+newPrefix+"|"+sf)
		b := c.loadRow(ctx, "path:"+oldPrefix+"|"+sf)
		a := c.assess(r, &b)
		ob.Rows = append(ob.Rows, obRow{PrefixIn: pIn, PrefixOld: oldPrefix, PrefixNew: newPrefix, Label: ecg.JoinPath(labels), Scenario: m["scenario"], Flags: m["flags"], Count: r.Count, Errors: r.Errors, Baseline: b.Count, Threshold: c.pol.Threshold, Status: a.Status, Evidence: a})
		if a.Status == "satisfied" {
			ob.Satisfied++
		}
		if a.Status == "failed" {
			ob.Status = "failed"
		}
		d := c.pol.Threshold - int(r.Count)
		if d > 0 {
			ob.Deficit += d
		} else if a.Status != "satisfied" && a.Status != "failed" {
			ob.Deficit++
		}
	}
	// Safety signals include new scenarios and incomplete executions. They are
	// separate from positive path coverage and cannot disappear through filtering.
	signalKeys, err := c.rdb.SMembers(ctx, "signals:"+cand.NodeKey).Result()
	if err != nil {
		ob.Status = "unknown"
		ob.Reasons = append(ob.Reasons, "cannot read candidate health")
		return ob
	}
	for _, key := range signalKeys {
		r := c.loadRow(ctx, key)
		a := c.assess(r, nil)
		if a.Status == "failed" {
			ob.Status = "failed"
			ob.Reasons = append(ob.Reasons, "candidate has failed request or response checks")
		} else if (r.Incomplete > 0 || r.Unavailable || (r.Errors > 0 && float64(r.Errors)/float64(r.Count) > c.pol.MaxErrRate)) && ob.Status != "failed" {
			ob.Status = "unknown"
			ob.Reasons = append(ob.Reasons, "candidate has unresolved failures or telemetry gaps")
		}
	}
	if ob.Status == "" {
		switch {
		case len(ob.Rows) == 0:
			ob.Status = "waiting"
			ob.Reasons = append(ob.Reasons, "waiting for successful baseline paths")
		case ob.Satisfied == len(ob.Rows):
			ob.Status = "satisfied"
		default:
			ob.Status = "hold"
		}
	}
	sort.Slice(ob.Rows, func(i, j int) bool {
		a, b := ob.Rows[i], ob.Rows[j]
		return a.Label+a.Scenario+a.Flags < b.Label+b.Scenario+b.Flags
	})
	seenReasons := map[string]bool{}
	reasons := ob.Reasons[:0]
	for _, reason := range ob.Reasons {
		if !seenReasons[reason] {
			reasons = append(reasons, reason)
			seenReasons[reason] = true
		}
	}
	ob.Reasons = reasons
	return ob
}

func (ob obligation) deficitFor(prefixIn string, threshold int) int {
	d := 0
	for _, r := range ob.Rows {
		if r.PrefixIn != prefixIn || r.Status == "failed" || r.Baseline < int64(threshold) {
			continue
		}
		x := threshold - int(r.Count)
		if x > 0 {
			d += x
		} else if r.Status != "satisfied" {
			d++
		}
	}
	return d
}

func (c *controller) promotionLoop() {
	for range time.Tick(2 * time.Second) {
		c.promoteReady(context.Background())
	}
}

func (c *controller) promoteReady(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cand := range c.instances(ctx) {
		if cand.Role != "candidate" {
			continue
		}
		base := c.baselineOf(ctx, cand.Service)
		if base == nil {
			continue
		}
		ob := c.obligationsFor(ctx, cand, *base)
		if ob.Status != "satisfied" {
			continue
		}
		p := c.rdb.TxPipeline()
		p.HSet(ctx, "inst:"+cand.ID, "role", "baseline")
		for _, in := range c.instances(ctx) {
			if in.Service == base.Service && in.Role == "baseline" {
				p.HSet(ctx, "inst:"+in.ID, "role", "draining")
			}
		}
		p.Set(ctx, "baseline:"+cand.Service, cand.NodeKey, 0)
		p.Set(ctx, "approved:"+cand.NodeKey, 1, 0)
		if _, err := p.Exec(ctx); err != nil {
			c.ledgerErr = "promotion storage failure; reset evidence after recovery"
			return
		}
		c.event(ctx, "promote", fmt.Sprintf("%s PROMOTED (%d exact-path requirements; mode=%s); %s draining", cand.ID, len(ob.Rows), c.pol.Mode(), base.ID))
		// Recompute every other candidate against the new baseline next tick.
		return
	}
}
