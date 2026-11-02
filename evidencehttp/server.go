// Package evidencehttp exposes the observation/evaluation API. It has no
// deployment permissions and does not proxy or route application traffic.
package evidencehttp

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/base698/ecg-demo/evidence"
)

type ServerOptions struct {
	Token        string
	MaxBodyBytes int64
}
type API struct {
	engine  *evidence.Engine
	options ServerOptions
}

func NewHandler(engine *evidence.Engine, o ServerOptions) http.Handler {
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 1 << 20
	}
	a := &API{engine: engine, options: o}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := engine.Series(r.Context()); err != nil {
			respond(w, 503, map[string]string{"error": "evidence store unavailable"})
			return
		}
		respond(w, 200, map[string]string{"status": "ok"})
	})
	protected := http.NewServeMux()
	protected.HandleFunc("POST /v1/observations", a.observe)
	protected.HandleFunc("POST /v1/evaluate", a.evaluate)
	protected.HandleFunc("GET /v1/series", a.series)
	protected.HandleFunc("GET /v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]any{
			"protocol": evidence.WireVersion, "mode": "observe-only", "deployment_control": false,
			"coverage":          "producer-supplied versioned executions; missing declared scenarios remain missing",
			"correctness":       "versioned application-owned checks, not inferred from HTTP status",
			"source_separation": true, "trace_assembly": "not performed by this API; the routed demo retains its linear OTLP adapter",
			"confidence": "conditional iid binomial diagnostic, not a sequential guarantee",
			"limits":     "no independent release attestation, durable workflow proof, automatic rollback or multi-tenant authorization"})
	})
	mux.Handle("/v1/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.Token != "" {
			header := r.Header.Get("Authorization")
			got := strings.TrimPrefix(header, "Bearer ")
			if !strings.HasPrefix(header, "Bearer ") || subtle.ConstantTimeCompare([]byte(got), []byte(o.Token)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				respond(w, 401, map[string]string{"error": "unauthorized"})
				return
			}
		}
		protected.ServeHTTP(w, r)
	}))
	return mux
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (a *API) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, a.options.MaxBodyBytes))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		respond(w, 400, map[string]string{"error": "invalid or oversized JSON request"})
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		respond(w, 400, map[string]string{"error": "expected one JSON value"})
		return false
	}
	return true
}
func (a *API) observe(w http.ResponseWriter, r *http.Request) {
	var o evidence.Observation
	if !a.decode(w, r, &o) {
		return
	}
	if err := o.Validate(); err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if o.CompletedAt.After(time.Now().Add(time.Second)) {
		respond(w, 400, map[string]string{"error": "completion is in the future"})
		return
	}
	added, err := a.engine.Observe(r.Context(), o)
	if errors.Is(err, evidence.ErrConflict) {
		respond(w, 409, map[string]string{"error": "observation ID conflicts with prior content"})
		return
	}
	if err != nil {
		respond(w, 503, map[string]string{"error": "observation could not be stored"})
		return
	}
	status := http.StatusCreated
	if !added {
		status = http.StatusOK
	}
	respond(w, status, map[string]bool{"accepted": added, "duplicate": !added})
}

type EvaluationRequest struct {
	Requirements []evidence.Requirement `json:"requirements"`
}
type EvaluationResponse struct {
	Reports []evidence.Report `json:"reports"`
}

func (a *API) evaluate(w http.ResponseWriter, r *http.Request) {
	var in EvaluationRequest
	if !a.decode(w, r, &in) {
		return
	}
	if len(in.Requirements) == 0 || len(in.Requirements) > 100 {
		respond(w, 400, map[string]string{"error": "supply 1–100 declared requirements"})
		return
	}
	out := EvaluationResponse{Reports: []evidence.Report{}}
	for _, req := range in.Requirements {
		report, err := a.engine.Evaluate(r.Context(), req)
		if err != nil {
			status := 400
			if report.Window.Unavailable || report.Assessment.Performance == "insufficient_baseline" {
				status = 503
			}
			respond(w, status, map[string]any{"error": "requirement is invalid or evidence unavailable", "report": report})
			return
		}
		out.Reports = append(out.Reports, report)
	}
	respond(w, 200, out)
}
func (a *API) series(w http.ResponseWriter, r *http.Request) {
	ds, err := a.engine.Series(r.Context())
	if err != nil {
		respond(w, 503, map[string]string{"error": "evidence store unavailable"})
		return
	}
	out := EvaluationResponse{Reports: []evidence.Report{}}
	for _, d := range ds {
		report, err := a.engine.Evaluate(r.Context(), evidence.Requirement{Series: d})
		if err != nil {
			respond(w, 503, map[string]string{"error": "evidence store unavailable"})
			return
		}
		out.Reports = append(out.Reports, report)
	}
	respond(w, 200, out)
}
