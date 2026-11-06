import { describe, expect, it } from "vitest";
import { CONNECTIVITY_API_BASE } from "./config";
import { getPurchaseOrder, getNodeScorecard, getVendor, getVendorNode, listVendorNodes, pollInboundShipments, pollPurchaseOrders, toApiError, withQuery } from "./api";
import { json, mockApi, problem } from "./test/fetchMock";
import { inboundShipment, purchaseOrder, scorecard, vendor, vendorNode } from "./test/fixtures";

describe("the read surface", () => {
  it("polls purchase orders with the filters in the query string", async () => {
    const api = mockApi({
      [`GET /vendor/purchase-orders`]: json({ items: [purchaseOrder()], nextCursor: "tok-2" }),
    });
    const page = await pollPurchaseOrders({ vendorId: "vend-south-1", nodeId: "node-76c2", limit: 25 });
    expect(page.items[0].poNumber).toBe("PO-9A1B-0001");
    expect(page.nextCursor).toBe("tok-2");
    const call = api.to("GET /vendor/purchase-orders")[0];
    expect(call.query.get("vendorId")).toBe("vend-south-1");
    expect(call.query.get("nodeId")).toBe("node-76c2");
    expect(call.query.get("limit")).toBe("25");
    expect(call.url.startsWith(`${CONNECTIVITY_API_BASE}${"/vendor/purchase-orders"}`)).toBe(true);
  });

  it("sends the continuation as the `cursor` query parameter (nextToken is 400 unknown-query-parameter)", async () => {
    const api = mockApi({ "GET /vendor/purchase-orders": json({ items: [] }) });
    await pollPurchaseOrders({ vendorId: "vend-south-1", cursor: "tok-2" });
    const call = api.to("GET /vendor/purchase-orders")[0];
    expect(call.query.get("cursor")).toBe("tok-2");
    expect(call.query.has("nextToken")).toBe(false);
  });

  it("reads an empty last page and a blank cursor", async () => {
    mockApi({ "GET /vendor/purchase-orders": json({ items: [], nextCursor: "" }) });
    const page = await pollPurchaseOrders({ vendorId: "vend-south-1" });
    expect(page.items).toEqual([]);
    expect(page.nextCursor).toBeUndefined();
  });

  it.each([
    ["a 200 with the retired purchaseOrders key", { purchaseOrders: [{ poNumber: "PO-1" }], nextToken: "t" }],
    ["a bare array", [{ poNumber: "PO-1" }]],
    ["items: null", { items: null }],
    ["an empty object", {}],
  ])("refuses %s instead of showing an empty list", async (_n, body) => {
    mockApi({ "GET /vendor/purchase-orders": json(body) });
    const err = await pollPurchaseOrders({ vendorId: "vend-south-1" }).catch((e) => e);
    expect(err).toBeInstanceOf(Error);
    expect(err.title).toMatch(/page/i);
  });

  it("polls inbound shipments from {items,nextCursor} with `cursor`", async () => {
    const api = mockApi({
      "GET /vendor/inbound-shipments": json({ items: [inboundShipment()], nextCursor: "c-2" }),
    });
    const page = await pollInboundShipments({ vendorId: "vend-south-1", cursor: "c-1", limit: 25 });
    expect(page.items[0].shipmentId).toBe(inboundShipment().shipmentId);
    expect(page.nextCursor).toBe("c-2");
    const call = api.to("GET /vendor/inbound-shipments")[0];
    expect(call.query.get("cursor")).toBe("c-1");
    expect(call.query.has("nextToken")).toBe(false);
  });

  it("refuses an inbound-shipments 200 that is not a page", async () => {
    mockApi({ "GET /vendor/inbound-shipments": json({ inboundShipments: [inboundShipment()] }) });
    const err = await pollInboundShipments({ vendorId: "vend-south-1" }).catch((e) => e);
    expect(err.title).toMatch(/page/i);
  });

  it("reads one purchase order carrying the vendorId", async () => {
    const api = mockApi({ "GET /vendor/purchase-orders/PO-9A1B-0001": json(purchaseOrder()) });
    const po = await getPurchaseOrder("vend-south-1", "PO-9A1B-0001");
    expect(po.state).toBe("ISSUED");
    expect(api.to("GET /vendor/purchase-orders/PO-9A1B-0001")[0].query.get("vendorId")).toBe("vend-south-1");
  });

  it("lists the vendor's nodes and reads one", async () => {
    const api = mockApi({
      "GET /vendor/nodes": json({ items: [vendorNode()] }),
      "GET /vendor/nodes/node-76c2": json(vendorNode()),
    });
    const list = await listVendorNodes("vend-south-1");
    expect(list.items[0].nodeId).toBe("node-76c2");
    const one = await getVendorNode("vend-south-1", "node-76c2");
    expect(one.timezone).toBe("America/Sao_Paulo");
    expect(api.to("GET /vendor/nodes")[0].query.get("vendorId")).toBe("vend-south-1");
  });

  it("lists every node, following nextCursor to the last page", async () => {
    const api = mockApi({
      "GET /vendor/nodes": (call) =>
        call.query.get("cursor") === "n-2"
          ? json({ items: [vendorNode({ nodeId: "node-c" })] })
          : call.query.get("cursor") === "n-1"
            ? json({ items: [vendorNode({ nodeId: "node-b" })], nextCursor: "n-2" })
            : json({ items: [vendorNode({ nodeId: "node-a" })], nextCursor: "n-1" }),
    });
    const list = await listVendorNodes("vend-south-1");
    expect(list.items.map((n) => n.nodeId)).toEqual(["node-a", "node-b", "node-c"]);
    expect(api.to("GET /vendor/nodes")).toHaveLength(3);
    expect(api.to("GET /vendor/nodes")[2].query.get("vendorId")).toBe("vend-south-1");
  });

  it("refuses a /vendor/nodes 200 with the retired nodes key", async () => {
    mockApi({ "GET /vendor/nodes": json({ nodes: [vendorNode()] }) });
    const err = await listVendorNodes("vend-south-1").catch((e) => e);
    expect(err.title).toMatch(/page/i);
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
