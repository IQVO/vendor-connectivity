import { useState } from "react";
import { Card, DataTable } from "@warehouse/ui-kit";
import { getNodeScorecard, listVendorNodes } from "../api";
import { Form, FormRow, SubmitButton, TextField } from "../components/formkit";
import { Facts, NodeStatusPill, PageHeader, ScorecardWindowRow, Stack } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { idProblem, utcLabel } from "../lib";
import type { VendorNode } from "../types";

/**
 * The vendor's fulfilment nodes (the local copy of the network's
 * node-topology events, GET /vendor/nodes) and, for the selected node, its
 * rolling scorecard relayed live from node-performance
 * (GET /vendor/nodes/{nodeId}/scorecard). Read-only.
 */
export function NodesScreen() {
  const [draft, setDraft] = useState("");
  const [vendorId, setVendorId] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const nodes = useRequest(vendorId ? () => listVendorNodes(vendorId) : null, `nodes|${vendorId}`);
  const scorecard = useRequest(
    vendorId && selected ? () => getNodeScorecard(vendorId, selected) : null,
    `scorecard|${vendorId}|${selected}`,
  );

  const vendorProblem = idProblem("vendor id", draft.trim());
  const apply = () => {
    if (vendorProblem) return;
    setVendorId(draft.trim());
    setSelected(null);
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Fulfilment nodes"
        subtitle="vendor-connectivity · the nodes the network registered for one vendor, and their scorecards"
      />
      <Card>
        <Form label="Node list filters" onSubmit={apply}>
          <FormRow>
            <TextField
              label="Vendor id"
              value={draft}
              onChange={setDraft}
              placeholder="vend-south-1"
              required
              hint={vendorProblem ?? "the account every read is scoped to"}
            />
            <SubmitButton>Show nodes</SubmitButton>
          </FormRow>
        </Form>
      </Card>
      <RequestView
        state={nodes}
        what="fulfilment nodes"
        whenIdle="Enter a vendor id to list its fulfilment nodes."
        isEmpty={(d) => d.nodes.length === 0}
        empty="No fulfilment nodes registered for this vendor yet."
      >
        {(d) => (
          <DataTable<VendorNode>
            rowKey={(n) => n.nodeId}
            rows={d.nodes}
            onRowClick={(n) => setSelected(n.nodeId)}
            columns={[
              { key: "id", header: "Node", render: (n) => n.nodeId },
              { key: "status", header: "Status", render: (n) => <NodeStatusPill status={n.status} /> },
              { key: "type", header: "Type", render: (n) => (n.subtype ? `${n.nodeType} · ${n.subtype}` : n.nodeType) },
              { key: "code", header: "Vendor code", render: (n) => n.vendorNodeCode },
              { key: "region", header: "Region", render: (n) => n.regionId ?? "—" },
              { key: "tz", header: "Timezone", render: (n) => n.timezone ?? "—" },
            ]}
          />
        )}
      </RequestView>
      {selected && vendorId && (
        <Card>
          <h2 style={{ fontSize: "var(--wh-font-size-lg)", marginTop: 0 }}>Scorecard — {selected}</h2>
          <RequestView state={scorecard} what="the node's scorecard">
            {(sc) => (
              <Stack gap={4}>
                <table style={{ borderCollapse: "collapse", fontSize: "var(--wh-font-size-sm)" }}>
                  <thead>
                    <tr style={{ color: "var(--wh-color-text-muted)", textAlign: "left" }}>
                      <th style={{ padding: "4px 10px 4px 0" }}>Window · grade</th>
                      <th style={{ padding: "4px 10px 4px 0" }}>Observations</th>
                      <th style={{ padding: "4px 10px 4px 0" }}>On-time handover</th>
                      <th style={{ padding: "4px 10px 4px 0" }}>Late ack</th>
                      <th style={{ padding: "4px 10px 4px 0" }}>Rejects</th>
                      <th style={{ padding: "4px 10px 4px 0" }}>Expiry</th>
                    </tr>
                  </thead>
                  <tbody>
                    {sc.windows.map((w) => (
                      <ScorecardWindowRow key={w.window} w={w} />
                    ))}
                  </tbody>
                </table>
                <Facts
                  items={[
                    ...sc.windows.map((w) => ({ label: `${w.window} computed at`, value: utcLabel(w.computedAt) })),
                  ]}
                />
              </Stack>
            )}
          </RequestView>
        </Card>
      )}
    </Stack>
  );
}
