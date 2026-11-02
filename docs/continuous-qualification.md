# Continuous qualification of immutable versions

The demo implements the flow in the [design reference](images/continuous-qualification.png):
versions run isolated checks independently, share a traffic controller, and earn
stable admission from evidence. There is no integration/staging promotion queue.
The libraries evaluate evidence; the demo controller owns traffic and lifecycle.

## What runs

| Design element | Demo implementation |
|---|---|
| Immutable version | Binary digest, service/version, contract, and effective behavior configuration identify the release. A live instance cannot change identity. |
| Isolated tests | Each service starts a private fixture suite before connecting to shared Redis or registering. It runs actual handlers against disposable local peer handlers and an in-memory Redis emulator. HTTP destinations are allowlisted to those fixtures. |
| Independent progress | Every process runs its own suite. Eligible requests rotate across viable canaries; an earlier candidate with missing evidence cannot reserve the rollout queue. |
| Stable | The baseline handles ordinary traffic. It must retain fresh, sufficient, checked evidence under the current policy and dependency combination. |
| Canary | Only eligible tenants receive a candidate response. Each request pins one candidate and the current baseline versions of its peers. |
| Shadow | List reads are mirrored in a bounded background task. The response is discarded. Only `GET /todos` is mirrored, and each service rejects writes carrying shadow context. |
| Admission | A passing isolated suite is required before either canary or shadow exposure. Canary promotion additionally requires live exact-path evidence, matching baseline latency evidence, and the selected confidence policy. |
| Withdrawal and rollback | The controller reevaluates stable admission every two seconds. A failing or expired qualification is withdrawn. A running predecessor is restored only when its evidence remains sufficient against current peers. Otherwise requests fail closed. |
| Retirement | A replaced baseline drains for at least 90 seconds, beyond the 60-second routing-plan lifetime plus request timeout. It is automatically retired only while its replacement remains qualified. Manual retirement is also available. |

`EXPOSURE=shadow` holds a version in shadow mode. The dashboard's **admit canary**
button changes its requested exposure; failing or inconclusive shadow checks
block that transition. A passing shadow read does not prove a write operation.
Once a canary qualifies, stable admission is automatic. Promotions update one
baseline at a time and recompute all affected requirements before the next
decision; verification traffic itself proceeds in parallel. Traffic scheduling
reuses assessments for at most one second to avoid putting repeated ledger scans
on every request. Observed failures and lifecycle/policy changes invalidate that
cache; promotion always evaluates fresh evidence directly.

## Confidence has context

An immutable release owns its evidence, but its admission is conditional:

```
release + exact dependency path + scenario/check versions + flags
        + source/environment/schema + policy + evidence window
        → qualification decision
```

The library partitions observations by these execution dimensions. Policy and
time select how those observations are evaluated. Changing a dependency cannot
reuse another full path's observations. Changing a binary or effective behavior
configuration creates a new identity, even if its human version label is reused.

Isolated fixture checks use the public SDK and evaluator. Their declared cases
cover add, invalid add, list, complete, delete, and the slow-read/retry path under
both flag values. The store omits invalid-add cases because validation returns
before reaching it. A missing, duplicated, mismatched, failed, or unchecked case
cannot satisfy the suite. Failed reruns cannot overwrite a prior failure for the
same release and suite. These finite contract checks are retained for that
immutable identity and suite version; they are not a statistical traffic sample.

Live evidence uses a rolling window. Shadow observations are exported to the
library under their own source, are displayed separately, and never enter live
promotion counts or stable health signals. Isolated checks are exported as
`isolated / disposable-fixture` and likewise cannot substitute for live checks.
The demo's live rollout ledger still combines browser and synthetic requests;
the separate observation service preserves both sources. Production adoption
would need an explicit policy for representative traffic and producer trust.

Stable monitoring checks current full-path coverage, correctness, freshness,
and optional confidence. Incomplete or failed stable traffic contributes a
health signal scoped to the exact baseline combination. Candidate or shadow
failures cannot contaminate a different stable combination. Rollback considers
the latest admitted change first, then reevaluates its peers against the restored
combination. This is conservative failure containment, not root-cause proof.
Latency is compared with the predecessor during admission; continuous detection
of latency drift against a fixed SLO remains a separate extension.

## Bootstrap and operating boundaries

The initial Compose services explicitly request `BOOTSTRAP=true`. They must pass
isolated checks, then have a 90-second bootstrap window to collect live evidence.
Known failures withdraw them immediately; insufficient evidence after the window
also withdraws them. A later candidate cannot become the initial baseline merely
by registering first. Withdrawal revokes automatic rejoining of that identity.
Recovery without a qualified predecessor requires operator intervention; there
is no fallback to an unverified candidate.

The fixture suite is isolated by private data, local peer handlers within the
service process, and an HTTP destination allowlist. It is **not an OS security
sandbox**, an external test authority, or proof of production database behavior.
Peers in the suite come from the same binary; live traffic must establish
compatibility with the actually deployed peers. Digests, check results, and
routing baggage are trusted producer claims, not signed attestations. This demo
does not run untrusted application code.

Shadow support is deliberately limited to the demo's read-only list operation.
General shadow execution of payments, notifications, or other writes needs
separate data, external-service substitutes, and enforced egress/side-effect
controls. No general write-shadowing capability is claimed.

The controller remains a single local demonstration with ephemeral Redis,
bounded evidence retention, a synchronous linear HTTP model, and no distributed
controller coordination. It does not provision per-release infrastructure,
assemble asynchronous workflows, or prove arbitrary business invariants.
Six successful requests in the default coverage mode are not a statistical
safety guarantee. The optional 95% bound assumes independent representative
samples and is not a sequential-testing guarantee.

## Regression evidence

- [Controller qualification tests](../cmd/controller/qualification_test.go)
  exercise independent progress, exact release binding, shadow separation,
  evidence-based rollback, and withdrawal when evidence expires.
- [Service isolation tests](../cmd/service/isolation_test.go) run the real fixture
  suite against healthy and corrupted handlers and reject shadow writes before
  any storage access.
- [Existing controller tests](../cmd/controller/evidence_test.go) cover full-path
  dependencies, pinned requests, identity changes, replay, missing telemetry,
  latency, confidence, and lifecycle behavior.
- [Library tests](../evidence/engine_test.go) verify partitioning, declared missing
  requirements, freshness, duplicate/conflicting IDs, and storage failures.
