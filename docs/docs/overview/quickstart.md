---
id: quickstart
title: Quickstart
sidebar_label: Quickstart
sidebar_position: 3
---

# Quickstart

Build the service, boot it, see what it refuses to boot without, run it on Postgres and Kafka, and make
the first calls. **Nothing on this page was captured from a live run of this branch:** the commands,
the refusal messages and the request bodies are taken from the code (`cmd/api/config.go`,
`cmd/api/main.go`, `apis/openapi.yaml`, `Makefile`) and the answers are described, not pasted. Run the
commands yourself to see the real output.

## Prerequisites

| Tool | Version | Needed for |
| --- | --- | --- |
| Go | `1.27.3` (`go.mod`) | everything |
| `make` | any | the targets below |
| Docker | any recent | a throwaway Postgres, and `make integration` (testcontainers boots its own Postgres and Kafka) |
| golangci-lint | `v2.14.0` (`GOLANGCI_VERSION` in the `Makefile`, pinned in CI) | `make lint`, `make check` |
| gremlins | `v0.6.0` (`GREMLINS_VERSION`) | `make mutation`, `make mutation-full` |
| govulncheck | latest | `make vuln` |
| Node | 20 (`.github/workflows/docs.yml`) | building this documentation site (`docs/`) and the `web/` remote |
| lefthook | optional | `lefthook install` runs `fmt-check`, `vet`, `lint` at pre-commit and `make check` at pre-push |

The fleet runs **one** Kafka broker (in-cluster, reachable at `localhost:9092` from the host through the
kind cluster); there is no docker-compose Kafka in this repository. The service needs Postgres **and**
Kafka to boot and to be ready: there is no in-memory mode, and `cmd/**` never links the memory adapters.
It also needs the four neighbour base URLs, but only dials them when a submission is processed (see
below).

## Build, test, run

```bash
make help          # list the targets
make build         # go build ./...
make test          # go test ./... -race
make arch-test     # go test ./internal/architecture/... -v
make bdd           # go test ./... -run TestFeatures -v
make coverage      # tests with -race plus the 90% gate over domain + application
make check         # fmt-check vet build lint test
make check-all     # check + coverage + arch-test + bdd
make integration   # go test -tags=integration ./... -race -count=1 (needs Docker)
```

The pyramid behind these targets, the scenario counts and the CI jobs are on
[Testing](/docs/development/testing).

Build the binary and start it with nothing configured:

```bash
go build -o vendor-connectivity ./cmd/api
HTTP_ADDR=127.0.0.1:18080 ./vendor-connectivity
```

It refuses to boot, before it opens a connection or binds the port, and exits with status 1, naming the
first missing variable (they are reported one at a time, in the order the code checks them):

```text
DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode
```

The next ones, once that is set, are `KAFKA_BROKERS`, the five `*_CONSUMER_GROUP` ids and the four
neighbour URLs. `EVENT_PUBLISHER=log` and out-of-range knobs such as `TX_FAILURE_RATE=2` are boot
errors too. Every variable is in [Configuration](/docs/operations/configuration); the full list of
failure modes is in [Troubleshooting](/docs/operations/troubleshooting).

## Run on Postgres and Kafka

Start a throwaway database and point the service at your Kafka (the service never creates topics through
an admin API; the broker auto-creates the two it produces on the relay's first write, and the topics it
consumes exist once their producers have published, see the [Runbook](/docs/operations/runbook)):

```bash
docker run -d --rm --name vc-pg -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=vc \
  -p 127.0.0.1:55432:5432 postgres:16-alpine

DATABASE_URL='postgres://postgres:pw@127.0.0.1:55432/vc?sslmode=disable' \
KAFKA_BROKERS=localhost:9092 \
NODE_CONSUMER_GROUP=vendor-connectivity-node-dev \
LISTING_CONSUMER_GROUP=vendor-connectivity-listing-dev \
SOURCING_CONSUMER_GROUP=vendor-connectivity-sourcing-dev \
INBOUND_CONSUMER_GROUP=vendor-connectivity-inbound-dev \
PACKAGING_CONSUMER_GROUP=vendor-connectivity-packaging-dev \
NODE_TOPOLOGY_URL=http://127.0.0.1:8081 OFFER_AVAILABILITY_URL=http://127.0.0.1:8082 \
SHIPPING_AND_TRACKING_URL=http://127.0.0.1:8083 NODE_PERFORMANCE_URL=http://127.0.0.1:8084 \
HTTP_ADDR=127.0.0.1:18080 ./vendor-connectivity
```

