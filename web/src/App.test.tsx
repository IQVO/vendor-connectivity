import { MemoryRouter, Route, Routes } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import App from "./App";
import { json, mockApi } from "./test/fetchMock";
import { purchaseOrder, vendorNode } from "./test/fixtures";

// The console mounts the remote under its own splat route; the remote's
// relative links must resolve against the mount point, not the full URL.
function renderUnderHost(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/vendor-connectivity/*" element={<App />} />
      </Routes>
    </MemoryRouter>,
  );
}

const ROUTES = {
  "GET /vendor/purchase-orders": json({ purchaseOrders: [purchaseOrder()] }),
  "GET /vendor/purchase-orders/PO-9A1B-0001": json(purchaseOrder()),
  "GET /vendor/nodes": json({ nodes: [vendorNode()] }),
};

describe("App", () => {
  it("redirects the mount point to the purchase-order list", async () => {
    mockApi(ROUTES);
    renderUnderHost("/vendor-connectivity");
    expect(await screen.findByRole("heading", { name: "Purchase orders" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Purchase orders" })).toHaveAttribute(
      "href", "/vendor-connectivity/orders",
    );
  });

  it("keeps the sub-nav at the mount point from a deep route", async () => {
    mockApi(ROUTES);
    renderUnderHost("/vendor-connectivity/orders/PO-9A1B-0001");
    await screen.findByRole("heading", { name: "Purchase order PO-9A1B-0001" });
    expect(screen.getByRole("link", { name: "Nodes" })).toHaveAttribute("href", "/vendor-connectivity/nodes");
  });

  it("navigates from the list into an order and over to the nodes", async () => {
    mockApi(ROUTES);
    renderUnderHost("/vendor-connectivity/orders");
    const user = userEvent.setup();

    await user.type(await screen.findByLabelText(/Vendor id/), "vend-south-1");
    await user.click(screen.getByRole("button", { name: "Show orders" }));
    await user.click(await screen.findByRole("link", { name: "PO-9A1B-0001" }));
    await screen.findByRole("heading", { name: "Purchase order PO-9A1B-0001" });
    expect(await screen.findByText(/as the vendor sees it/)).toBeInTheDocument();

    await user.click(screen.getByRole("link", { name: "Nodes" }));
    await user.type(await screen.findByLabelText(/Vendor id/), "vend-south-1");
    await user.click(screen.getByRole("button", { name: "Show nodes" }));
    expect(await screen.findByText("node-76c2")).toBeInTheDocument();
  });
});
