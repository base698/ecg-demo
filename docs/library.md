# ECG library guide

## Boundaries

These packages form one experimental Go module: `github.com/base698/ecg-demo`.
No stable API release is tagged yet. From another Go application:

```bash
go get github.com/base698/ecg-demo@main
```

For local development, add a replace directive to that application's go.mod:

```go
replace github.com/base698/ecg-demo => /absolute/path/to/ecg-demo
```

Import `github.com/base698/ecg-demo/sdk`, `github.com/base698/ecg-demo/evidence`,
etc. The core `evidence` package has no Redis, HTTP, OpenTelemetry exporter,
global initialization, or demo-controller dependency.
Applications own their tracer provider, exporters, HTTP server and shutdown.
The internal `demotel` package configures globals only for the demo binaries.

## Embedding

```go
store := memory.New(memory.Options{MaxSeries: 100, MaxObservations: 10000})
engine, err := evidence.New(store, evidence.DefaultPolicy())
if err != nil { return err }
recorder := sdk.RecorderFunc(func(ctx context.Context, o evidence.Observation) error {
    _, err := engine.Observe(ctx, o)
    return err
})
client, err := sdk.New(sdk.Options{
    Release: ecg.Release{Service: "orders", Version: "1", Artifact: artifactDigest,
        Contract: "orders/v1", Config: configDigest},
    Tracer: tracer, // optional; otherwise a no-op tracer, not the global provider
    Recorder: recorder,
})
if err != nil { return err }
series := evidence.Series{
    Scenario: evidence.Definition{Name: "order.create", Version: "1"},
    Checks: []evidence.Definition{{Name: "order-response", Version: "1"}},
    Flags: map[string]string{"express": "on"},
    Source: "synthetic", Environment: "staging", Schema: "orders/v1",
}
ctx, op, err := client.Start(ctx, series) // omitted path defaults to this release
if err != nil { return err }
// Call the application, then inspect its business response.
if err := op.Check(evidence.CheckResult{
    Definition: series.Checks[0], Status: "passed", // result of your actual check
}); err != nil { return err }
exportErr := op.End(ctx, sdk.Outcome{Complete: true})
// Handle exportErr as telemetry failure, separately from the business result.
```

The runnable [labels example](../examples/labels/run.go) supplies real check
results and complete error handling. `Operation.End` records at most once;
repeated failed checks stay failed. Declared checks that were omitted or remain
unknown prevent checked-success claims. An execution is one sample, regardless
of its number of checks or trace spans.

For remote delivery, create `evidencehttp.NewClient(endpoint, options)` and wrap
it in `sdk.NewAsync`. Enqueue is bounded/nonblocking; queue overflow returns
`ErrBufferFull`. Record calls validate and copy observations before enqueueing.
Observe `Stats()` for accepted, delivered, failed and dropped counts. Stop
producers, then call `Close` with a deadline to drain. Parent cancellation stops
the worker and drops queued work. The supplied downstream recorder must honor
context cancellation. Use a process-lifetime context, not a request context, as
the async parent. Give `End` a valid export context after a request is cancelled
if you still want to report its failure.

The HTTP client has a timeout and refuses redirects. It does not automatically
retry. A caller retry must reuse the **same observation ID and content**; creating
a new operation for each delivery attempt would inflate the evidence. Async
export is an in-memory queue, not a durable outbox. Missing exports can bias the
received data; the server cannot discover observations it never received.

## Evidence identity and declared scenarios

A series key includes the ordered release path; scenario name and version;
check names and versions; evaluated flags; source; environment; schema. Changing
any one creates a different evidence partition. Release identity includes the
artifact as well as human version, contract and effective configuration.
Fingerprints identify producer claims; this is not independent code attestation.
Use a SHA-256 binary/image digest and a digest of allowlisted non-secret effective
configuration. Do not include tokens, addresses, customer/order IDs or bodies in
labels, flags or check explanations.

Scenarios are application-owned classifications. Define a bounded vocabulary,
select the scenario when starting an operation, and bump its version when its
meaning changes. Flags record the evaluated behavior for that execution, such
as `express=on`; they do not enable features or operate a flag provider.

Declare each required `Series` with `Engine.Evaluate` or `/v1/evaluate`. A never
observed requirement returns `missing`. Listing observed series alone is not a
coverage plan. A baseline must have the same scenario, checks, flags, source,
environment, schema, and ordered service names; release identities may differ.
Latency comparisons require sufficient fresh baseline observations.

