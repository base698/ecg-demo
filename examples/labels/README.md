# Generic label example

```bash
go run ./cmd/labels-example
go run ./cmd/labels-example -endpoint http://localhost:9100
```

This example uses a fictional protocol: POST a count to `/labels` and receive
an array of text labels. Its mock server runs on loopback. No external business
system, carrier, or printer is called, and no third-party application client is
included. Only the ECG evidence endpoint is configurable.

The example injects an OpenTelemetry provider and emits 18 observations through
the public SDK:

- Six valid single-label responses: coverage satisfied, confidence insufficient.
- Six valid two-label responses: coverage satisfied, confidence insufficient.
- Six HTTP 200 responses missing labels: response check failed.
- One declared production/live requirement with no executions: missing.

The response check requires the requested number of distinct, nonempty labels.
It proves neither durable storage nor physical printing. Fixture release digests
identify fixture labels, not attested production builds. Repeated synthetic
cases are not independent representative production traffic.

`go test ./examples/labels` drives the fixture through the SDK, HTTP API and
memory store. Store contract tests exercise memory and Redis. The todo demo's
integration test also covers its trace adapter and asynchronous exporter.
