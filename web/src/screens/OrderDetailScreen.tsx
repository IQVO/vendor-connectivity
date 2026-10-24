import { Link, useParams, useSearchParams } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { getPurchaseOrder, getTransaction } from "../api";
import { Facts, PageHeader, Stack, StatePill } from "../components/layout";
import { ProblemAlert, RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { utcLabel } from "../lib";
import type { Label, PoLine } from "../types";
import { ApiError } from "../api";

/** Extracts the vendorId search parameter every detail read needs. */
function useVendorId(): string {
  return String(useSearchParams()[0].get("vendorId") ?? "");
}

/** The detail route: reads :poNumber and ?vendorId=, then the order. */
export function OrderDetailRoute() {
  const { poNumber = "" } = useParams();
  const vendorId = useVendorId();
  const order = useRequest(
    vendorId && poNumber ? () => getPurchaseOrder(vendorId, poNumber) : null,
    `order|${vendorId}|${poNumber}`,
  );

  return (
    <Stack gap={5}>
      <PageHeader
        title={`Purchase order ${poNumber}`}
        subtitle={
          <>
            as the vendor sees it — never a ship-to address ·{" "}
            <Link to={`../../orders?vendorId=${encodeURIComponent(vendorId)}`}>back to the list</Link>
          </>
        }
      />
      {!vendorId && <ProblemAlert error={new ApiError(0, { detail: "Open this order from the purchase-order list." }, "No vendor id")} />}
      <RequestView state={order} what="the purchase order">
        {(o) => (
          <Stack gap={4}>
            <Card>
              <Facts
                items={[
                  { label: "State", value: <StatePill state={o.state} /> },
                  { label: "Vendor", value: o.vendorId },
                  { label: "Node", value: o.nodeId },
                  { label: "Issued at", value: utcLabel(o.issuedAt) },
                  { label: "Required ship-by", value: utcLabel(o.requiredShipBy) },
                  { label: "Acknowledge by", value: utcLabel(o.ackBy) },
                  ...(o.shippedAt ? [{ label: "Shipped at", value: utcLabel(o.shippedAt) }] : []),
                ]}
              />
            </Card>
            <Card>
              <h2 style={{ fontSize: "var(--wh-font-size-lg)", marginTop: 0 }}>Lines</h2>
              <DataTable<PoLine>
                rowKey={(l) => String(l.lineNo)}
                rows={o.lines ?? []}
                columns={[
                  { key: "lineNo", header: "Line", align: "right", render: (l) => String(l.lineNo) },
                  { key: "listing", header: "Listing", render: (l) => l.listingId },
                  { key: "qty", header: "Quantity", align: "right", render: (l) => String(l.quantity) },
                ]}
              />
            </Card>
            <AcknowledgementCard vendorId={vendorId} transactionId={o.acknowledgement?.transactionId} />
            <Card>
              <h2 style={{ fontSize: "var(--wh-font-size-lg)", marginTop: 0 }}>Labels and tracking</h2>
              <DataTable<Label>
                rowKey={(l) => l.labelRef}
                rows={o.labels ?? []}
                emptyState="No shipping labels requested."
                columns={[
                  { key: "ref", header: "Label", render: (l) => l.labelRef },
                  { key: "carrier", header: "Carrier", render: (l) => l.carrier },
                  { key: "tracking", header: "Tracking number", render: (l) => l.trackingNumber },
                ]}
              />
            </Card>
          </Stack>
        )}
      </RequestView>
    </Stack>
  );
}

/** The acknowledgement, once its transaction succeeded, and that
 *  transaction's outcome on demand. */
function AcknowledgementCard({ vendorId, transactionId }: { vendorId: string; transactionId?: string }) {
  const tx = useRequest(
    transactionId ? () => getTransaction(vendorId, transactionId) : null,
    `tx|${vendorId}|${transactionId}`,
  );
  if (!transactionId) {
    return (
      <Card>
        <h2 style={{ fontSize: "var(--wh-font-size-lg)", marginTop: 0 }}>Acknowledgement</h2>
        <div style={{ color: "var(--wh-color-text-muted)", fontSize: "var(--wh-font-size-sm)" }}>
          Not acknowledged yet.
        </div>
      </Card>
    );
  }
  return (
    <Card>
      <h2 style={{ fontSize: "var(--wh-font-size-lg)", marginTop: 0 }}>Acknowledgement</h2>
      <RequestView state={tx} what="the acknowledgement's transaction">
        {(t) => (
          <Stack gap={3}>
            <Facts
              items={[
                { label: "Transaction", value: t.transactionId },
                { label: "Kind", value: t.kind },
                { label: "Status", value: t.status },
                { label: "Received at", value: utcLabel(t.receivedAt) },
                ...(t.processedAt ? [{ label: "Processed at", value: utcLabel(t.processedAt) }] : []),
              ]}
            />
            {t.errors?.length > 0 && (
              <div role="alert" style={{ color: "var(--wh-color-status-danger)", fontSize: "var(--wh-font-size-sm)" }}>
                {t.errors.map((e, i) => (
                  <div key={i}>{e.code}: {e.message}{e.path ? ` (${e.path})` : ""}</div>
                ))}
              </div>
            )}
          </Stack>
        )}
      </RequestView>
    </Card>
  );
}
