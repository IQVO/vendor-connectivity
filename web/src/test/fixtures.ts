import type { InboundShipment, PurchaseOrder, Scorecard, VendorAccount, VendorNode } from "../types";

export function purchaseOrder(over: Partial<PurchaseOrder> = {}): PurchaseOrder {
  return {
    poNumber: "PO-9A1B-0001",
    vendorId: "vend-south-1",
    nodeId: "node-76c2",
    state: "ISSUED",
    issuedAt: "2026-10-22T09:00:00Z",
    requiredShipBy: "2026-10-23T17:00:00Z",
    ackBy: "2026-10-22T13:00:00Z",
    lines: [
      { lineNo: 1, listingId: "lst-000042", quantity: 2 },
      { lineNo: 2, listingId: "lst-000077", quantity: 5 },
    ],
    labels: [],
    trackingNumbers: [],
    ...over,
  };
}

export function vendorNode(over: Partial<VendorNode> = {}): VendorNode {
  return {
    nodeId: "node-76c2",
    vendorId: "vend-south-1",
    vendorNodeCode: "VC-SOUTH-01",
    nodeType: "FULFILLMENT_CENTER",
    subtype: "SORTABLE",
    status: "ACTIVE",
    regionId: "r-south",
    timezone: "America/Sao_Paulo",
    handlingTimeMinutes: 120,
    profileRevision: 3,
    ...over,
  };
}

export function scorecard(over: Partial<Scorecard> = {}): Scorecard {
  return {
    nodeId: "node-76c2",
    windows: [
      {
        window: "7d", grade: "A", observations: 120, computedAt: "2026-10-24T08:00:00Z",
        onTimeHandoverRate: 0.97, lateAckRate: 0.02, rejectRate: 0.01, expiryRate: 0.005, promiseKeptRate: 0.96,
        feedFreshnessP95Minutes: 4.5,
      },
      { window: "30d", grade: "B", observations: 512, computedAt: "2026-10-24T08:00:00Z", onTimeHandoverRate: 0.91 },
    ],
    ...over,
  };
}

export function vendor(over: Partial<VendorAccount> = {}): VendorAccount {
  return {
    vendorId: "vend-south-1",
    name: "North Fulfilment",
    ackWindowMinutes: 240,
    state: "ACTIVE",
    ...over,
  };
}

export function inboundShipment(over: Partial<InboundShipment> = {}): InboundShipment {
  return {
    shipmentId: "ish-4b7e",
    sellerId: "S-1",
    nodeId: "node-76c2",
    shippedAt: "2026-10-23T18:30:00Z",
    status: "SHIPPED",
    pallets: [
      {
        sscc: "006141410000000036",
        cartons: [
          {
            sscc: "006141410000000012", parentSscc: "006141410000000036", kind: "HOMOGENEOUS_CASE",
            grossWeightG: 900, contents: [{ gtin: "00614141000036", qtyEaches: 12 }],
          },
        ],
      },
    ],
    cartons: [
      { sscc: "006141410000000029", kind: "MIXED", contents: [{ gtin: "00614141000036", qtyEaches: 2 }] },
    ],
    ...over,
  };
}
