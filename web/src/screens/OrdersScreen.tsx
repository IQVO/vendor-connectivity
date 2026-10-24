import { useState } from "react";
import { Link } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { pollPurchaseOrders } from "../api";
import { Form, FormRow, SubmitButton, TextField } from "../components/formkit";
import { PageHeader, Stack, StatePill } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { idProblem, optionalIdProblem, utcLabel } from "../lib";
import type { PurchaseOrder } from "../types";

interface Filters {
  vendorId: string;
  nodeId: string;
}

const INITIAL: Filters = { vendorId: "", nodeId: "" };

/**
 * The purchase orders released to one vendor: GET /vendor/purchase-orders,
 * oldest first, a page at a time. Paging follows the opaque `nextToken`;
 * the tokens already visited are kept so "Previous" walks back without the
 * API having to support it. Narrowing to one node restarts at the first
 * page. A read-only screen: acknowledging an order is a REST write of the
 * vendor's own system, not something this console does.
 */
export function OrdersScreen() {
  const [draft, setDraft] = useState<Filters>(INITIAL);
  const [applied, setApplied] = useState<Filters | null>(null);
  // tokens[i] is the nextToken of page i+1 ("" = the first page).
  const [tokens, setTokens] = useState<string[]>([""]);
  const token = tokens[tokens.length - 1];
  const page = useRequest(
    applied ? () => pollPurchaseOrders({
      vendorId: applied.vendorId, nodeId: applied.nodeId || undefined, limit: 25, nextToken: token || undefined,
    }) : null,
    `orders|${applied?.vendorId}|${applied?.nodeId}|${token}`,
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
        title="Purchase orders"
        subtitle="vendor-connectivity · the orders the network released to one vendor, oldest first"
      />
      <Card>
        <Form label="Purchase order filters" onSubmit={apply}>
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
              hint={nodeProblem ?? "keep only orders released to this node"}
            />
            <SubmitButton>Show orders</SubmitButton>
          </FormRow>
        </Form>
      </Card>
      <RequestView
        state={page}
        what="purchase orders"
        whenIdle="Enter a vendor id to poll its purchase orders."
        isEmpty={(d) => d.purchaseOrders.length === 0}
        empty="No purchase orders for this vendor (and node filter)."
      >
        {(d) => (
          <Stack>
            <DataTable<PurchaseOrder>
              rowKey={(o) => o.poNumber}
              rows={d.purchaseOrders}
              columns={[
                {
                  key: "po",
                  header: "PO number",
                  render: (o) => (
                    <Link to={`../orders/${encodeURIComponent(o.poNumber)}?vendorId=${encodeURIComponent(applied?.vendorId ?? "")}`}>
                      {o.poNumber}
                    </Link>
                  ),
                },
                { key: "state", header: "State", render: (o) => <StatePill state={o.state} /> },
                { key: "node", header: "Node", render: (o) => o.nodeId },
                { key: "issued", header: "Issued", render: (o) => utcLabel(o.issuedAt) },
                { key: "shipBy", header: "Required ship-by", render: (o) => utcLabel(o.requiredShipBy) },
                { key: "ackBy", header: "Acknowledge by", render: (o) => utcLabel(o.ackBy) },
                { key: "lines", header: "Lines", align: "right", render: (o) => String(o.lines?.length ?? 0) },
              ]}
            />
            <div style={{ display: "flex", gap: "var(--wh-space-2)" }}>
              <button
                type="button"
                disabled={tokens.length < 2}
                onClick={() => setTokens((t) => t.slice(0, -1))}
              >
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
