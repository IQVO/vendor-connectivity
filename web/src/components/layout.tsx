import type { ReactNode } from "react";
import { StatusPill } from "@warehouse/ui-kit";
import type { NodeStatus, PoState, ScorecardWindow } from "../types";

export function PageHeader({ title, subtitle }: { title: string; subtitle: ReactNode }) {
  return (
    <div>
      <h1 style={{ fontSize: "var(--wh-font-size-2xl)", margin: 0 }}>{title}</h1>
      <p style={{ color: "var(--wh-color-text-muted)", marginTop: 4 }}>{subtitle}</p>
    </div>
  );
}

export function Stack({ children, gap = 4 }: { children: ReactNode; gap?: 3 | 4 | 5 }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: `var(--wh-space-${gap})` }}>
      {children}
    </div>
  );
}

/** A label/value list (dl) for a record's scalar fields. */
export function Facts({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl
      style={{
        display: "grid",
        gridTemplateColumns: "max-content 1fr",
        gap: "var(--wh-space-2) var(--wh-space-4)",
        margin: 0,
        fontSize: "var(--wh-font-size-sm)",
      }}
    >
      {items.map((item) => (
        <div key={item.label} style={{ display: "contents" }}>
          <dt style={{ color: "var(--wh-color-text-muted)" }}>{item.label}</dt>
          <dd style={{ margin: 0 }}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

type Tone = "neutral" | "progress" | "success" | "warning" | "danger";

/** The purchase-order lifecycle is vendor-connectivity's own, so StatusPill's
 *  tone override carries it. */
const STATE_TONE: Record<PoState, Tone> = {
  ISSUED: "progress",
  ACK_PROCESSING: "progress",
  ACCEPTED: "success",
  REJECTED: "danger",
  EXPIRED: "warning",
  SHIPMENT_PROCESSING: "progress",
  SHIPPED: "success",
  CANCELLED: "neutral",
};

export function StatePill({ state }: { state: PoState }) {
  return <StatusPill status={state} tone={STATE_TONE[state]} size="sm" />;
}

const NODE_TONE: Record<NodeStatus, Tone> = {
  PROPOSED: "neutral",
  ACTIVE: "success",
  SUSPENDED: "warning",
  RETIRED: "neutral",
};

export function NodeStatusPill({ status }: { status: NodeStatus }) {
  return <StatusPill status={status} tone={NODE_TONE[status]} size="sm" />;
}

/** One rolling window of a node's scorecard. */
export function ScorecardWindowRow({ w }: { w: ScorecardWindow }) {
  return (
    <tr>
      <td style={{ padding: "4px 10px 4px 0" }}>
        <strong>{w.window}</strong> · {w.grade}
      </td>
      <td style={{ padding: "4px 10px 4px 0" }}>{w.observations}</td>
      <td style={{ padding: "4px 10px 4px 0" }}>{percentLabel(w.onTimeHandoverRate)}</td>
      <td style={{ padding: "4px 10px 4px 0" }}>{percentLabel(w.lateAckRate)}</td>
      <td style={{ padding: "4px 10px 4px 0" }}>{percentLabel(w.rejectRate)}</td>
      <td style={{ padding: "4px 10px 4px 0" }}>{percentLabel(w.expiryRate)}</td>
    </tr>
  );
}

function percentLabel(rate: number | undefined): string {
  if (rate === undefined) return "—";
  return `${(rate * 100).toFixed(1)} %`;
}
