package evidence

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/base698/ecg-demo/ecg"
)

const WireVersion = "ecg.observation/v1"

// Definition versions application meaning, not a deployment. Changing a check
// or classifier requires a new version so old evidence cannot silently qualify.
type Definition struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Series is the complete evidence partition. Source (synthetic/production),
// environment (mock/staging/live), and schema are explicit and never defaulted.
// Path is supplied by the application/trace adapter, not inferred by this API.
type Series struct {
	Path        []ecg.Release     `json:"path"`
	Scenario    Definition        `json:"scenario"`
	Checks      []Definition      `json:"checks"`
	Flags       map[string]string `json:"flags"`
	Source      string            `json:"source"`
	Environment string            `json:"environment"`
	Schema      string            `json:"schema"`
}

func validLabel(v string, max int) bool {
	return strings.TrimSpace(v) != "" && len(v) <= max && !strings.ContainsAny(v, "\x00\r\n")
}
func (d Definition) valid() bool { return validLabel(d.Name, 128) && validLabel(d.Version, 64) }

func (s Series) Validate() error {
	if len(s.Path) < 1 || len(s.Path) > 32 || !s.Scenario.valid() || len(s.Checks) > 32 || len(s.Flags) > 16 {
		return fmt.Errorf("invalid path, scenario or dimension limits")
	}
	for _, r := range s.Path {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	for _, v := range []string{s.Source, s.Environment, s.Schema} {
		if !validLabel(v, 128) {
			return fmt.Errorf("source, environment and schema are required bounded labels")
		}
	}
	seen := map[Definition]bool{}
	for _, d := range s.Checks {
		if !d.valid() || seen[d] {
			return fmt.Errorf("invalid or duplicate check definition")
		}
		seen[d] = true
	}
	for k, v := range s.Flags {
		if !validLabel(k, 64) || !validLabel(v, 128) {
			return fmt.Errorf("invalid flag dimension")
		}
	}
	return nil
}

// Canonical returns an owned copy; map insertion/check declaration order does
// not change the series. Call Validate before accepting untrusted input.
func (s Series) Canonical() Series {
	s.Path = append([]ecg.Release(nil), s.Path...)
	s.Checks = append([]Definition{}, s.Checks...)
	sort.Slice(s.Checks, func(i, j int) bool {
		a, b := s.Checks[i], s.Checks[j]
		if a.Name == b.Name {
			return a.Version < b.Version
		}
		return a.Name < b.Name
	})
	flags := map[string]string{}
	for k, v := range s.Flags {
		flags[k] = v
	}
	s.Flags = flags
	return s
}

func (s Series) Key() string { b, _ := json.Marshal(s.Canonical()); return ecg.Digest(b) }

type CheckResult struct {
	Definition
	Status string `json:"status"`           // passed | failed | unknown
	Reason string `json:"reason,omitempty"` // bounded non-sensitive explanation
}

type Observation struct {
	Version     string        `json:"version"`
	ID          string        `json:"id"`
	Series      Series        `json:"series"`
	CompletedAt time.Time     `json:"completed_at"`
	DurationMS  float64       `json:"duration_ms"`
	Failed      bool          `json:"failed"`
	Complete    bool          `json:"complete"`
	Checks      []CheckResult `json:"checks"`
}

func (o Observation) Validate() error {
	if o.Version != WireVersion || !validLabel(o.ID, 128) || o.CompletedAt.IsZero() || o.DurationMS < 0 || math.IsNaN(o.DurationMS) || math.IsInf(o.DurationMS, 0) {
		return fmt.Errorf("invalid observation version, identity, timestamp or duration")
	}
	if err := o.Series.Validate(); err != nil {
		return err
	}
	expected := map[Definition]bool{}
	for _, d := range o.Series.Checks {
		expected[d] = true
	}
	seen := map[Definition]bool{}
	for _, r := range o.Checks {
		if !expected[r.Definition] || seen[r.Definition] || len(r.Reason) > 512 || (r.Status != "passed" && r.Status != "failed" && r.Status != "unknown") {
			return fmt.Errorf("invalid, undeclared or duplicate check result")
		}
		seen[r.Definition] = true
	}
	return nil
}

func (o Observation) Canonical() Observation {
	o.Series = o.Series.Canonical()
	o.CompletedAt = o.CompletedAt.UTC()
	o.Checks = append([]CheckResult{}, o.Checks...)
	sort.Slice(o.Checks, func(i, j int) bool {
		a, b := o.Checks[i], o.Checks[j]
		if a.Name == b.Name {
			return a.Version < b.Version
		}
		return a.Name < b.Name
	})
	return o
}

func (o Observation) Sample() Sample {
	s := Sample{ID: o.ID, MS: o.CompletedAt.UnixMilli(), Failed: o.Failed, Incomplete: !o.Complete, LatencyMS: o.DurationMS, Source: o.Series.Source}
	known := 0
	for _, r := range o.Checks {
		if r.Status == "passed" || r.Status == "failed" {
			known++
		}
		if r.Status == "failed" {
			s.AssertionFailed = true
			s.Failed = true
		}
	}
	s.Checked = len(o.Series.Checks) > 0 && known == len(o.Series.Checks)
	return s
}
