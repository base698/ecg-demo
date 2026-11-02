package main

import (
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/internal/democontract"
	"github.com/redis/go-redis/v9"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type span struct {
	TraceID  string            `json:"t"`
	SpanID   string            `json:"s"`
	ParentID string            `json:"p"`
	Name     string            `json:"n"`
	Kind     int               `json:"k"`
	Error    bool              `json:"e"`
	StartNs  uint64            `json:"a"`
	EndNs    uint64            `json:"z"`
	Attrs    map[string]string `json:"x"`
}

var bufferSpan = redis.NewScript(`
if redis.call('EXISTS', KEYS[3]) == 1 then return 0 end
local added=redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('EXPIRE', KEYS[1], 120)
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[4])
return added
`)
var takeTrace = redis.NewScript(`
local score=redis.call('ZSCORE', KEYS[2], ARGV[1])
if not score or tonumber(score)>tonumber(ARGV[2]) then return {} end
local spans=redis.call('HVALS', KEYS[1])
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('SET', KEYS[3], '1', 'EX', 86400)
return spans
`)

func (c *controller) handleOTLP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	var body io.Reader = http.MaxBytesReader(w, r.Body, 8<<20)
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, "invalid compression", 400)
			return
		}
		defer gz.Close()
		body = gz
	}
	raw, err := io.ReadAll(io.LimitReader(body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		http.Error(w, "trace payload too large or unreadable", 413)
		return
	}
	var req coltracepb.ExportTraceServiceRequest
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		err = protojson.Unmarshal(raw, &req)
	} else {
		err = proto.Unmarshal(raw, &req)
	}
	if err != nil {
		http.Error(w, "invalid OTLP", 400)
		return
	}
	ctx := r.Context()
	now := time.Now().UnixMilli()
	n := int64(0)
	for _, rs := range req.ResourceSpans {
		res := attrsToMap(rs.Resource.GetAttributes())
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				if len(sp.TraceId) != 16 || len(sp.SpanId) != 8 {
					http.Error(w, "invalid span identity", 400)
					return
				}
				s := span{TraceID: hex.EncodeToString(sp.TraceId), SpanID: hex.EncodeToString(sp.SpanId), ParentID: hex.EncodeToString(sp.ParentSpanId), Name: sp.Name, Kind: int(sp.Kind), Error: sp.Status.GetCode() == tracepb.Status_STATUS_CODE_ERROR, StartNs: sp.StartTimeUnixNano, EndNs: sp.EndTimeUnixNano, Attrs: attrsToMap(sp.Attributes)}
				s.Attrs["service.name"] = res["service.name"]
				b, _ := json.Marshal(s)
				added, err := bufferSpan.Run(ctx, c.rdb, []string{"pend:" + s.TraceID, "pending", "final:" + s.TraceID}, s.SpanID, string(b), now, s.TraceID).Int64()
				if err != nil {
					http.Error(w, "trace store unavailable", 503)
					return
				}
				n += added
			}
		}
	}
	c.rdb.HIncrBy(ctx, "stats", "spans", n)
	out, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Write(out)
}
func attrsToMap(kvs []*commonpb.KeyValue) map[string]string {
	m := map[string]string{}
	for _, kv := range kvs {
		switch v := kv.Value.GetValue().(type) {
		case *commonpb.AnyValue_StringValue:
			m[kv.Key] = v.StringValue
		case *commonpb.AnyValue_BoolValue:
			m[kv.Key] = strconv.FormatBool(v.BoolValue)
		case *commonpb.AnyValue_IntValue:
			m[kv.Key] = strconv.FormatInt(v.IntValue, 10)
		case *commonpb.AnyValue_DoubleValue:
			m[kv.Key] = strconv.FormatFloat(v.DoubleValue, 'f', -1, 64)
		}
	}
	return m
}

const quietPeriod = 2500 * time.Millisecond

func (c *controller) finalizerLoop() {
	ctx := context.Background()
	for range time.Tick(time.Second) {
		cutoff := time.Now().Add(-quietPeriod).UnixMilli()
		ids, err := c.rdb.ZRangeByScore(ctx, "pending", &redisZRange{Min: "-inf", Max: strconv.FormatInt(cutoff, 10)}).Result()
		if err != nil {
			continue
		}
		for _, id := range ids {
			raws, err := takeTrace.Run(ctx, c.rdb, []string{"pend:" + id, "pending", "final:" + id}, id, cutoff).StringSlice()
			if err != nil {
				continue
			}
			spans := []span{}
			for _, raw := range raws {
				var s span
				if json.Unmarshal([]byte(raw), &s) == nil {
					spans = append(spans, s)
				}
			}
			if len(spans) > 0 {
				c.assemble(ctx, spans)
			}
		}
	}
}

