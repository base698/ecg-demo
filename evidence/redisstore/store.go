// Package redisstore stores observations atomically in a namespaced Redis.
// It never flushes the database or owns the caller's Redis client lifecycle.
package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/base698/ecg-demo/ecg"
	"github.com/base698/ecg-demo/evidence"
	"github.com/redis/go-redis/v9"
)

type Options struct {
	Namespace                                       string
	Retention                                       time.Duration
	MaxSeries, MaxSamplesPerSeries, MaxObservations int
}
type Store struct {
	client  *redis.Client
	prefix  string
	options Options
}

func New(client *redis.Client, o Options) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("Redis client is required")
	}
	if o.Namespace == "" {
		o.Namespace = "default"
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(o.Namespace) {
		return nil, fmt.Errorf("invalid namespace")
	}
	if o.Retention == 0 {
		o.Retention = 24 * time.Hour
	}
	if o.Retention < time.Second {
		return nil, fmt.Errorf("retention must be at least one second")
	}
	if o.MaxSeries <= 0 {
		o.MaxSeries = 1000
	}
	if o.MaxSamplesPerSeries <= 0 {
		o.MaxSamplesPerSeries = 10000
	}
	if o.MaxObservations <= 0 {
		o.MaxObservations = 100000
	}
	return &Store{client: client, prefix: "ecg:{" + o.Namespace + "}:", options: o}, nil
}

var appendScript = redis.NewScript(`
local old=redis.call('GET',KEYS[1])
if old then if old==ARGV[1] then return 0 else return -1 end end
if redis.call('SISMEMBER',KEYS[2],ARGV[2])==0 and redis.call('SCARD',KEYS[2])>=tonumber(ARGV[7]) then return -2 end
redis.call('ZREMRANGEBYSCORE',KEYS[4],'-inf','('..ARGV[6])
if redis.call('ZCARD',KEYS[4])>=tonumber(ARGV[8]) then return -2 end
redis.call('ZREMRANGEBYSCORE',KEYS[5],'-inf',ARGV[10])
if redis.call('ZCARD',KEYS[5])>=tonumber(ARGV[11]) then return -2 end
redis.call('SET',KEYS[1],ARGV[1],'EX',ARGV[5])
redis.call('ZADD',KEYS[5],ARGV[12],KEYS[1])
redis.call('EXPIRE',KEYS[5],ARGV[5])
redis.call('SADD',KEYS[2],ARGV[2])
redis.call('HSET',KEYS[3],'series',ARGV[3])
redis.call('HINCRBY',KEYS[3],'total',1)
local last=tonumber(redis.call('HGET',KEYS[3],'last') or '0')
if tonumber(ARGV[4])>last then redis.call('HSET',KEYS[3],'last',ARGV[4]) end
if tonumber(ARGV[4])>=tonumber(ARGV[6]) then redis.call('ZADD',KEYS[4],ARGV[4],ARGV[9]) end
redis.call('EXPIRE',KEYS[4],ARGV[5])
return 1
`)

func (s *Store) Append(ctx context.Context, o evidence.Observation) (bool, error) {
	if err := o.Validate(); err != nil {
		return false, err
	}
	o = o.Canonical()
	payload, _ := json.Marshal(o)
	definition, _ := json.Marshal(o.Series)
	sample, _ := json.Marshal(o.Sample())
	key := o.Series.Key()
	result, err := appendScript.Run(ctx, s.client, []string{s.prefix + "seen:" + ecg.Digest([]byte(o.ID)), s.prefix + "series", s.prefix + "row:" + key, s.prefix + "samples:" + key, s.prefix + "seen-index"}, ecg.Digest(payload), key, string(definition), o.CompletedAt.UnixMilli(), int64(s.options.Retention/time.Second), time.Now().Add(-s.options.Retention).UnixMilli(), s.options.MaxSeries, s.options.MaxSamplesPerSeries, string(sample), time.Now().UnixMilli(), s.options.MaxObservations, time.Now().Add(s.options.Retention).UnixMilli()).Int()
	if err != nil {
		return false, err
	}
	if result == -1 {
		return false, evidence.ErrConflict
	}
	if result == -2 {
		return false, evidence.ErrCapacity
	}
	return result == 1, nil
}

var snapshotScript = redis.NewScript(`
local out={redis.call('HGET',KEYS[1],'total') or '0',redis.call('HGET',KEYS[1],'last') or '0'}
local samples=redis.call('ZRANGEBYSCORE',KEYS[2],ARGV[1],ARGV[2])
for _,v in ipairs(samples) do table.insert(out,v) end
return out
`)

func (s *Store) Snapshot(ctx context.Context, d evidence.Series, since, until time.Time) (evidence.Snapshot, error) {
	out := evidence.Snapshot{Samples: []evidence.Sample{}}
	if until.Sub(since) > s.options.Retention {
		return out, fmt.Errorf("requested window exceeds store retention")
	}
	key := d.Key()
	values, err := snapshotScript.Run(ctx, s.client, []string{s.prefix + "row:" + key, s.prefix + "samples:" + key}, since.UnixMilli(), until.UnixMilli()).StringSlice()
	if err != nil {
		return out, err
	}
	if len(values) < 2 {
		return out, fmt.Errorf("invalid stored snapshot")
	}
	out.Total, err = strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return out, err
	}
	out.LastMS, err = strconv.ParseInt(values[1], 10, 64)
	if err != nil {
		return out, err
	}
	for _, raw := range values[2:] {
		var sample evidence.Sample
		if err := json.Unmarshal([]byte(raw), &sample); err != nil {
			return out, err
		}
		out.Samples = append(out.Samples, sample)
	}
	return out, nil
}

func (s *Store) Series(ctx context.Context) ([]evidence.Series, error) {
	keys, err := s.client.SMembers(ctx, s.prefix+"series").Result()
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	out := []evidence.Series{}
	for _, k := range keys {
		raw, err := s.client.HGet(ctx, s.prefix+"row:"+k, "series").Result()
		if err != nil {
			return nil, err
		}
		var d evidence.Series
		if err = json.Unmarshal([]byte(raw), &d); err != nil {
			return nil, err
		}
		if err = d.Validate(); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}