The group ids are yours to choose (the code has none); a fresh group replays its topic from the first
offset, so the local node and listing views converge by themselves. The embedded migrations run at
boot. The startup log is JSON on stdout; the messages, in order, are listed on
[Observability](/docs/operations/observability). The four neighbour URLs may point at nothing while you
only exercise reads and the purchase-order path: they are dialled only when a submission of the kind
that needs them (node registration, node profile, inventory) is processed, or for a label or a
scorecard. Point them at the real services (or a stub) to try those.

`GET /readyz` is `200` only while Postgres answers and a Kafka broker answers a metadata request; with
either down it answers `503` `application/problem+json` (`dependency-unavailable`) naming which one,
never why (the cause is in the WARN log). `GET /healthz` is liveness only. Stop the process with
Ctrl-C: it flips `/readyz` to `503`, waits `SHUTDOWN_DRAIN_DELAY` (5 s), drains HTTP, stops the
consumers and loops, stops the relay and closes the pool.

## First calls

Every write needs an `Idempotency-Key` header, there is no authentication, and a vendor names itself with
`vendorId` (body of a write, query parameter of a read). Replace the host with
`localhost:8000/api/retail-network/vendor-connectivity` to go through Kong.

```bash
BASE=http://127.0.0.1:18080

# 1. onboard a vendor with a 4-hour acknowledgement window (201 on first call)
curl -si -X POST $BASE/vendors -H 'Content-Type: application/json' -H 'Idempotency-Key: onboard-V-WS' \
  -d '{"vendorId":"V-WS","name":"Warehouse Systems","ackWindowMinutes":240}'

# 2. read the account back
curl -s "$BASE/vendors/V-WS"

# 3. poll the vendor's purchase orders (empty until order-sourcing releases something to a V-WS node)
curl -s "$BASE/vendor/purchase-orders?vendorId=V-WS&limit=50"

# 4. acknowledge one in full (202 with a transactionId); every line, at the ordered quantity
curl -si -X POST $BASE/vendor/acknowledgements -H 'Content-Type: application/json' -H 'Idempotency-Key: ack-PO-1' \
  -d '{"vendorId":"V-WS","poNumber":"PO-7f3c","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2}]}'

# 5. reconcile the transaction (PROCESSING, then SUCCESS or FAILURE)
curl -s "$BASE/vendor/transactions/<transactionId from step 4>?vendorId=V-WS"
```

What to expect, from the code: step 1 answers `201` with the account, and a repeat with the same key
replays the stored answer; a repeat with a different key answers `200` with the unchanged account.
Step 3 answers a page `{"purchaseOrders":[...],"nextToken":"..."}` with no `nextToken` on the last
page. Step 4 answers `202` and `{"transactionId": "..."}`; the order moves to `ACK_PROCESSING` and, after
`TX_PROCESSING_DELAY` and the next submission-processor pass (every `SUBMISSION_POLL_INTERVAL`), to
`ACCEPTED`, publishing `PurchaseOrderAccepted` and `SubmissionProcessed`. A missing `Idempotency-Key`
is `422 idempotency-key-required`; an unknown order is `404 not-found`; an acknowledgement after `ackBy` is
`409 ack-window-closed`. The error slugs are listed on [API reference](/docs/api-reference).

To see purchase orders without a running order-sourcing, publish a `FulfillmentReleasedToNode`
CloudEvent for a node that the local node view knows (`NodeRegistered` for a `VENDOR` node of `V-WS`
first) on `retailnetwork.order-sourcing.events`; the shapes are on [Events](/docs/api-reference/events).

## Other binaries

```bash
# read-only MCP server (Streamable HTTP at /mcp and /, :8090)
DATABASE_URL=... NODE_TOPOLOGY_URL=... OFFER_AVAILABILITY_URL=... \
SHIPPING_AND_TRACKING_URL=... NODE_PERFORMANCE_URL=... go run ./cmd/mcp

# read-only A2A agent (JSON-RPC at / and /a2a, Agent Card at /.well-known/agent-card.json, :8093)
A2A_AGENT_URL=http://localhost:8093/ DATABASE_URL=... NODE_TOPOLOGY_URL=... \
OFFER_AVAILABILITY_URL=... SHIPPING_AND_TRACKING_URL=... NODE_PERFORMANCE_URL=... go run ./cmd/a2a
```

Both read the database `cmd/api` owns and never touch Kafka. The MCP tools are on
[MCP tools](/docs/mcp/tools). The operator console remote lives in `web/` (`cd web && npm ci && npm run dev`
serves it standalone on `:4021`; `@warehouse/ui-kit` must be a sibling checkout).

## Build this documentation site

```bash
cd docs
npm ci
npm run build   # fails on a broken link or an invalid Mermaid diagram
npm start       # dev server
```

The REST reference under `docs/docs/api-reference/rest/` is generated from `apis/openapi.yaml` by
`npm run gen-api-docs`; do not edit it by hand.
