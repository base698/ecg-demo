package main

import (
	"context"
	"github.com/base698/ecg-demo/evidence"
	"encoding/json"
	"strconv"
	"time"
)

type row = evidence.Window
type sample = evidence.Sample
type assessment = evidence.Assessment

func (c *controller) loadRow(ctx context.Context, key string) row {
	m, err := c.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return row{Unavailable: true}
	}
	r := row{}
	r.Total, _ = strconv.ParseInt(m["count"], 10, 64)
	r.LastMS, _ = strconv.ParseInt(m["last_ms"], 10, 64)
	cutoff := time.Now().Add(-time.Duration(c.pol.EvidenceTTL) * time.Second).UnixMilli()
	values, err := c.rdb.ZRangeByScore(ctx, "samples:"+key, &redisZRange{Min: strconv.FormatInt(cutoff, 10), Max: "+inf"}).Result()
	if err != nil {
		r.Unavailable = true
		return r
	}
	for _, v := range values {
		var s sample
		if json.Unmarshal([]byte(v), &s) != nil {
			r.Unavailable = true
			continue
		}
		r.Count++
		if s.Failed {
			r.Errors++
		}
		if s.Checked {
			r.Checked++
		}
		if s.AssertionFailed {
			r.AssertionFailures++
		}
		if s.Incomplete {
			r.Incomplete++
		}
		r.LatSum += s.LatencyMS
	}
	return r
}

func (c *controller) getRow(ctx context.Context, prefix, sf string) row {
	return c.loadRow(ctx, "hoprow:"+prefix+":"+sf)
}

func (c *controller) assess(r row, b *row) assessment { return c.pol.Assess(r, b) }
func (c *controller) rowStatus(r row) string          { return c.assess(r, nil).Status }
func errorUpper95(n, k int64) float64                 { return evidence.ErrorUpper95(n, k) }
