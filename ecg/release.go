package ecg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Release identifies code, its contract and the effective non-secret
// configuration. Artifact and Config are producer-supplied digests; ECG does
// not independently attest them. Instance identity is deliberately separate.
type Release struct {
	Service  string `json:"service"`
	Version  string `json:"version"`
	Artifact string `json:"artifact"`
	Contract string `json:"contract"`
	Config   string `json:"config"`
}

func (r Release) Validate() error {
	for name, value := range map[string]string{"service": r.Service, "version": r.Version, "artifact": r.Artifact, "contract": r.Contract, "config": r.Config} {
		if value == "" || len(value) > 256 {
			return fmt.Errorf("release %s must contain 1–256 bytes", name)
		}
	}
	return nil
}

// Key includes the actual artifact even when a human version label is reused.
func (r Release) Key() string {
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

// Digest produces a full SHA-256 digest, useful for allowlisted configuration.
// Never fingerprint an entire environment containing credentials.
func Digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
