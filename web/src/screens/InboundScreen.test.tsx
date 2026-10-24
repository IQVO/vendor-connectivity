import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { InboundScreen } from "./InboundScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { inboundShipment } from "../test/fixtures";

function renderScreen() {
  return render(
    <MemoryRouter initialEntries={["/inbound"]}>
      <InboundScreen />
    </MemoryRouter>,
  );
}

async function askFor(vendorId: string, nodeId = "") {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Vendor id/), vendorId);
  if (nodeId) await user.type(screen.getByLabelText(/Node id/), nodeId);
  await user.click(screen.getByRole("button", { name: "Show shipments" }));
}

describe("InboundScreen", () => {
  it("stays idle until a vendor id is entered", async () => {
    mockApi({});
    renderScreen();
    expect(await screen.findByText(/Enter a vendor id to poll its inbound shipments/)).toBeInTheDocument();
  });

  it("lists the vendor's inbound shipments with their tree sizes", async () => {
    const api = mockApi({
      "GET /vendor/inbound-shipments": json({ inboundShipments: [inboundShipment()] }),
    });
    renderScreen();
    await askFor("vend-south-1");
    expect(await screen.findByText("ish-4b7e")).toBeInTheDocument();
    expect(screen.getByText("SHIPPED")).toBeInTheDocument();
    expect(screen.getByText("2026-10-23 18:30:00Z")).toBeInTheDocument();
    const call = api.to("GET /vendor/inbound-shipments")[0];
    expect(call.query.get("vendorId")).toBe("vend-south-1");
    expect(call.query.get("nodeId")).toBeNull();
  });

  it("passes the node filter through and pages with nextToken", async () => {
    const api = mockApi({
      "GET /vendor/inbound-shipments": json({ inboundShipments: [inboundShipment()], nextToken: "tok-9" }),
    });
    renderScreen();
    await askFor("vend-south-1", "node-76c2");
    await screen.findByText("ish-4b7e");
    expect(api.to("GET /vendor/inbound-shipments")[0].query.get("nodeId")).toBe("node-76c2");
    api.set("GET /vendor/inbound-shipments", json({ inboundShipments: [inboundShipment({ shipmentId: "ish-0a5c" })] }));
    (await screen.findByRole("button", { name: "Next page" })).click();
    await screen.findByText("ish-0a5c");
    expect(api.to("GET /vendor/inbound-shipments")[1].query.get("nextToken")).toBe("tok-9");
  });

  it("renders an empty page honestly", async () => {
    mockApi({ "GET /vendor/inbound-shipments": json({ inboundShipments: [] }) });
    renderScreen();
    await askFor("vend-empty");
    expect(await screen.findByText(/No inbound shipments/)).toBeInTheDocument();
  });

  it("renders a failed read as the problem it is", async () => {
    mockApi({ "GET /vendor/inbound-shipments": problem(404, "not-found", "Not found", "no such vendor") });
    renderScreen();
    await askFor("vend-404");
    expect(await screen.findByRole("alert")).toHaveTextContent("Not found");
    expect(screen.getByText("no such vendor")).toBeInTheDocument();
  });
});
