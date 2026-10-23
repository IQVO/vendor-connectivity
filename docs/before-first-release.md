# Before the first release: vendor-connectivity

The first merge to `main` runs `docker-publish` and `release`. Packaging that is missing or wrong
at that moment forces a rework, so work through this list on a feature branch, **before** the
first `develop` → `main` PR. Delete a row only when the service really does not have that part, and
say so in the PR.

`Dockerfile` and `.dockerignore` came from the fleet reference. The chart is NOT generated: it is
shaped by what the service runs (here three consumer groups, four neighbour URLs, the simulation
knobs), so copy the closest sibling's `charts/<repo>/` and adapt it.

| # | Item | Status | Done when | Guarded by |
|---|------|--------|-----------|------------|
| 1 | **Real runtime only.** `cmd/api` wires Postgres and Kafka and nothing else; the test doubles under `internal/adapters/outbound/memory` are never imported by `cmd/**`. `DATABASE_URL`, `KAFKA_BROKERS`, the three `*_CONSUMER_GROUP` ids and the four neighbour URLs are mandatory. | [x] done | `cmd/**` has no import of the memory or repocontract packages; `loadRuntime` returns a boot error naming the variable before anything opens. | `TestRuntimeBinaryHasNoInMemoryAdapters`; boot errors in `cmd/api/main_test.go` (`TestLoadRuntimeRefusesWithTheVariableName`). |
| 2 | **No Go file at the repo root.** The godog runner is `internal/bdd/bdd_test.go`, the scenarios are in `features/`. | [x] done | `ls *.go` prints nothing; `make bdd` finds `TestFeatures`. | `TestNoGoFilesAtRepoRoot`. |
| 3 | **`Dockerfile` builds every `cmd/*`.** `ENTRYPOINT ["./api"]` is `cmd/api`, `EXPOSE 8080` is the default `HTTP_ADDR`. | [ ] pending verification | `docker build .` succeeds and `docker run --rm <image>` exits 1 with `missing required environment variable: DATABASE_URL`. The build stage digest is the one the sibling services pin (the template's older digest ships an older Go than `go.mod` requires). | `trivy-scan` / `docker-publish` jobs. |
| 4 | **`.dockerignore`** excludes docs, harness, test output. | [x] done | Build context is small. | none: review once. |
| 5 | **Helm chart `charts/vendor-connectivity/`** with `Chart.yaml` `name: vendor-connectivity`; chart directory, `Chart.yaml` name and the `helm package` file name must all be `vendor-connectivity`. | [ ] **follow-up** (out of scope of the service build) | `helm lint` and `helm template` with the required values pass. | `helm-lint`, `release`. |
| 6 | **`DATABASE_URL` comes from a Secret**, never from the ConfigMap. | [ ] follow-up (chart) | `helm template` shows `secretKeyRef`. | chart test (row 8). |
| 7 | **Pod hardening and shutdown room.** `terminationGracePeriodSeconds` above the binary's bounds: `SHUTDOWN_DRAIN_DELAY` (5 s) + HTTP drain (10 s) + consumers and loops (10 s) + relay (10 s) = 35 s, so 40 s or more. | [ ] follow-up (chart) | Values and Deployment carry it. | chart test (row 8). |
| 8 | **Chart tests in `charts/vendor-connectivity/tests/`.** Every env var `cmd/api` reads must be rendered (see `docs/runtime.md`). | [ ] follow-up (chart) | `bash charts/vendor-connectivity/tests/render_test.sh` prints `PASS`. | `chart-tests` job (only warns when the directory is empty). |
| 9 | **Consumer group ids are chart values**: `NODE_CONSUMER_GROUP`, `LISTING_CONSUMER_GROUP`, `SOURCING_CONSUMER_GROUP`, stable and service-prefixed, never derived from the pod name. | [ ] follow-up (chart) | Each is rendered from `config.*` and refuses to render empty. | chart test (row 8); `kafka-testing-and-consumers.md` rule. |
| 10 | **Optional CI jobs match the service.** `web` and `docs-api-drift` are not in `ci.yml` (no `web/`, no generated docs); `api-lint` runs the two Spectral rulesets on `apis/openapi.yaml` and `apis/asyncapi.yaml`. | [x] done | No job in `ci.yml` is permanently red or skipped for a missing directory. | first PR's CI run. |
| 11 | **Repository secrets** `DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`; GHCR push allowed for the Actions token. | [ ] ask the repo admin | A dry run of `docker-publish` on a throwaway branch. | none. |
| 12 | **`govulncheck` is clean** before the PR to `main`. | [ ] pending verification | `make vuln` exits 0. | `vuln` job. |

Follow-ups left for after the service build: the Helm chart and its tests (rows 5-9), the repository
secrets (row 11). Keep this file: it is the service's packaging record.
