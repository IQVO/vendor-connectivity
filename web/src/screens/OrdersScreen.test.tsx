import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { OrdersScreen } from "./OrdersScreen";
import { json, mockApi, pending, problem } from "../test/fetchMock";
import { purchaseOrder } from "../test/fixtures";

function renderScreen() {
  return render(
    <MemoryRouter initialEntries={["/orders"]}>
      <OrdersScreen />
    </MemoryRouter>,
  );
}

async function askFor(vendorId: string, nodeId = "") {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Vendor id/), vendorId);
  if (nodeId) await user.type(screen.getByLabelText(/Node id/), nodeId);
  await user.click(screen.getByRole("button", { name: "Show orders" }));
}

describe("OrdersScreen", () => {
  it("stays idle until a vendor id is entered", async () => {
    mockApi({});
    renderScreen();
    expect(await screen.findByText(/Enter a vendor id to poll its purchase orders/)).toBeInTheDocument();
  });

  it("refuses a malformed vendor id before any request", async () => {
    mockApi({});
    renderScreen();
    await askFor("a/b");
    expect(await screen.findByText(/A vendor id cannot contain spaces/)).toBeInTheDocument();
  });

  it("shows the vendor's orders, oldest first, with their windows", async () => {
    const api = mockApi({
      "GET /vendor/purchase-orders": json({ purchaseOrders: [purchaseOrder({ state: "ACCEPTED" })] }),
    });
    renderScreen();
    await askFor("vend-south-1");
    expect(await screen.findByText("PO-9A1B-0001")).toBeInTheDocument();
    expect(screen.getByText("ACCEPTED")).toBeInTheDocument();
    expect(screen.getByText("2026-10-22 09:00:00Z")).toBeInTheDocument();
    expect(screen.getByText("2026-10-23 17:00:00Z")).toBeInTheDocument();
    const call = api.to("GET /vendor/purchase-orders")[0];
    expect(call.query.get("vendorId")).toBe("vend-south-1");
    expect(call.query.get("nodeId")).toBeNull();
  });

  it("passes the node filter through", async () => {
    const api = mockApi({ "GET /vendor/purchase-orders": json({ purchaseOrders: [] }) });
    renderScreen();
    await askFor("vend-south-1", "node-76c2");
    await screen.findByText(/No purchase orders/);
    expect(api.to("GET /vendor/purchase-orders")[0].query.get("nodeId")).toBe("node-76c2");
  });

  it("pages with the opaque nextToken", async () => {
    const api = mockApi({ "GET /vendor/purchase-orders": json({ purchaseOrders: [purchaseOrder()], nextToken: "tok-2" }) });
    renderScreen();
    await askFor("vend-south-1");
    const next = await screen.findByRole("button", { name: "Next page" });
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();
    api.set("GET /vendor/purchase-orders", json({ purchaseOrders: [purchaseOrder({ poNumber: "PO-9A1B-0002" })] }));
    (next as HTMLElement).click();
    await screen.findByText("PO-9A1B-0002");
    expect(api.to("GET /vendor/purchase-orders")[1].query.get("nextToken")).toBe("tok-2");
    expect(screen.getByRole("button", { name: "Previous page" })).toBeEnabled();
  });

  it("renders the problem's title and detail on a failed read", async () => {
    mockApi({ "GET /vendor/purchase-orders": problem(404, "not-found", "Not found", "no such vendor") });
    renderScreen();
    await askFor("vend-404");
    expect(await screen.findByRole("alert")).toHaveTextContent("Not found");
    expect(screen.getByText("no such vendor")).toBeInTheDocument();
  });

  it("renders the loading state without a test id race", async () => {
    mockApi({ "GET /vendor/purchase-orders": pending() });
    renderScreen();
    await askFor("vend-south-1");
    expect(await screen.findByRole("status")).toHaveTextContent(/Loading purchase orders/);
  });
});
