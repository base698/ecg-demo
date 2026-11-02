package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/redis/go-redis/v9"
)

type redisZRange = redis.ZRangeBy

type node struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Service string `json:"service"`
	Version string `json:"version"`
	Role    string `json:"role"`
	NodeKey string `json:"node_key"`
	Alive   bool   `json:"alive"`
}
type edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Count  int64  `json:"count"`
	Errors int64  `json:"errors"`
	Status string `json:"status"`
	Rows   int    `json:"rows"`
}
type pathRec struct {
	Label    string     `json:"label"`
	Scenario string     `json:"scenario"`
	Flags    string     `json:"flags"`
	Count    int64      `json:"count"`
	Total    int64      `json:"total"`
	Errors   int64      `json:"errors"`
	AvgMS    float64    `json:"avg_ms"`
	LastMS   int64      `json:"last_ms"`
	Status   string     `json:"status"`
	Hops     []string   `json:"hops"`
	Evidence assessment `json:"evidence"`
}
type cell struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
	Errors int64  `json:"errors"`
	Rows   int    `json:"rows"`
	Full   int    `json:"full"`
}
type state struct {
	Now              int64             `json:"now"`
	Policy           policy            `json:"policy"`
	Services         []string          `json:"services"`
	Instances        []instance        `json:"instances"`
	Obligations      []obligation      `json:"obligations"`
	Nodes            []node            `json:"nodes"`
	Edges            []edge            `json:"edges"`
	Paths            []pathRec         `json:"paths"`
	Matrix           map[string]any    `json:"matrix"`
	Recent           []json.RawMessage `json:"recent"`
	Events           []json.RawMessage `json:"events"`
	Stats            map[string]string `json:"stats"`
	Invalidated      map[string]int64  `json:"invalidated"`
	Scenarios        []string          `json:"scenarios"`
	LedgerError      string            `json:"ledger_error"`
	ActiveCandidates []string          `json:"active_candidates"`
	ActiveCandidate  string            `json:"active_candidate"`
	Capabilities     map[string]string `json:"capabilities"`
}

