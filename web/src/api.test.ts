import { describe, expect, it } from "vitest";
import { CONNECTIVITY_API_BASE } from "./config";
import { getPurchaseOrder, getNodeScorecard, getVendor, getVendorNode, listVendorNodes, pollPurchaseOrders, toApiError, withQuery } from "./api";
import { json, mockApi, problem } from "./test/fetchMock";
import { purchaseOrder, scorecard, vendor, vendorNode } from "./test/fixtures";

describe("the read surface", () => {
  it("polls purchase orders with the filters in the query string", async () => {
    const api = mockApi({
      [`GET /vendor/purchase-orders`]: json({ purchaseOrders: [purchaseOrder()], nextToken: "tok-2" }),
    });
    const page = await pollPurchaseOrders({ vendorId: "vend-south-1", nodeId: "node-76c2", limit: 25 });
    expect(page.purchaseOrders[0].poNumber).toBe("PO-9A1B-0001");
    expect(page.nextToken).toBe("tok-2");
    const call = api.to("GET /vendor/purchase-orders")[0];
    expect(call.query.get("vendorId")).toBe("vend-south-1");
    expect(call.query.get("nodeId")).toBe("node-76c2");
    expect(call.query.get("limit")).toBe("25");
    expect(call.url.startsWith(`${CONNECTIVITY_API_BASE}${"/vendor/purchase-orders"}`)).toBe(true);
  });

  it("normalizes a missing array and a blank token", async () => {
    mockApi({ "GET /vendor/purchase-orders": json({}) });
    const page = await pollPurchaseOrders({ vendorId: "vend-south-1" });
    expect(page.purchaseOrders).toEqual([]);
    expect(page.nextToken).toBeUndefined();
  });

  it("reads one purchase order carrying the vendorId", async () => {
    const api = mockApi({ "GET /vendor/purchase-orders/PO-9A1B-0001": json(purchaseOrder()) });
    const po = await getPurchaseOrder("vend-south-1", "PO-9A1B-0001");
    expect(po.state).toBe("ISSUED");
    expect(api.to("GET /vendor/purchase-orders/PO-9A1B-0001")[0].query.get("vendorId")).toBe("vend-south-1");
  });

  it("lists the vendor's nodes and reads one", async () => {
    const api = mockApi({
      "GET /vendor/nodes": json({ nodes: [vendorNode()] }),
      "GET /vendor/nodes/node-76c2": json(vendorNode()),
    });
    const list = await listVendorNodes("vend-south-1");
    expect(list.nodes[0].nodeId).toBe("node-76c2");
    const one = await getVendorNode("vend-south-1", "node-76c2");
    expect(one.timezone).toBe("America/Sao_Paulo");
    expect(api.to("GET /vendor/nodes")[0].query.get("vendorId")).toBe("vend-south-1");
  });

  it("relays a node scorecard and always keeps the windows array", async () => {
    mockApi({ "GET /vendor/nodes/node-76c2/scorecard": json(scorecard()) });
    const sc = await getNodeScorecard("vend-south-1", "node-76c2");
    expect(sc.windows.map((w) => w.window)).toEqual(["7d", "30d"]);
    mockApi({ "GET /vendor/nodes/node-76c2/scorecard": json({ nodeId: "node-76c2" }) });
    expect((await getNodeScorecard("vend-south-1", "node-76c2")).windows).toEqual([]);
  });

  it("reads the vendor account by path parameter", async () => {
    const api = mockApi({ "GET /vendors/vend-south-1": json(vendor()) });
    const v = await getVendor("vend-south-1");
    expect(v.ackWindowMinutes).toBe(240);
    expect(api.to("GET /vendors/vend-south-1")).toHaveLength(1);
  });
});

describe("errors", () => {
  it("surfaces the problem's title, detail and slug", async () => {
    mockApi({ "GET /vendors/vend-404": problem(404, "not-found", "Not found", "no such vendor") });
    const err = await getVendor("vend-404").catch((e) => e);
    expect(err.slug).toBe("not-found");
    expect(err.status).toBe(404);
    expect(err.title).toBe("Not found");
    expect(err.detail).toBe("no such vendor");
  });

  it("keeps the fallback title when the body is not a problem", async () => {
    mockApi({ "GET /vendors/vend-south-1": new Response("nope", { status: 502 }) });
    const err = await getVendor("vend-south-1").catch((e) => e);
    expect(err.status).toBe(502);
    expect(err.title).toBe("502");
  });

  it("wraps a network failure", () => {
    const err = toApiError(new TypeError("fetch failed"));
    expect(err.status).toBe(0);
    expect(err.title).toBe("Network error");
  });
});

describe("withQuery", () => {
  it("drops empty values and builds the query string", () => {
    expect(withQuery("/x", { a: "1", b: "", c: undefined, d: 2 })).toBe("/x?a=1&d=2");
    expect(withQuery("/x")).toBe("/x");
  });

});
