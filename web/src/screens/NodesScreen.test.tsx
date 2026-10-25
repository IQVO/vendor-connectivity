import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { NodesScreen } from "./NodesScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { scorecard, vendorNode } from "../test/fixtures";

function renderScreen() {
  return render(
    <MemoryRouter initialEntries={["/nodes"]}>
      <NodesScreen />
    </MemoryRouter>,
  );
}

async function askFor(vendorId: string) {
  const user = userEvent.setup();
  await user.type(await screen.findByLabelText(/Vendor id/), vendorId);
  await user.click(screen.getByRole("button", { name: "Show nodes" }));
}

describe("NodesScreen", () => {
  it("lists the vendor's nodes with their topology facts", async () => {
    mockApi({ "GET /vendor/nodes": json({ nodes: [vendorNode(), vendorNode({ nodeId: "node-aa10", subtype: "NON_SORTABLE", status: "PROPOSED", timezone: "Europe/Berlin" })] }) });
    renderScreen();
    await askFor("vend-south-1");
    expect(await screen.findByText("node-76c2")).toBeInTheDocument();
    expect(screen.getByText("node-aa10")).toBeInTheDocument();
    expect(screen.getByText("FULFILLMENT_CENTER · SORTABLE")).toBeInTheDocument();
    expect(screen.getByText("America/Sao_Paulo")).toBeInTheDocument();
    expect(screen.getByText("PROPOSED")).toBeInTheDocument();
  });

  it("relays the selected node's scorecard", async () => {
    const api = mockApi({
      "GET /vendor/nodes": json({ nodes: [vendorNode()] }),
      "GET /vendor/nodes/node-76c2/scorecard": json(scorecard()),
    });
    renderScreen();
    await askFor("vend-south-1");
    (await screen.findByText("node-76c2")).closest("tr")!.click();
    expect(await screen.findByText("7d")).toBeInTheDocument();
    expect(screen.getByText("97.0 %")).toBeInTheDocument();
    expect(screen.getByText("2.0 %")).toBeInTheDocument();
    expect(api.to("GET /vendor/nodes/node-76c2/scorecard")[0].query.get("vendorId")).toBe("vend-south-1");
  });

  it("shows an empty note for a vendor with no nodes", async () => {
    mockApi({ "GET /vendor/nodes": json({ nodes: [] }) });
    renderScreen();
    await askFor("vend-empty");
    expect(await screen.findByText(/No fulfilment nodes registered/)).toBeInTheDocument();
  });

  it("renders a neighbour outage as the problem it is", async () => {
    mockApi({
      "GET /vendor/nodes": json({ nodes: [vendorNode()] }),
      "GET /vendor/nodes/node-76c2/scorecard": problem(503, "upstream-unavailable", "Upstream unavailable", "node-performance is not answering"),
    });
    renderScreen();
    await askFor("vend-south-1");
    (await screen.findByText("node-76c2")).closest("tr")!.click();
    expect(await screen.findByRole("alert")).toHaveTextContent("Upstream unavailable");
  });

  it("refuses a malformed vendor id before any request", async () => {
    mockApi({});
    renderScreen();
    await askFor("a b");
    expect(await screen.findByText(/A vendor id cannot contain spaces/)).toBeInTheDocument();
  });
});