func (c *controller) handleState(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx := r.Context()
	st := state{Now: time.Now().UnixMilli(), Policy: c.pol, Matrix: map[string]any{}, Invalidated: map[string]int64{}, Services: []string{}, Instances: []instance{}, Obligations: []obligation{}, Nodes: []node{}, Edges: []edge{}, Paths: []pathRec{}, Recent: []json.RawMessage{}, Events: []json.RawMessage{}, Scenarios: []string{}, LedgerError: c.ledgerErr,
		Capabilities: map[string]string{"coverage": "exact full paths in a pinned rollout plan", "correctness": "todo-response-v1 request-local response assertions", "confidence": "95% one-sided binomial bound; assumes independent representative requests", "performance": "mean latency compared with fresh matching baseline paths", "scope": "parallel independent canaries, one candidate per request; read-only shadow traffic", "qualification": "declared isolated fixture checks per immutable release; continuous live admission and evidence-based rollback", "not_proven": "production durability, authorization, general side-effect isolation, fan-out, async workflows, distributed controller coordination"}}
	st.Services = c.serviceOrder(ctx)
	if st.Services == nil {
		st.Services = []string{}
	}
	st.Instances = c.instances(ctx)
	for i := range st.Instances {
		in := &st.Instances[i]
		in.ShadowStatus, in.ShadowCount = c.shadowStatus(ctx, *in)
		if in.Role == "baseline" {
			in.Exposure = "stable"
		}
		if in.Role == "draining" || in.Role == "suspended" || in.Role == "retired" {
			in.Exposure = in.Role
		}
		if in.IsolationStatus != "passed" {
			in.Exposure = "blocked"
		}
	}
	if st.Instances == nil {
		st.Instances = []instance{}
	}
	st.Stats, _ = c.rdb.HGetAll(ctx, "stats").Result()
	st.Scenarios, _ = c.rdb.SMembers(ctx, "scenarios").Result()
	sort.Strings(st.Scenarios)
	bases := map[string]instance{}
	nodeSet := map[string]node{}
	candStatus := map[string]string{}
	for _, in := range st.Instances {
		if in.Role == "baseline" {
			bases[in.Service] = in
		}
		n := node{ID: in.NodeKey, Label: ecg.Hop(in.Service, in.Version), Service: in.Service, Version: in.Version, Role: in.Role, NodeKey: in.NodeKey, Alive: in.Role != "retired"}
		old, ok := nodeSet[in.NodeKey]
		if !ok || !old.Alive || in.Role == "baseline" {
			nodeSet[in.NodeKey] = n
		}
	}
	for _, in := range st.Instances {
		if in.Role == "candidate" {
			if b, ok := bases[in.Service]; ok {
				ob := c.obligationsFor(ctx, in, b)
				st.Obligations = append(st.Obligations, ob)
				candStatus[in.NodeKey] = ob.Status
				if in.Exposure == "canary" && in.IsolationStatus == "passed" && ob.Status != "failed" && ob.Status != "unknown" {
					st.ActiveCandidates = append(st.ActiveCandidates, in.ID)
				}
			}
		}
	}
	if len(st.ActiveCandidates) > 0 {
		st.ActiveCandidate = st.ActiveCandidates[0]
	}
	iter := c.rdb.Scan(ctx, 0, "inv:*", 100).Iterator()
	for iter.Next(ctx) {
		st.Invalidated[strings.TrimPrefix(iter.Val(), "inv:")], _ = c.rdb.Get(ctx, iter.Val()).Int64()
	}
	edgeAgg := map[string]*edge{}
	keys, _ := c.rdb.SMembers(ctx, "paths").Result()
	for _, pk := range keys {
		m, err := c.rdb.HGetAll(ctx, "path:"+pk).Result()
		if err != nil || len(m) == 0 {
			continue
		}
		nks, labels, services := strings.Split(m["node_keys"], ","), strings.Split(m["labels"], ","), strings.Split(m["services"], ",")
		if len(nks) != len(labels) || len(nks) != len(services) {
			continue
		}
		rr := c.loadRow(ctx, "path:"+pk)
		a := c.assess(rr, nil)
		p := pathRec{Label: m["label"], Scenario: m["scenario"], Flags: m["flags"], Count: rr.Count, Total: rr.Total, Errors: rr.Errors, AvgMS: a.AvgMS, LastMS: rr.LastMS, Status: a.Status, Hops: nks, Evidence: a}
		for i, nk := range nks {
			n, ok := nodeSet[nk]
			if !ok {
				parts := strings.SplitN(labels[i], "@", 2)
				ver := "?"
				if len(parts) == 2 {
					ver = parts[1]
				}
				n = node{ID: nk, Label: labels[i], Service: services[i], Version: ver, Role: "gone", NodeKey: nk}
				nodeSet[nk] = n
			}
			if !n.Alive {
				p.Status = "historical"
			}
			if i > 0 {
				k := nks[i-1] + ">" + nk
				e, ok := edgeAgg[k]
				if !ok {
					e = &edge{From: nks[i-1], To: nk, Status: "covered"}
					edgeAgg[k] = e
				}
				e.Count += rr.Count
				e.Errors += rr.Errors
				e.Rows++
				if !n.Alive || !nodeSet[nks[i-1]].Alive {
					e.Status = "historical"
				} else if rr.Errors > 0 {
					e.Status = "failed"
				} else if rr.Count < int64(c.pol.Threshold) && e.Status == "covered" {
					e.Status = "partial"
				}
			}
		}
		st.Paths = append(st.Paths, p)
	}
	for _, n := range nodeSet {
		st.Nodes = append(st.Nodes, n)
	}
	sort.Slice(st.Nodes, func(i, j int) bool { return st.Nodes[i].Label+st.Nodes[i].ID < st.Nodes[j].Label+st.Nodes[j].ID })
	for _, e := range edgeAgg {
		st.Edges = append(st.Edges, *e)
	}
	topo, _ := c.rdb.HGetAll(ctx, "topology").Result()
	for _, a := range st.Nodes {
		for _, b := range st.Nodes {
			if a.Alive && b.Alive && topo[a.Service] == b.Service && edgeAgg[a.ID+">"+b.ID] == nil {
				st.Edges = append(st.Edges, edge{From: a.ID, To: b.ID, Status: "unobserved"})
			}
		}
	}
	sort.Slice(st.Edges, func(i, j int) bool { return st.Edges[i].From+st.Edges[i].To < st.Edges[j].From+st.Edges[j].To })
	sort.Slice(st.Paths, func(i, j int) bool {
		return st.Paths[i].Label+st.Paths[i].Scenario+st.Paths[i].Flags < st.Paths[j].Label+st.Paths[j].Scenario+st.Paths[j].Flags
	})
	// This is explicitly a prefix coverage diagnostic, never a promotion verdict.
	if len(st.Services) >= 2 {
		sa, sb := st.Services[0], st.Services[1]
		rows, cols := []string{}, []string{}
		labels := map[string]string{}
		for _, n := range st.Nodes {
			labels[n.ID] = n.Label + " · " + n.ID[:8]
			if n.Service == sa {
				rows = append(rows, n.ID)
			}
			if n.Service == sb {
				cols = append(cols, n.ID)
			}
		}
		cells := map[string]cell{}
		for _, a := range rows {
			for _, b := range cols {
				cl := cell{Status: "unobserved"}
				prefix := ecg.Chain(ecg.Chain("", a), b)
				sf, _ := c.rdb.SMembers(ctx, "rows:"+prefix).Result()
				for _, key := range sf {
					rr := c.getRow(ctx, prefix, key)
					cl.Count += rr.Count
					cl.Errors += rr.Errors
					cl.Rows++
					if rr.Count >= int64(c.pol.Threshold) {
						cl.Full++
					}
				}
				switch {
				case !nodeSet[a].Alive || !nodeSet[b].Alive:
					cl.Status = "historical"
				case cl.Errors > 0:
					cl.Status = "failed"
				case cl.Count == 0:
					cl.Status = "unobserved"
				case cl.Full == len(st.Scenarios) && cl.Full == cl.Rows:
					cl.Status = "covered"
				default:
					cl.Status = "partial"
				}
				cells[a+"|"+b] = cl
			}
		}
		st.Matrix = map[string]any{"row_service": sa, "col_service": sb, "rows": rows, "cols": cols, "labels": labels, "cells": cells, "expected_rows": len(st.Scenarios)}
	}
	rec, _ := c.rdb.LRange(ctx, "recent", 0, 30).Result()
	for _, s := range rec {
		st.Recent = append(st.Recent, json.RawMessage(s))
	}
	events, _ := c.rdb.LRange(ctx, "events", 0, 40).Result()
	for _, s := range events {
		st.Events = append(st.Events, json.RawMessage(s))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(st)
}
func (c *controller) serviceOrder(ctx context.Context) []string {
	topo, _ := c.rdb.HGetAll(ctx, "topology").Result()
	all, _ := c.rdb.SMembers(ctx, "services").Result()
	isTarget := map[string]bool{}
	for _, t := range topo {
		isTarget[t] = true
	}
	var roots []string
	for _, s := range all {
		if !isTarget[s] {
			roots = append(roots, s)
		}
	}
	sort.Strings(roots)
	var out []string
	seen := map[string]bool{}
	for _, r := range roots {
		for s := r; s != "" && !seen[s]; s = topo[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range all {
		if !seen[s] {
			out = append(out, s)
		}
	}
	return out
}
