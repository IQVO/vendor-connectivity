---
id: core-domain-chart
title: Core Domain Chart
sidebar_label: Core Domain Chart
sidebar_position: 1
description: "Why Vendor Connectivity is a Supporting subdomain with a Product-stage protocol, plotted on the ddd-crew Core Domain Chart."
---

# Core Domain Chart

Following the ddd-crew [Core Domain Charts](https://github.com/ddd-crew/core-domain-charts):
business differentiation (y) against model complexity (x).

```mermaid
quadrantChart
    title vendor-connectivity on the Core Domain Chart
    x-axis Low model complexity --> High model complexity
    y-axis Low business differentiation --> High business differentiation
    quadrant-1 Core
    quadrant-2 Decisive - Short-term Core
    quadrant-3 Supporting
    quadrant-4 Generic
    vendor-connectivity context: [0.46, 0.36]
    PurchaseOrder protocol and clock: [0.44, 0.42]
    Submission transactions: [0.38, 0.24]
    VendorAccount onboarding: [0.16, 0.18]
```

Source: the three slices are the aggregates in `internal/domain/purchaseorder` (eight states, six domain errors in `purchaseorder.go`), `internal/domain/submission` (three states, two errors) and `internal/domain/vendor` (two states); the positions are judgement, not code, and the context point is the aggregator's published value (see [Subdomain classification](/docs/ddd/subdomain-classification)).

Omitted: the other ten contexts (see the
[network-wide chart](https://iqvo.github.io/warehouse-docs/network/strategic-design/core-domain-chart)) and the fleet's
`network-fulfillment`, which sits on the fleet's own chart. Slice
positions are judgement; only the context point (complexity 0.46,
differentiation 0.36) is the aggregator's published value.

## Classification: Supporting

All four points sit in **quadrant 3 (Supporting)**. The network needs this
context to buy fulfillment from vendors, and it must be right, but it is
not where the network wins customers. The decisions that differentiate
(which node, which path, which promise) belong to `order-sourcing` and
`delivery-promise`, both Core. This context executes what they decide and
reports what vendors answer. See
[subdomain classification](https://iqvo.github.io/warehouse-docs/network/strategic-design/subdomain-classification).

**Business model.** Revenue-channel enabler: third-party capacity without
integration cost. Every vendor that integrates once can be sourced from,
and the network pays no per-vendor integration.

**Evolution: Product.** The shape of the protocol (polled orders, a
whole-order acknowledgement inside a window, asynchronous writes reconciled
by a transaction id, labels before shipment) is well known from public
vendor interfaces (see the vendor platform API surface map).
The network adopts that shape in its own vocabulary rather than inventing
one. What stays custom is the `ackBy` rule and the link to the path lock
([ADR 0005](https://iqvo.github.io/warehouse-docs/network/adr/0005-vendor-acceptance-is-the-path-lock)).

## Why the slices sit where they do

- **PurchaseOrder protocol and clock** is plotted highest. It carries the
  one rule that shapes network behaviour: `ackBy` is the earlier of the
  vendor's window and `requiredShipBy - handlingTime`, so a slow answer can
  never eat the handling time. It has eight states and six invariants,
  which is the bulk of the context's complexity.
- **Submission transactions** are mechanism: three states, two invariants,
  and the simulation knobs. They matter for correctness, not for
  differentiation.
- **VendorAccount onboarding** is a two-state master record with one range
  check and a suspension guard; close to Generic, but it holds the vendor's ack window, which feeds
  `ackBy`, so it stays here rather than being bought.

**Movement to watch.** If the network starts negotiating per-vendor terms
through the protocol (for example, windows that vary by tier or by
scorecard grade), the PurchaseOrder slice would move right and up. Nothing
in the code does that today.
