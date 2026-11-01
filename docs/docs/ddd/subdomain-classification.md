---
id: subdomain-classification
title: Subdomain classification
sidebar_label: Subdomain classification
sidebar_position: 2
description: "Vendor Connectivity is a Supporting subdomain in the partner tier of the retail network, the network's Open Host Service for vendors; why it is not Core or Generic, and its neighbours' verdicts."
---

# Subdomain classification

`vendor-connectivity` is a **Supporting** bounded context in the **Network** area, CloudEvents tier
`partner` (every event type it publishes starts
`com.retailnetwork.partner.vendor-connectivity.`). The verdict is the one in the aggregator's
[Subdomain classification](https://iqvo.github.io/warehouse-docs/network/strategic-design/subdomain-classification)
(`network/docs/strategic-design/subdomain-classification.md` on `origin/main`): business model
*revenue-channel enabler*, evolution *Product*. The neighbour rows below are copied from that table,
not recalled. The page was read on 2026-11-01; if the aggregator has moved since, it wins.

## Why Supporting

- **Not Core.** The competitive advantage of the network is the delivery promise
  (`delivery-promise`) and where an order is sourced (`order-sourcing`). This context decides
  neither: it receives a release and issues a purchase order from it, and it relays a vendor's
  answer. Its rules are narrow and deterministic: a window to acknowledge (`ackBy`, between 1 h and
  24 h), an order acknowledged once and in full, labels before a shipment, a vendor that is
  suspended or not. Nothing in `internal/domain` ranks, prices or chooses.
- **Not Generic.** The protocol is specific to this network: the purchase order carries a
  `requiredShipBy` taken from the sourcing release, the acknowledgement window is derived from the
  vendor's agreement and the node's handling time, every write is a transaction with an
  `Idempotency-Key`, and the vendor's own node ids come from `node-topology`. Industry practice
  (a direct-fulfilment-style exchange) shapes it, but it is not bought off the shelf unchanged.
- **Open Host Service.** It is the single front door every vendor system (in this fleet:
  `network-fulfillment`, vendor `V-WS`) goes through, so contract stability matters more than
  cleverness. That is why the repository carries an OpenAPI 3.0.3 document for the Vendor API and an
  AsyncAPI 3.0.0 document for its events, and why the contract is the first thing a change must
  keep ([API reference](/docs/api-reference)).

Model complexity is low to moderate: three small aggregates (`VendorAccount` with two states,
`PurchaseOrder` with eight, `Submission` with three), read models fed by events, and no algorithm. See the
[Core domain chart](/docs/ddd/core-domain-chart). What would move it: a rule that
decides which vendor or node serves an order would make it part of sourcing (Core); replacing the
protocol with a purchased marketplace integration behind an anti-corruption layer would make it Generic.

## Neighbours (from the aggregator's table)

| Bounded context | Classification | CloudEvents tier | Relation to this context |
| --- | --- | --- | --- |
| `order-sourcing` | Core | `commerce` | upstream (releases and cancellations); downstream (consumes the purchase-order events) |
| `node-topology` | Supporting | `logistics` | upstream (node events); called synchronously to register nodes and declare profiles |
| `offer-availability` | Supporting | `commerce` | upstream (listings, packaging); called synchronously for inventory feeds |
| `seller-inbound` | Supporting | `partner` | upstream (inbound shipment events); downstream (consumes the inbound receipt) |
| `shipping-and-tracking` | Generic | `logistics` | called synchronously for labels; downstream (consumes `VendorShipmentConfirmed`) |
| `node-performance` | Supporting | `partner` | downstream (consumes the events); called synchronously for the scorecard |
| `delivery-promise`, `customer-ordering` | Core, Generic | `commerce` | no edge |
| `linehaul-transportation`, `last-mile-delivery` | Supporting | `logistics` | no edge |

Source: the classification and tier columns come from the aggregator's table; the relation column
comes from `internal/adapters/inbound/kafka/consumers.go`, `outbound/kafka/encoder.go` and
`outbound/httpclient/clients.go`, verified on [Integration](/docs/ecosystem/integration).

## Tier is not classification

`partner` groups contexts by area of the business, not by Core, Supporting or Generic: `node-performance`
and `seller-inbound` share the tier and the class, while `vendor-connectivity` is `partner` and
Supporting too. The tier decides the type prefix; the class decides how much design investment the
context deserves. Other fleet applications (the warehouse `wms`/`wes` tiers) are classified in the same
way in their own sites; the fleet's warehouse contexts are not neighbours of this one, and
`network-fulfillment` is a vendor system outside the network, not a context of it.
