import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { ApiError, getInboundShipment, pollInboundShipments } from "../api";
import { Form, FormRow, SubmitButton, TextField } from "../components/formkit";
import { Facts, PageHeader, Stack } from "../components/layout";
import { ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { idProblem, optionalIdProblem, utcLabel } from "../lib";
import type { InboundShipment, ShipmentCarton } from "../types";

interface Filters {
  vendorId: string;
  nodeId: string;
}

const INITIAL: Filters = { vendorId: "", nodeId: "" };

/**
 * The inbound shipments bound for one vendor's nodes (GET
 * /vendor/inbound-shipments, oldest first, paged with the opaque nextToken),
 * and one shipment's pallet-and-carton tree on demand (GET
 * /vendor/inbound-shipments/{shipmentId}). Read-only: recording a receipt is
 * a vendor-system write, not something this console does.
 */
export function InboundScreen() {
  const [draft, setDraft] = useState<Filters>(INITIAL);
  const [applied, setApplied] = useState<Filters | null>(null);
  const [tokens, setTokens] = useState<string[]>([""]);
  const token = tokens[tokens.length - 1];
  const page = useRequest(
    applied ? () => pollInboundShipments({
      vendorId: applied.vendorId, nodeId: applied.nodeId || undefined, limit: 25, nextToken: token || undefined,
    }) : null,
    `inbound|${applied?.vendorId}|${applied?.nodeId}|${token}`,
  );

  const vendorProblem = idProblem("vendor id", draft.vendorId.trim());
  const nodeProblem = optionalIdProblem("node id", draft.nodeId.trim());
  const apply = () => {
    if (vendorProblem || nodeProblem) return;
    setApplied({ vendorId: draft.vendorId.trim(), nodeId: draft.nodeId.trim() });
    setTokens([""]);
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Inbound shipments"
        subtitle="vendor-connectivity · the shipments bound for the vendor's nodes, oldest first"
      />
      <Card>
        <Form label="Inbound filters" onSubmit={apply}>
          <FormRow>
            <TextField
              label="Vendor id"
              value={draft.vendorId}
              onChange={(v) => setDraft((f) => ({ ...f, vendorId: v }))}
              placeholder="vend-south-1"
              required
              hint={vendorProblem ?? "the account every read is scoped to"}
            />
            <TextField
              label="Node id"
              value={draft.nodeId}
              onChange={(v) => setDraft((f) => ({ ...f, nodeId: v }))}
              placeholder="any node"
              hint={nodeProblem ?? "keep only shipments bound for this node"}
            />
            <SubmitButton>Show shipments</SubmitButton>
          </FormRow>
        </Form>
      </Card>
      <RequestView
        state={page}
        what="inbound shipments"
        whenIdle="Enter a vendor id to poll its inbound shipments."
        isEmpty={(d) => d.inboundShipments.length === 0}
        empty="No inbound shipments for this vendor (and node filter)."
      >
        {(d) => (
          <Stack>
            <DataTable<InboundShipment>
              rowKey={(s) => s.shipmentId}
              rows={d.inboundShipments}
              columns={[
                {
                  key: "id",
                  header: "Shipment",
                  render: (s) => (
                    <Link to={`../inbound/${encodeURIComponent(s.shipmentId)}?vendorId=${encodeURIComponent(applied?.vendorId ?? "")}`}>
                      {s.shipmentId}
                    </Link>
                  ),
                },
                { key: "status", header: "Status", render: (s) => s.status },
                { key: "node", header: "Node", render: (s) => s.nodeId },
                { key: "seller", header: "Seller", render: (s) => s.sellerId ?? "—" },
                { key: "shipped", header: "Shipped", render: (s) => utcLabel(s.shippedAt) },
                { key: "pallets", header: "Pallets", align: "right", render: (s) => String(s.pallets?.length ?? 0) },
                { key: "cartons", header: "Top-level cartons", align: "right", render: (s) => String(s.cartons?.length ?? 0) },
              ]}
            />
            <div style={{ display: "flex", gap: "var(--wh-space-2)" }}>
              <button type="button" disabled={tokens.length < 2} onClick={() => setTokens((t) => t.slice(0, -1))}>
                Previous page
              </button>
              <button
                type="button"
                disabled={!d.nextToken}
                onClick={() => d.nextToken && setTokens((t) => [...t, d.nextToken as string])}
              >
                Next page
              </button>
            </div>
          </Stack>
        )}
      </RequestView>
    </Stack>
  );
}

/** The detail route: reads :shipmentId and ?vendorId=, then the tree. */
export function InboundShipmentRoute() {
  const { shipmentId = "" } = useParams();
  const vendorId = String(useSearchParams()[0].get("vendorId") ?? "");
  const shipment = useRequest(
    vendorId && shipmentId ? () => getInboundShipment(vendorId, shipmentId) : null,
    `shipment|${vendorId}|${shipmentId}`,
  );

  return (
    <Stack gap={5}>
      <PageHeader
        title={`Inbound shipment ${shipmentId}`}
        subtitle={
          <>
            the pallet-and-carton tree ·{" "}
            <Link to={`../../inbound?vendorId=${encodeURIComponent(vendorId)}`}>back to the list</Link>
          </>
        }
      />
      {!vendorId && <ProblemAlert error={new ApiError(0, { detail: "Open this shipment from the inbound list." }, "No vendor id")} />}
      <RequestView state={shipment} what="the inbound shipment">
        {(s) => (
          <Stack gap={4}>
            <Card>
              <Facts
                items={[
                  { label: "Status", value: s.status },
                  { label: "Node", value: s.nodeId },
                  { label: "Seller", value: s.sellerId ?? "—" },
                  { label: "Shipped at", value: utcLabel(s.shippedAt) },
                ]}
              />
            </Card>
            {s.pallets?.map((p) => (
              <Card key={p.sscc}>
                <h2 style={{ fontSize: "var(--wh-font-size-lg)", marginTop: 0 }}>Pallet {p.sscc}</h2>
                <CartonTable cartons={p.cartons} />
              </Card>
            ))}
            {s.cartons?.length > 0 && (
              <Card>
                <h2 style={{ fontSize: "var(--wh-font-size-lg)", marginTop: 0 }}>Top-level cartons</h2>
                <CartonTable cartons={s.cartons} />
              </Card>
            )}
          </Stack>
        )}
      </RequestView>
    </Stack>
  );
}

function CartonTable({ cartons }: { cartons: ShipmentCarton[] }) {
  return (
    <DataTable<ShipmentCarton>
      rowKey={(c) => c.sscc}
      rows={cartons}
      columns={[
        { key: "sscc", header: "Carton", render: (c) => c.sscc },
        { key: "kind", header: "Kind", render: (c) => c.kind },
        { key: "items", header: "Item lines", align: "right", render: (c) => String(c.contents?.length ?? 0) },
        { key: "eaches", header: "Eaches", align: "right", render: (c) => String((c.contents ?? []).reduce((n, i) => n + i.qtyEaches, 0)) },
        { key: "weight", header: "Gross weight", align: "right", render: (c) => (c.grossWeightG ? `${c.grossWeightG} g` : "—") },
      ]}
    />
  );
}
