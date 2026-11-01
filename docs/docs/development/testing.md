---
id: testing
title: Testing
sidebar_label: Testing
sidebar_position: 1
---

# Testing

The test pyramid **as it is on this branch**: a pure domain under a mutation gate, use
cases and adapters with unit and `httptest` tests, a godog BDD suite that drives the real
router and the real consumers over in-memory doubles, testcontainers integration tests
against real Postgres and Kafka, architecture fitness tests, spec linting and chart tests.

| Layer | Count / threshold | Command |
| --- | --- | --- |
| Unit, domain | 107 top-level test functions in 7 packages (`purchaseorder` 36, `shared/gs1` 26, `inbound` 19, `vendor` 8, `network` 7, `submission` 7, `shared` 4) | `make test` |
| Unit, application | 60 in `internal/application/{usecases,requests,gateway}` (53 + 5 + 2) | `make test` |
| Unit and `httptest`, adapters and `cmd/*` | `inbound/kafka` 40, `inbound/http` 32, `inbound/mcp` 25, `inbound/a2a` 16, `outbound/kafka` 18, `outbound/outbox` 12, `outbound/telemetry` 11, `outbound/httpclient` 8, `outbound/memory` 8, `kafka/cloudevents` 6, `outbound/postgres` 3, `requestlog` 4, `ids` 2, `clock` 1, `cmd/a2a` 15, `cmd/api` 14, `cmd/mcp` 12, `bootretry` 6 | `make test` |
| Coverage gate | 90 % over `./internal/domain/...,./internal/application/...` (`COVERAGE_THRESHOLD` in the `Makefile`) | `make coverage` |
| BDD (godog / Gherkin) | 8 feature files, **54 scenarios** (`Scenario` and `Scenario Outline` blocks) and 371 step lines | `make bdd` |
| Integration (testcontainers) | 20 top-level tests behind `//go:build integration`: `cmd/api` 2, `cmd/mcp` 2, `cmd/a2a` 2, `inbound/kafka` 1, `outbound/kafka` 1, `outbound/outbox` 5, `outbound/postgres` 7 | `make integration` |
| Architecture fitness | 32 top-level test functions in `internal/architecture` | `make arch-test` |
| Mutation (gremlins) | efficacy 99, mutant coverage 99 (`.gremlins.yaml`, pinned v0.6.0); the file records 100 % / 100 % measured for `network`, `purchaseorder`, `shared`, `submission` and `vendor` on 2026-10-23 | `make mutation`, `make mutation-full` |
| Spec linting (Spectral) | `apis/openapi.yaml` and `apis/asyncapi.yaml`, `--fail-severity=warn` | CI `api-lint` |
| Chart tests | `charts/vendor-connectivity/tests/render_test.sh` and `test_service_selectors.py` | CI `chart-tests` |
| Web remote | lint, `tsc -b`, `npm test` and `npm run build` in `web/` | CI `web` |
| Contract (consumer-driven or schemathesis) | **none**: no Pact or schemathesis configuration and no CI job. Consumed event shapes are pinned by tests that restate the producers' payloads as raw JSON, and `TestEventCatalogueMatchesContract` ties the published catalogue to `apis/asyncapi.yaml` | none |
| Evals | **none**: `EVALS.md` is the harness template's note; it measures nothing in this repo | none |

Counts come from `^func Test` per package on this commit (test files only, integration
files counted separately); many tests are table-driven, so the number of cases is larger.
Run `go test ./... -v` for the cases.

## Make targets

Every target mirrors a CI job; the `Makefile` is the only definition of the coverage scope.