type hopRec struct {
	Service, Version, Instance, NodeKey, PrefixIn, PrefixOut, Intended string
	Failed                                                             bool
	LatencyMS                                                          float64
	Depth                                                              int
	StartNs                                                            uint64
}
type recentTrace struct {
	TS        int64   `json:"ts"`
	Path      string  `json:"path"`
	Scenario  string  `json:"scenario"`
	Flags     string  `json:"flags"`
	Tenant    string  `json:"tenant"`
	Source    string  `json:"source"`
	Eligible  bool    `json:"eligible"`
	Failed    bool    `json:"failed"`
	Outcome   string  `json:"outcome"`
	LatMS     float64 `json:"lat_ms"`
	Credited  bool    `json:"credited"`
	Note      string  `json:"note,omitempty"`
	TraceID   string  `json:"trace_id"`
	Assertion string  `json:"assertion"`
}

// The demo supports a linear synchronous chain with retries. A branch, a
// disconnected graph, or mismatched identities is inconclusive, never proof.
func (c *controller) assemble(ctx context.Context, spans []span) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(spans) == 0 {
		return
	}
	id := spans[0].TraceID
	if id == "" {
		return
	}
	claimed, err := c.rdb.SetNX(ctx, "processed:"+id, 1, 24*time.Hour).Result()
	if err != nil {
		c.ledgerErr = "trace deduplication unavailable"
		return
	}
	if !claimed {
		c.rdb.HIncrBy(ctx, "stats", "duplicates", 1)
		return
	}
	byID := map[string]span{}
	for _, s := range spans {
		byID[s.SpanID] = s
	}
	spans = spans[:0]
	for _, s := range byID {
		spans = append(spans, s)
	}
	gaps := []string{}
	mismatch := ""
	children := map[string][]span{}
	for _, s := range spans {
		if s.SpanID == "" || s.TraceID != id || s.EndNs < s.StartNs {
			gaps = append(gaps, "invalid span identity or time")
		}
		if s.ParentID != "" {
			children[s.ParentID] = append(children[s.ParentID], s)
			if _, ok := byID[s.ParentID]; !ok {
				gaps = append(gaps, "missing parent span")
			}
		}
	}
	depth := func(s span) int {
		seen := map[string]bool{s.SpanID: true}
		d := 0
		for p, ok := byID[s.ParentID]; ok; p, ok = byID[p.ParentID] {
			if seen[p.SpanID] {
				gaps = append(gaps, "cyclic span ancestry")
				break
			}
			seen[p.SpanID] = true
			d++
		}
		return d
	}
	var root *span
	hops := []hopRec{}
	source := "unknown"
	affected := map[string]bool{}
	anyError := false
	maxEnd := uint64(0)
	for i, s := range spans {
		if s.EndNs > maxEnd {
			maxEnd = s.EndNs
		}
		if s.Error {
			anyError = true
		}
		if src := s.Attrs["ecg.source"]; src != "" {
			source = src
		}
		if s.Kind == 3 && s.Attrs[ecg.AttrTarget] != "" {
			serverChild := false
			for _, ch := range children[s.SpanID] {
				if ch.Kind == 2 {
					serverChild = true
				}
			}
			if !serverChild {
				gaps = append(gaps, "outbound request has no server span")
			}
		}
		if intended := s.Attrs[ecg.AttrIntended]; intended != "" {
			nk, e := c.rdb.HGet(ctx, "identity:"+intended, "node_key").Result()
			if e == nil {
				affected[nk] = true
			} else if e != redis.Nil {
				c.ledgerErr = "identity store unavailable"
				return
			}
		}
		nk := s.Attrs[ecg.AttrNodeKey]
		if nk == "" || s.Kind != 2 {
			continue
		}
		h := hopRec{Service: s.Attrs[ecg.AttrService], Version: s.Attrs[ecg.AttrVersion], Instance: s.Attrs[ecg.AttrInstance], NodeKey: nk, PrefixIn: s.Attrs[ecg.AttrPrefixIn], PrefixOut: s.Attrs[ecg.AttrPrefixOut], Intended: s.Attrs[ecg.AttrIntended], Depth: depth(s), StartNs: s.StartNs}
		if s.EndNs >= s.StartNs {
			h.LatencyMS = float64(s.EndNs-s.StartNs) / 1e6
		}
		status, _ := strconv.Atoi(s.Attrs["http.response.status_code"])
		h.Failed = s.Error || s.Attrs[ecg.AttrFailed] == "true" || status >= 500
		identity, e := c.rdb.HGetAll(ctx, "identity:"+h.Instance).Result()
		if e != nil {
			c.ledgerErr = "identity store unavailable"
			return
		}
		if identity["node_key"] != nk || identity["service"] != h.Service || identity["version"] != h.Version {
			mismatch = "unregistered or mismatched execution identity"
		} else {
			affected[nk] = true
		}
		if h.Intended == "" || h.Intended != h.Instance {
			mismatch = "intended and observed instances differ"
		}
		if ecg.Chain(h.PrefixIn, h.NodeKey) != h.PrefixOut {
			mismatch = "invalid path fingerprint"
		}
		// Find the nearest ECG server ancestor, allowing intervening client spans.
		parentPrefix := ""
		seen := map[string]bool{}
		for p, ok := byID[s.ParentID]; ok; p, ok = byID[p.ParentID] {
			if seen[p.SpanID] {
				break
			}
			seen[p.SpanID] = true
			if p.Kind == 2 && p.Attrs[ecg.AttrNodeKey] != "" {
				parentPrefix = p.Attrs[ecg.AttrPrefixOut]
				break
			}
		}
		if parentPrefix != h.PrefixIn {
			gaps = append(gaps, "path prefix does not match ancestry")
		}
		if h.PrefixIn == "" {
			if root != nil {
				gaps = append(gaps, "multiple execution roots")
			}
			root = &spans[i]
		}
		hops = append(hops, h)
	}
	if len(affected) == 0 && len(hops) == 0 {
		return
	}
	sort.Slice(hops, func(i, j int) bool {
		if hops[i].Depth != hops[j].Depth {
			return hops[i].Depth < hops[j].Depth
		}
		return hops[i].StartNs < hops[j].StartNs
	})
	uniq := []hopRec{}
	seen := map[string]int{}
	for _, h := range hops {
		if i, ok := seen[h.PrefixOut]; ok {
			uniq[i] = h
		} else {
			seen[h.PrefixOut] = len(uniq)
			uniq = append(uniq, h)
		}
	}
	hops = uniq
	prior := ""
	for _, h := range hops {
		if h.PrefixIn != prior {
			gaps = append(gaps, "branching or disconnected execution is unsupported")
		}
		prior = h.PrefixOut
	}
	scenario, flags, tenant, outcome, assertion := "unknown", "unknown", "", "unknown", "not_checked"
	eligible, failed := false, anyError
	if root == nil {
		gaps = append(gaps, "missing execution root")
	} else {
		scenario = root.Attrs[ecg.AttrScenario]
		flags = root.Attrs[ecg.AttrFlags]
		tenant = root.Attrs[ecg.AttrTenant]
		outcome = root.Attrs[ecg.AttrOutcome]
		eligible = root.Attrs[ecg.AttrEligible] == "true"
		failed = root.Error || root.Attrs[ecg.AttrFailed] == "true"
		status, _ := strconv.Atoi(root.Attrs["http.response.status_code"])
		failed = failed || status >= 500
		if root.Attrs[ecg.AttrOracle] == democontract.Oracle {
			assertion = root.Attrs[ecg.AttrAssertion]
		}
		for _, s := range spans {
			if s.Kind == 2 && s.Attrs[ecg.AttrNodeKey] != "" && (s.Attrs[ecg.AttrScenario] != scenario || s.Attrs[ecg.AttrFlags] != flags) {
				gaps = append(gaps, "scenario or flags changed within a trace")
			}
		}
	}
	checked := assertion == "passed" || assertion == "failed"
	assertionFailed := assertion == "failed"
	failed = failed || assertionFailed
	ms := int64(maxEnd / 1e6)
	if ms <= 0 || ms > time.Now().Add(time.Second).UnixMilli() {
		gaps = append(gaps, "invalid observation timestamp")
		ms = time.Now().UnixMilli()
	}
	labels := []string{}
	lat := float64(0)
	for _, h := range hops {
		labels = append(labels, ecg.Hop(h.Service, h.Version))
	}
	if len(hops) > 0 {
		lat = hops[0].LatencyMS
	}
	incomplete := len(gaps) > 0 || mismatch != ""
	info := sample{ID: id, MS: ms, Failed: failed, Checked: checked, AssertionFailed: assertionFailed, Incomplete: incomplete, LatencyMS: lat, Source: source}
	c.exportLibraryObservation(ctx, hops, scenario, flags, assertion, info)
	rec := recentTrace{TS: ms, Path: ecg.JoinPath(labels), Scenario: scenario, Flags: flags, Tenant: tenant, Source: source, Eligible: eligible, Failed: failed, Outcome: outcome, LatMS: lat, TraceID: id, Assertion: assertion}
	pipe := c.rdb.TxPipeline()
	pipe.HIncrBy(ctx, "stats", "traces", 1)
	if incomplete {
		pipe.HIncrBy(ctx, "stats", "gaps", 1)
		rec.Note = strings.Join(gaps, "; ")
		if mismatch != "" {
			pipe.HIncrBy(ctx, "stats", "mismatches", 1)
			rec.Note += "; " + mismatch
		}
		rec.Note += "; no coverage credit; health signal retained"
	} else {
		rec.Credited = true
		c.credit(pipe, ctx, hops, labels, scenario, flags, info)
	}
	// All observed participants and intended targets retain negative/uncertain
	// evidence, including candidates that failed before producing a server span.
	for nk := range affected {
		key := "signal:" + nk + ":" + scenario + "|" + flags
		c.writeSample(pipe, ctx, key, info)
		pipe.SAdd(ctx, "signals:"+nk, key)
	}
	b, _ := json.Marshal(rec)
	pipe.LPush(ctx, "recent", b)
	pipe.LTrim(ctx, "recent", 0, 59)
	if _, err := pipe.Exec(ctx); err != nil {
		c.ledgerErr = "evidence write failed; reset evidence after storage recovery"
	}
}

