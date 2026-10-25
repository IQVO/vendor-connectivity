import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { OrderDetailRoute } from "./OrderDetailScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { purchaseOrder } from "../test/fixtures";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/orders/:poNumber" element={<OrderDetailRoute />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("OrderDetailScreen", () => {
  it("shows the order's windows, lines, acknowledgement and labels", async () => {
    mockApi({
      "GET /vendor/purchase-orders/PO-9A1B-0001": json(purchaseOrder({
        state: "ACCEPTED",
        acknowledgement: { decision: "ACCEPT", transactionId: "tx-3f0c9d" },
        labels: [{ labelRef: "lbl-1", trackingNumber: "TRK-88", carrier: "overnight" }],
        trackingNumbers: ["TRK-88"],
      })),
      "GET /vendor/transactions/tx-3f0c9d": json({
        transactionId: "tx-3f0c9d", vendorId: "vend-south-1", kind: "ACKNOWLEDGEMENT",
        status: "SUCCESS", receivedAt: "2026-10-22T10:00:00Z", processedAt: "2026-10-22T10:00:02Z", errors: [],
      }),
    });
    renderAt("/orders/PO-9A1B-0001?vendorId=vend-south-1");

    expect(await screen.findByRole("heading", { name: "Purchase order PO-9A1B-0001" })).toBeInTheDocument();
    expect(screen.getByText("ACCEPTED")).toBeInTheDocument();
    expect(screen.getByText("lst-000042")).toBeInTheDocument();
    // The acknowledgement's transaction is a second read: wait for it.
    expect(await screen.findByText("tx-3f0c9d")).toBeInTheDocument();
    expect(screen.getByText("SUCCESS")).toBeInTheDocument();
    expect(screen.getAllByText("TRK-88").length).toBeGreaterThan(0);
    // Never a ship-to address, an orderId or a delivery promise.
    for (const absent of ["orderId", "shipTo", "deliveryPromise"]) {
      expect(screen.queryByText(new RegExp(absent, "i"))).toBeNull();
    }
  });

  it("says the order is unacknowledged while no transaction succeeded", async () => {
    mockApi({ "GET /vendor/purchase-orders/PO-9A1B-0001": json(purchaseOrder()) });
    renderAt("/orders/PO-9A1B-0001?vendorId=vend-south-1");
    await screen.findByRole("heading", { name: "Purchase order PO-9A1B-0001" });
    expect(await screen.findByText(/Not acknowledged yet/)).toBeInTheDocument();
  });

  it("renders an upstream problem as an alert", async () => {
    mockApi({
      "GET /vendor/purchase-orders/PO-OTHER": problem(404, "not-found", "Not found", "no such purchase order"),
    });
    renderAt("/orders/PO-OTHER?vendorId=vend-south-1");
    expect(await screen.findByRole("alert")).toHaveTextContent("Not found");
  });

  it("explains itself when opened without a vendor id", async () => {
    mockApi({});
    renderAt("/orders/PO-9A1B-0001");
    expect(await screen.findByRole("alert")).toHaveTextContent(/No vendor id/);
  });
});