| Target | Runs | Notes |
| --- | --- | --- |
| `make help` | prints the target list | default target |
| `make build` | `go build ./...` | compiles the three binaries |
| `make vet` | `go vet ./...` | |
| `make fmt` / `make fmt-check` | `gofmt -w .` / fail if `gofmt -l .` is non-empty | |
| `make lint` | `golangci-lint run --allow-parallel-runners ./...` | pinned `v2.14.0`, config `.golangci.yml` |
| `make test` | `go test ./... -race` | unit, `httptest` and BDD (the godog runner is a normal `Test` function); no database needed |
| `make coverage` | `go test ./... -race -coverprofile=coverage.out -coverpkg=./internal/domain/...,./internal/application/...`, then the 90 % gate | The CI `test` job runs exactly this |
| `make arch-test` | `go test ./internal/architecture/... -v` | |
| `make bdd` | `go test ./... -run TestFeatures -v` | the runner is `internal/bdd/bdd_test.go`; the module root holds no Go file |
| `make integration` | `go test -tags=integration ./... -race -count=1` | needs only Docker: each test boots its own Postgres and Kafka. Not part of `check` |
| `make mutation` | `gremlins unleash` on each package under `./internal/domain/*/` | the fast, blocking subset |
| `make mutation-full` | `gremlins unleash ./internal/domain --workers 1 --timeout-coefficient 30` | the exhaustive scheduled run |
| `make vuln` | `govulncheck ./...` | |
| `make check` | `fmt-check vet build lint test` | the pre-push hook (lefthook) runs this |
| `make check-all` | `check coverage arch-test bdd` | run before pushing |

## Unit tests

- **Domain** (`internal/domain/*`): pure Go, no fixtures. They pin the aggregate
  invariants: the vendor account's 1 h to 24 h acknowledgement window and its
  suspend/reinstate states, the purchase order's full-acknowledgement, window, label and
  confirmation rules, the submission's one-outcome rule, GS1 check digits and SSCC/GTIN
  parsing (`shared/gs1`), and the ordered domain events.
- **Use cases** (`internal/application/usecases`): run against the in-memory adapters
  (`internal/adapters/outbound/memory`), which are test doubles only: the shipped binaries
  never import them (`TestRuntimeBinaryHasNoInMemoryAdapters`). The repository contract
  (`internal/adapters/outbound/repocontract`) is one shared suite run against both the
  memory store and Postgres.
- **HTTP adapter**: `httptest` over the real router: handler groups, the idempotency
  capture (replay, reuse, rollback on 5xx), NUL rejection, readiness, telemetry.
- **Kafka consumers**: a fake `Reader` drives the run loop (`consume_loop_test.go`: retry,
  backoff, dead-letter after 5 attempts, no commit before success), plus `deadletter_*`
  tests and per-consumer tests whose fixtures restate the producers' contracts as raw JSON.
- **Outbox relay**: ordering, the attempt cap (`relay_cap_test.go`) and the ERROR record
  with `outbox_id` for a capped row.
- **MCP and A2A**: tool and skill outputs, read-only governance
  (`internal/adapters/inbound/mcp/governance_test.go`), the Agent Card
  (`internal/adapters/inbound/a2a/card_test.go`: no streaming, no security schemes).
- **`cmd/*`**: env parsing and boot configuration, shutdown order, telemetry bring-up.

Run one package or one test:

```bash
go test ./internal/domain/purchaseorder -v
go test ./internal/adapters/inbound/kafka -run TestConsumeLoop -v
```

## BDD (godog)

`features/*.feature` holds eight files; the runner is `internal/bdd/bdd_test.go`
(`TestFeatures`, `Strict: true`, path `../../features`). It drives the real chi router
over HTTP and feeds the local copies and the releases through the real consumers'
`HandleMessage` with raw CloudEvents, on the in-memory doubles wired the way `cmd/api`
wires the real adapters.

| Feature file | Scenarios |
| --- | --- |
| `purchase_order_lifecycle.feature` | 12 |
| `node_and_inventory_transactions.feature` | 9 |
| `inbound_shipments_and_receipts.feature` | 8 |
| `vendor_accounts.feature` | 8 |
| `node_profile_and_scorecard.feature` | 7 |
| `labels_and_shipment.feature` | 5 |
| `polling_pagination.feature` | 3 |
| `simulation_knobs.feature` | 2 |

Step files: `steps_test.go`, `steps_inbound_test.go`, `steps_wave2_test.go`.

