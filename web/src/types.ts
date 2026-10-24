/** Wire types of apis/openapi.yaml (camelCase, as the REST contract names them). */

export const PO_STATES = ["ISSUED", "ACK_PROCESSING", "ACCEPTED", "REJECTED", "EXPIRED", "SHIPMENT_PROCESSING", "SHIPPED", "CANCELLED"] as const;
export type PoState = (typeof PO_STATES)[number];

export const NODE_STATUSES = ["PROPOSED", "ACTIVE", "SUSPENDED", "RETIRED"] as const;
export type NodeStatus = (typeof NODE_STATUSES)[number];

export const TRANSACTION_KINDS = ["ACKNOWLEDGEMENT", "INVENTORY", "NODE_REGISTRATION", "NODE_PROFILE", "SHIPMENT_CONFIRMATION"] as const;
export type TransactionKind = (typeof TRANSACTION_KINDS)[number];

export const TRANSACTION_STATUSES = ["PROCESSING", "SUCCESS", "FAILURE"] as const;
export type TransactionStatus = (typeof TRANSACTION_STATUSES)[number];

/** One purchase order line: what to fulfil, never a ship-to address. */
export interface PoLine {
  lineNo: number;
  listingId: string;
  quantity: number;
}

/** A shipping label; it never carries the ship-to address. */
export interface Label {
  packageNo?: number;
  labelRef: string;
  trackingNumber: string;
  carrier: string;
}

/** The vendor's whole-order decision, visible once its transaction succeeded. */
export interface Acknowledgement {
  decision: "ACCEPT" | "REJECT";
  reasonCode?: string;
  transactionId: string;
}

/** A purchase order as the vendor sees it (no ship-to PII, no orderId). */
export interface PurchaseOrder {
  poNumber: string;
  vendorId: string;
  nodeId: string;
  state: PoState;
  issuedAt: string;
  requiredShipBy: string;
  ackBy: string;
  lines: PoLine[];
  acknowledgement?: Acknowledgement;
  labels: Label[];
  trackingNumbers: string[];
  shippedAt?: string;
}

/** One page of GET /vendor/purchase-orders. */
export interface PurchaseOrderPage {
  purchaseOrders: PurchaseOrder[];
  nextToken?: string;
}

/** A node as the network knows it (the local copy of node-topology events). */
export interface VendorNode {
  nodeId: string;
  vendorId: string;
  vendorNodeCode: string;
  nodeType: string;
  subtype?: string;
  status: NodeStatus;
  regionId?: string;
  timezone?: string;
  handlingTimeMinutes?: number;
  profileRevision?: number;
}

export interface VendorNodeList {
  nodes: VendorNode[];
}

/** One rolling window of a node's scorecard, relayed from node-performance. */
export interface ScorecardWindow {
  window: string;
  grade: string;
  onTimeHandoverRate?: number;
  lateAckRate?: number;
  rejectRate?: number;
  expiryRate?: number;
  promiseKeptRate?: number;
  feedFreshnessP95Minutes?: number;
  observations: number;
  computedAt: string;
}

export interface Scorecard {
  nodeId: string;
  windows: ScorecardWindow[];
}

/** One error of a failed transaction. */
export interface SubmissionError {
  code: string;
  message: string;
  path?: string;
}

/** The outcome of an asynchronous write, read by its 202 transactionId. */
export interface Transaction {
  transactionId: string;
  vendorId: string;
  kind: TransactionKind;
  status: TransactionStatus;
  receivedAt: string;
  processedAt?: string;
  errors: SubmissionError[];
  nodeId?: string;
  poNumber?: string;
}

/** The vendor account itself. */
export interface VendorAccount {
  vendorId: string;
  name: string;
  ackWindowMinutes: number;
  state: "ACTIVE" | "SUSPENDED";
}

/** Filters of the purchase-order poll. */
export interface PollQuery {
  vendorId: string;
  nodeId?: string;
  createdAfter?: string;
  nextToken?: string;
  limit?: number;
}

/** One SKU line of an inbound carton, in eaches. */
export interface ShipmentItem {
  gtin: string;
  sku?: string;
  qtyEaches: number;
  lot?: string;
  expiry?: string;
}

/** One carton of a shipment; parentSscc names its pallet when it sits on one. */
export interface ShipmentCarton {
  sscc: string;
  parentSscc?: string;
  kind: string;
  contents: ShipmentItem[];
  grossWeightG?: number;
}

/** A pallet with the cartons stacked on it. */
export interface ShipmentPallet {
  sscc: string;
  cartons: ShipmentCarton[];
}

/** The pallet-and-carton tree of one inbound shipment. */
export interface InboundShipment {
  shipmentId: string;
  sellerId?: string;
  nodeId: string;
  shippedAt: string;
  status: "SHIPPED" | "RECEIVED";
  pallets: ShipmentPallet[];
  cartons: ShipmentCarton[];
}

/** One page of GET /vendor/inbound-shipments. */
export interface InboundShipmentPage {
  inboundShipments: InboundShipment[];
  nextToken?: string;
}

/** Filters of the inbound-shipment poll. */
export interface InboundPollQuery {
  vendorId: string;
  nodeId?: string;
  createdAfter?: string;
  nextToken?: string;
  limit?: number;
}
