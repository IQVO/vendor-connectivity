---
name: architecture-review
description: Bounded-context boundary and ADR-compliance review of a change (expensive, post-integration): hexagonal direction, cross-context coupling, contradicted ADRs. Invoke explicitly: /architecture-review [range].
disable-model-invocation: true
argument-hint: "[git range]"
---

Perform a bounded-context boundary and ADR-compliance review of the
current changes (or `$ARGUMENTS` if given, e.g. a branch/PR diff range).

This is EXPENSIVE relative to `/code-review` — it reasons about
cross-repo/cross-context implications, not just this diff's local
correctness. Use it post-integration (before merging a PR that touches
architecture, not on every small commit) or when asked explicitly to
check a design decision against this fleet's standing architecture.

## What to check, in priority order

1. **Hexagonal dependency direction.** Domain depends on nothing;
   application depends on domain+ports; adapters depend on
   application+domain; only `cmd/` wires every layer together. Run
   `go test ./internal/architecture/... -v` first — if it's already red,
   report that and stop; don't hand-review what a fitness test already
   caught.
2. **Context boundary.** vendor-connectivity consumes exactly the eight events of
   `docs/adr/0003-consumed-contracts-and-local-read-models.md` (node-topology x5,
   offer-availability x1, order-sourcing x2) into local read models and makes exactly
   four synchronous in-org calls (node-topology, offer-availability,
   shipping-and-tracking, node-performance; `internal/adapters/outbound/httpclient`).
   Any other consumer or outbound call is a new architectural decision that needs its
   own ADR in `docs/adr/`, not a code change slipped in silently. Sibling data is
   never read live.
3. **Runtime rule.** `cmd/**` must not import `internal/adapters/outbound/memory`
   or `repocontract` (`TestRuntimeBinaryHasNoInMemoryAdapters`,
   `TestRuntimeBinaryDoesNotImportTheRepositoryContract`); the relay is built with
   `WithMaxAttempts` (`TestOutboxRelaysPassAttemptCap`); `/readyz` keeps its
   empty-topic Kafka metadata check. This repo has no MCP adapter and no analytics
   read side.
5. **Kafka consumer-group correctness.** Each consumer's group id comes from its
   mandatory env var (`NODE_CONSUMER_GROUP`, `LISTING_CONSUMER_GROUP`,
   `SOURCING_CONSUMER_GROUP`), never a literal
   (`TestKafkaConsumerGroupNeverHardcodedInline`); every handler is idempotent on the
   CloudEvents id through the inbox and dispatches on the full `type`. See
   `.claude/rules/fleet/kafka-testing-and-consumers.md`.
6. **A new bounded-context integration with no companion documentation.**
   If this change introduces or changes a cross-repo contract (a new
   REST call, a new Kafka topic subscription, a new MCP tool consumed by
   a sibling), check whether an ADR documents the decision — and whether
   a companion ADR should exist in the OTHER repo too, per this fleet's
   companion-ADR convention (see `.claude/skills/how-to-write-an-adr/SKILL.md`).
7. **Auth-reintroduction and sibling-call bans**, same as `/code-review`
   items 6-8, but reasoned about more thoroughly here — check not just
   "is there a Bearer literal" but "does this change's INTENT require
   re-litigating the fleet-wide auth-removal decision," which would need
   its own ADR, not a silent code change.

## Output format

State clearly: PASS (no architectural concerns), CONCERNS (list them,
each tied to the specific rule/ADR it would violate), or NEEDS-ADR (the
change is architecturally sound but undocumented — name what the ADR
should cover). Cite the specific file/rule/ADR for every finding; a
finding with no citation is not actionable.

This is advisory. It never blocks a merge on its own, and it never
modifies files. If a finding conflicts with a decision explicitly stated
in this repo's own AGENTS.md/CLAUDE.md, defer to that document and say
so — this command reasons about the fleet's general conventions, not a
higher authority than the repo's own explicit guidance.