## Integration tests

Every integration test boots its own containers through `internal/testing/pgtest` and
`internal/testing/kafkatest`; there is no `DATABASE_URL`, no `docker compose` and no
`t.Skip` gate (`TestPostgresIntegrationTestsUseTestcontainers` and
`TestKafkaIntegrationTestsUseTestcontainers` enforce it). They cover the Postgres
repositories against the shared contract, the transactional outbox and relay (including the
attempt cap), the consumers against a real broker, and the three binaries end to end
(`cmd/api/e2e_integration_test.go` and the `main_integration_test.go` files).

## Architecture fitness tests

`internal/architecture` (32 test functions) enforces what the layout promises:

- `TestHexagonalArchitecture`, `TestMCPAdapterDependencyRule`, `TestA2AAdapterDependencyRule`:
  the dependency rule between domain, application and adapters.
- `TestRuntimeBinaryHasNoInMemoryAdapters`: `cmd/**` never imports the memory adapter or
  `repocontract`.
- `TestNoGoFilesAtRepoRoot`: Go files live under `cmd/` and `internal/`.
- `TestNoAuthMiddlewareReintroduced`: no auth middleware on REST, MCP or A2A.
- `TestA2AAgentIsReadOnly`, `TestMCPOptionalToolInputsCarryOmitempty`: the agent surfaces
  stay read-only and their optional inputs stay optional.
- `TestCloudEventsOnly`, `TestNoEventEnvelopeToggleOrFlatEnvelope`,
  `TestEventCatalogueMatchesContract`: CloudEvents 1.0 only, and the event catalogue equals
  `apis/asyncapi.yaml`.
- Kafka hygiene: consumer groups never hard-coded (`TestKafkaConsumerGroupNeverHardcodedInline`),
  writers set `RequiredAcks`, never set the topic twice, dead-letter calls guard
  cancellation, and relays pass the attempt cap.

Most sensors have a companion `...SensorBites` / `...FailsOnBadFixtures` test that proves
the sensor fails on a bad fixture.

## Mutation testing

`.gremlins.yaml`: one worker, timeout coefficient 30, efficacy and mutant coverage
thresholds 99 (gremlins fails at or below the threshold). `make mutation` runs every
package matched by `./internal/domain/*/`; `make mutation-full` runs `./internal/domain`
whole.

## CI jobs (`.github/workflows/ci.yml`)

| Job | What it runs | When |
| --- | --- | --- |
| `lint` | golangci-lint `v2.14.0` | push, PR |
| `guide-lint` | `scripts/harness/guide_lint.py`, `test_hook.py` | advisory (`continue-on-error`) |
| `complexity` | gocyclo, gocognit, cyclop, funlen, nestif | push, PR |
| `file-length` | revive file-length budget (300 code lines) | blocking on PRs into `develop`, informational otherwise |
| `test` | build, vet, `make coverage` | push, PR |
| `bdd` | `go test ./... -run TestFeatures -v` | push, PR |
| `integration` | `go test -tags=integration ./... -race -count=1` | push, PR |
| `mutation-fast` | gremlins on every `./internal/domain/*/` | push, PR |
| `mutation` | gremlins on `./internal/domain` | schedule, manual |
| `drift` | deadcode, `go mod tidy -diff`, knip, coverage-quality | schedule, manual (advisory) |
| `api-lint` | Spectral on both specs | push, PR |
| `vuln` | `govulncheck ./...` | push, PR |
| `helm-lint` | `ct lint` and the service-selector test | PR only |
| `chart-tests` | every `charts/vendor-connectivity/tests/*.sh` and `test_*.py` | push, PR |
| `arch-test` | `go test ./internal/architecture/... -v` | push, PR |
| `web` | lint, `tsc -b`, tests and build of `web/` | push, PR |
| `docker-publish`, `release` | image to GHCR (cosign-signed) and a GitHub release with the chart | push to `main` only |

The documentation site has its own workflow, `.github/workflows/docs.yml`: it builds the
site on every pull request and deploys on push to `main`.
