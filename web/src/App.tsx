import { Navigate, NavLink, Outlet, Route, Routes } from "react-router-dom";
import { OrderDetailRoute } from "./screens/OrderDetailScreen";
import { OrdersScreen } from "./screens/OrdersScreen";
import { NodesScreen } from "./screens/NodesScreen";

const SUB_NAV = [
  { to: "orders", label: "Purchase orders" },
  { to: "nodes", label: "Nodes" },
];

const linkStyle = ({ isActive }: { isActive: boolean }) => ({
  display: "inline-flex",
  padding: "6px 12px",
  borderRadius: "var(--wh-radius-pill)",
  fontSize: "var(--wh-font-size-sm)",
  fontWeight: isActive ? 600 : 500,
  color: isActive ? "var(--wh-color-text)" : "var(--wh-color-text-muted)",
  background: isActive ? "var(--wh-color-accent-muted)" : "transparent",
  textDecoration: "none",
});

/** The sub-nav, rendered by a layout route with the path "/".
 *
 *  The console mounts this component inside its own `<Route
 *  path="/vendor-connectivity/*">`. A relative `<NavLink>` rendered straight in
 *  that splat route resolves against the FULL current URL (react-router 7 uses
 *  the last path-contributing match's pathname, splat included), so from
 *  /vendor-connectivity/nodes the link `orders` would point at
 *  /vendor-connectivity/nodes/orders. A pathless layout route does not help
 *  (react-router drops pathless routes from relative resolution); a layout
 *  route WITH path "/" does: its own match is the mount point, under any
 *  prefix and standalone at `/`. (Same fix as every other remote in the
 *  fleet; App.test.tsx mounts under the host's splat route to prove it.) */
function ConnectivityLayout() {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--wh-space-5)" }}>
      <nav aria-label="Vendor connectivity sections" style={{ display: "flex", gap: "var(--wh-space-2)" }}>
        {SUB_NAV.map((item) => (
          <NavLink key={item.to} to={item.to} style={linkStyle}>
            {item.label}
          </NavLink>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}

/** Exposed as vendor_connectivity_mfe/App via Module Federation. Takes NO
 *  props: the console mounts it under `/vendor-connectivity/*` and provides
 *  the BrowserRouter, the design tokens and `window.__WAREHOUSE_CONFIG__`.
 *  Routes here are RELATIVE, so the component works identically under that
 *  prefix (in the shell) or at / (standalone dev, see main.tsx). It calls
 *  ONLY this context's own API (/api/vendor-connectivity), and it is
 *  READ-ONLY: every screen is a GET.
 *
 *   - (index): redirects to orders.
 *   - orders: the purchase orders released to one vendor, paged with
 *     nextToken, narrowable to one node.
 *   - orders/:poNumber: one order -- its windows, lines, acknowledgement,
 *     labels and tracking numbers.
 *   - nodes: the vendor's fulfilment nodes and, for the selected one, its
 *     rolling scorecard relayed from node-performance. */
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<ConnectivityLayout />}>
        <Route index element={<Navigate to="orders" replace />} />
        <Route path="orders" element={<OrdersScreen />} />
        <Route path="orders/:poNumber" element={<OrderDetailRoute />} />
        <Route path="nodes" element={<NodesScreen />} />
      </Route>
    </Routes>
  );
}