func (c *controller) writeSample(p redis.Pipeliner, ctx context.Context, key string, s sample) {
	b, _ := json.Marshal(s)
	p.HIncrBy(ctx, key, "count", 1)
	if s.Failed {
		p.HIncrBy(ctx, key, "errors", 1)
	}
	p.Eval(ctx, `local old=tonumber(redis.call("HGET",KEYS[1],"last_ms") or "0"); if tonumber(ARGV[1])>old then redis.call("HSET",KEYS[1],"last_ms",ARGV[1]) end; return 1`, []string{key}, s.MS)
	p.ZAdd(ctx, "samples:"+key, redis.Z{Score: float64(s.MS), Member: string(b)})
	cutoff := time.Now().Add(-time.Duration(c.pol.EvidenceTTL) * time.Second).UnixMilli()
	p.ZRemRangeByScore(ctx, "samples:"+key, "-inf", "("+strconv.FormatInt(cutoff, 10))
	p.Expire(ctx, "samples:"+key, time.Duration(c.pol.EvidenceTTL+60)*time.Second)
}

func (c *controller) credit(p redis.Pipeliner, ctx context.Context, hops []hopRec, labels []string, scenario, flags string, info sample) {
	if len(hops) == 0 {
		return
	}
	sf := scenario + "|" + flags
	p.SAdd(ctx, "scenarios", sf)
	nks, services := []string{}, []string{}
	for i, h := range hops {
		r := info
		r.LatencyMS = h.LatencyMS
		c.writeSample(p, ctx, "hoprow:"+h.PrefixOut+":"+sf, r)
		p.HSet(ctx, "hopmeta:"+h.PrefixOut, map[string]any{"prefix_in": h.PrefixIn, "node_key": h.NodeKey, "service": h.Service, "version": h.Version, "instance": h.Instance, "label": ecg.JoinPath(labels[:i+1]), "label_in": ecg.JoinPath(labels[:i])})
		p.SAdd(ctx, "hops:"+h.NodeKey, h.PrefixOut)
		p.SAdd(ctx, "rows:"+h.PrefixOut, sf)
		nks = append(nks, h.NodeKey)
		services = append(services, h.Service)
	}
	pk := hops[len(hops)-1].PrefixOut + "|" + sf
	c.writeSample(p, ctx, "path:"+pk, info)
	p.HSet(ctx, "path:"+pk, map[string]any{"label": ecg.JoinPath(labels), "scenario": scenario, "flags": flags, "node_keys": strings.Join(nks, ","), "labels": strings.Join(labels, ","), "services": strings.Join(services, ",")})
	p.SAdd(ctx, "paths", pk)
}

func (c *controller) labelsForNode(ctx context.Context, nodeKey string) []string {
	out := []string{}
	prefixes, _ := c.rdb.SMembers(ctx, "hops:"+nodeKey).Result()
	for _, p := range prefixes {
		if l, _ := c.rdb.HGet(ctx, "hopmeta:"+p, "label").Result(); l != "" {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}
func init() { log.SetFlags(log.Ltime) }
