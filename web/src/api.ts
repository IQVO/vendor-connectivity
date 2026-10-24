import { CONNECTIVITY_API_BASE } from "./config";
import type {
  PollQuery, PurchaseOrder, PurchaseOrderPage, Scorecard, Transaction,
  VendorAccount, VendorNode, VendorNodeList,
} from "./types";

/** RFC 9457 problem+json body every error response from vendor-connectivity
 *  returns (`type` = the problem's slug, e.g. "not-found"). */
export interface ProblemDetails {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  instance?: string;
}

/**
 * Every failed call -- an HTTP error with a problem+json body, an HTTP error
 * without one, or a network failure -- surfaces as one ApiError, so a screen
 * has a single shape to render: `title` (the problem's category) and `detail`
 * (what exactly was wrong), plus `slug` to branch on a specific problem
 * (e.g. `not-found`, `node-not-owned-by-vendor`).
 */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: ProblemDetails | null;
  readonly title: string;
  readonly detail: string;
  /** The problem's type (e.g. "not-found"); "" when unknown. */
  readonly slug: string;

  constructor(status: number, problem: ProblemDetails | null, fallbackTitle: string) {
    const title = problem?.title || fallbackTitle;
    const detail = problem?.detail ?? "";
    super(detail || title);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
    this.title = title;
    this.detail = detail;
    this.slug = problem?.type ? (problem.type.split("/").pop() ?? "") : "";
  }
}

/** Normalizes anything thrown by a request into an ApiError. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const message = err instanceof Error ? err.message : String(err);
  return new ApiError(0, { detail: message }, "Network error");
}

type Query = Record<string, string | number | undefined>;

export function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

/**
 * The one door to the service. Only this context's own API is ever called
 * (`CONNECTIVITY_API_BASE` = <apiOrigin>/api/vendor-connectivity): no sibling
 * context, no MCP server. This console is READ-ONLY: every call is a GET
 * with no custom header, so they stay simple CORS requests.
 */
async function get<T>(path: string): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${CONNECTIVITY_API_BASE}${path}`);
  } catch (err) {
    throw toApiError(err);
  }
  if (!res.ok) {
    let problem: ProblemDetails | null = null;
    try {
      problem = (await res.json()) as ProblemDetails;
    } catch {
      // non-JSON error body -- fall through with problem = null
    }
    throw new ApiError(res.status, problem, `${res.status} ${res.statusText}`.trim());
  }
  return (await res.json()) as T;
}

const seg = encodeURIComponent;

/** GET /vendor/purchase-orders -- the vendor's orders, oldest first, paged. */
export async function pollPurchaseOrders(q: PollQuery): Promise<PurchaseOrderPage> {
  const res = await get<PurchaseOrderPage>(
    withQuery("/vendor/purchase-orders", {
      vendorId: q.vendorId, nodeId: q.nodeId, createdAfter: q.createdAfter,
      nextToken: q.nextToken, limit: q.limit,
    }),
  );
  return { purchaseOrders: res.purchaseOrders ?? [], nextToken: res.nextToken || undefined };
}

/** GET /vendor/purchase-orders/{poNumber} (404 not-found, never a ship-to). */
export async function getPurchaseOrder(vendorId: string, poNumber: string): Promise<PurchaseOrder> {
  return get<PurchaseOrder>(withQuery(`/vendor/purchase-orders/${seg(poNumber)}`, { vendorId }));
}

/** GET /vendor/nodes -- the vendor's fulfilment nodes, sorted by nodeId. */
export async function listVendorNodes(vendorId: string): Promise<VendorNodeList> {
  const res = await get<VendorNodeList>(withQuery("/vendor/nodes", { vendorId }));
  return { nodes: res.nodes ?? [] };
}

/** GET /vendor/nodes/{nodeId} (node-not-owned-by-vendor for another vendor's). */
export async function getVendorNode(vendorId: string, nodeId: string): Promise<VendorNode> {
  return get<VendorNode>(withQuery(`/vendor/nodes/${seg(nodeId)}`, { vendorId }));
}

/** GET /vendor/nodes/{nodeId}/scorecard -- relayed live from node-performance. */
export async function getNodeScorecard(vendorId: string, nodeId: string): Promise<Scorecard> {
  const res = await get<Scorecard>(withQuery(`/vendor/nodes/${seg(nodeId)}/scorecard`, { vendorId }));
  return { ...res, windows: res.windows ?? [] };
}

/** GET /vendor/transactions/{transactionId} -- follow an asynchronous write. */
export async function getTransaction(vendorId: string, transactionId: string): Promise<Transaction> {
  return get<Transaction>(withQuery(`/vendor/transactions/${seg(transactionId)}`, { vendorId }));
}

/** GET /vendors/{vendorId} -- the account, its window and its state. */
export async function getVendor(vendorId: string): Promise<VendorAccount> {
  return get<VendorAccount>(`/vendors/${seg(vendorId)}`);
}