`Source` and `Environment` are mandatory labels. Synthetic/mock observations
cannot satisfy a production/live requirement. The API accepts labels supplied
by trusted producers; it does not authenticate their truth. An incomplete
execution returns unknown rather than coverage credit. A failed recognized
response check immediately fails the assessment, including on HTTP 200.

The default coverage policy requires six fresh executions and recognized
checks. It separately displays the conditional 95% exact binomial bound.
Confidence mode additionally applies that bound. Neither mode proves arbitrary
business invariants, durable storage, external side effects or successful printing.
The binomial bound assumes independent representative requests and provides no
sequential-testing or system-wide guarantee.

## HTTP protocol

`cmd/ecg` exposes:

| Route | Contract |
|---|---|
| `GET /healthz` | Storage connectivity/readability; 503 when unavailable. |
| `POST /v1/observations` | `evidence.Observation` with `version: ecg.observation/v1`; 201 new, 200 duplicate, 409 conflicting ID, 400 invalid, 503 storage/capacity failure. |
| `POST /v1/evaluate` | `{"requirements":[{"series":{...},"baseline":{...}}]}`; baseline optional, 1–100 requirements. Returns `reports`. |
| `GET /v1/series` | Reports for observed series; does not invent missing requirements. |
| `GET /v1/capabilities` | Explicit supported scope and limitations. |

JSON uses the public Go types' field tags; unknown fields, trailing JSON and
bodies over 1 MiB are rejected. Request release paths contain 1–32 entries;
there are at most 32 declared checks and 16 bounded flag dimensions. Completion
times more than one second ahead of the server are rejected. Samples are
selected by execution completion time, not arrival time.

Set `ECG_API_TOKEN` for a shared bearer token on `/v1/*`. Health is unauthenticated.
There is no tenant authorization or TLS termination in this service. The
standalone Compose file binds its port to loopback; the demo service is internal
to its Compose network. Redis namespaces isolate keys, not permissions.

The service ingests **structured observations**, not arbitrary OTLP. The demo
keeps its explicit linear-chain trace assembler and supplies observations after
checking ancestry, identities and routing. Fan-out/joins/async workflow assembly,
release discovery, deployment orchestration and rollback are not library
capabilities. SDK spans alone do not become server observations: configure a
Recorder or an adapter. No process-level instrumentation is silently installed.

## Stores and containers

`memory.New` defaults to 1,000 historical series and 100,000 retained observation
IDs, with 24-hour retention. Restart loses data. `redisstore.New` defaults to
1,000 historical series, 10,000 retained samples per series, 100,000 retained
observation IDs globally, and 24-hour retention. Explicit options configure these
limits. Capacity exhaustion rejects new writes rather than evicting fresh
samples. Series metadata and lifetime totals remain until the store is removed;
plan namespace rotation for many releases. This is a bounded prototype, not
unlimited production storage.

IDs are deduplicated atomically for retention duration from acceptance. Exact
redelivery is a no-op; a conflicting reuse is rejected. After dedup expiry, replay
may count again. Redis snapshots read counts/samples atomically; snapshot windows
cannot exceed retention. Retention means available history, while the default
freshness window is five minutes. Late observations count in lifetime totals
but do not revive a fresh count. Memory and Redis have the same Store contract;
Redis persistence depends on its own configuration. The standalone Compose stack
enables AOF with Redis defaults, not a zero-loss durability guarantee.

```bash
# Embed without Docker, or run a bounded in-memory API:
go run ./cmd/ecg -listen 127.0.0.1:9100
# Persistent isolated API and sample:
docker compose -f compose.evidence.yml up --build -d --wait
docker compose -f compose.evidence.yml --profile example run --build --rm labels-example
# Full todo demo, including its evidence container and library dashboard:
docker compose up --build
```

Docker targets `evidence`, `labels-example` and `demo` run as an unprivileged
user. `demo` is the default target for existing deploy/retire scripts. The
standalone service accepts `-redis`, `-namespace`, `-window`, `-threshold` and
`-confidence`; Redis password and API token come from `ECG_REDIS_PASSWORD` and
`ECG_API_TOKEN`. Library options are explicit and do not read environment.
