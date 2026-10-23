# 1. Vendor Connectivity is a bounded context of the retail network

Status: Accepted (bootstrap). Design source: retail-network-docs `model/model.yaml`, context `vendor-connectivity`, and its ADRs RN-0001 and RN-0007.

## Context

The retail network is modelled as ten bounded contexts, each its own service and database. The network's Open Host Service for vendors that operate fulfillment nodes. Publishes the Vendor API and its Published Language: vendors register nodes, declare their operational profile, send inventory feeds, poll purchase orders, acknowledge them in full inside the acknowledgement window, request labels and confirm shipment. Every write is an asynchronous transaction a vendor reconciles.

## Decision

- This repo owns the aggregates VendorAccount, PurchaseOrder, Submission; the invariants and error names are those of the model.
- Business decisions of the context:
- Acknowledge once, in full, before ackBy: fill-or-kill; partial acknowledgements are refused.
- ackBy = min(issuedAt + vendor ackWindow, requiredShipBy - node handlingTime).
- A label is issued only for an ACCEPTED purchase order; a shipment is confirmed only after a label.
- Every vendor write returns 202 with a transactionId; the write takes effect when its transaction succeeds.
- The Vendor API never carries ship-to PII.
- Rate limits are part of the contract (429 with Retry-After).
- Events use the network CloudEvents namespace `com.retailnetwork.partner.vendor-connectivity.<entity>.<Event>` on `retailnetwork.vendor-connectivity.events`
  (analytics-only events on `.analytics`). Published: VendorOnboarded, PurchaseOrderIssued, PurchaseOrderAccepted, PurchaseOrderRejected, PurchaseOrderExpired, PurchaseOrderCancelled, VendorShipmentConfirmed, SubmissionProcessed. No `warehouse.*` topic is consumed or produced.
- No live REST lookup into sibling contexts except the calls the model lists under `sync_calls`.
- No authentication on REST, consistent with the fleet. PII exists only in customer-ordering and shipping-and-tracking.

## Consequences

Relationships and the full consumed-event list are in the model and in the AsyncAPI contract added with the first feature PR.
