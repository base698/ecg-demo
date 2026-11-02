// Package ecg defines release identities and the ECG trace vocabulary.
package ecg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// Baggage members propagated with each request (W3C baggage header).
const (
	BagPlan     = "ecg.plan"     // pinned instances for one execution
	BagPrefix   = "ecg.prefix"   // running path hash (Merkle chain of node keys)
	BagScenario = "ecg.scenario" // application-defined scenario
	BagFlags    = "ecg.flags"    // per-request feature-flag evaluations
	BagEligible = "ecg.eligible" // "1" when the request may hit an unverified candidate
	BagTenant   = "ecg.tenant"
)

// Span attributes written by every instrumented hop.
const (
	AttrService         = "ecg.service"
	AttrVersion         = "ecg.version"
	AttrInstance        = "ecg.instance"
	AttrNodeKey         = "ecg.node_key"
	AttrPrefixIn        = "ecg.prefix_in"
	AttrPrefixOut       = "ecg.prefix_out"
	AttrScenario        = "ecg.scenario"
	AttrFlags           = "ecg.flags"
	AttrEligible        = "ecg.eligible"
	AttrTenant          = "ecg.tenant"
	AttrIntended        = "ecg.intended" // instance the router chose for this hop
	AttrTarget          = "ecg.target"   // on client spans: downstream service name
	AttrOutcome         = "ecg.outcome"  // business outcome: ok | declined | retried
	AttrRetries         = "ecg.retries"
	AttrFailed          = "ecg.failed"
	AttrOracle          = "ecg.oracle"
	AttrAssertion       = "ecg.assertion"
	AttrAssertionReason = "ecg.assertion_reason"
)

// HTTP headers used between caller and callee (in addition to W3C trace headers).
const (
	HdrIntended = "X-Ecg-Intended"
	HdrAttempt  = "X-Ecg-Attempt"
)

// NodeKey is the static identity of a running instance's code+contract+config.
// Flag evaluations are deliberately NOT part of it; they vary per request and
// are folded into the attestation key by the ledger instead.
func NodeKey(service, version, contract, config string) string {
	b, _ := json.Marshal([]string{service, version, contract, config})
	return short(sha256.Sum256(b), 32)
}

// Chain extends the path prefix with one more hop: prefix_out = H(prefix_in, node_key).
func Chain(prefixIn, nodeKey string) string {
	b, _ := json.Marshal([]string{prefixIn, nodeKey})
	return short(sha256.Sum256(b), 32)
}

func short(sum [32]byte, n int) string {
	return hex.EncodeToString(sum[:])[:n]
}

// Hop is a human-readable label like "b@2".
func Hop(service, version string) string { return service + "@" + version }

// JoinPath joins hop labels with the arrow used throughout the UI.
func JoinPath(hops []string) string { return strings.Join(hops, "→") }

// ArtifactID binds evidence to the actual service binary, including local builds.
func ArtifactID() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
