import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "api-reference/rest/vendor-connectivity-api",
    },
    {
      type: "category",
      label: "Purchase Orders",
      link: {
        type: "doc",
        id: "api-reference/rest/purchase-orders",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/poll-purchase-orders",
          label: "Poll purchase orders (PollPurchaseOrders)",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-purchase-order",
          label: "Get one purchase order (GetPurchaseOrder)",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Acknowledgements",
      link: {
        type: "doc",
        id: "api-reference/rest/acknowledgements",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/submit-acknowledgement",
          label: "Acknowledge a purchase order in full (SubmitAcknowledgement)",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Inventory",
      link: {
        type: "doc",
        id: "api-reference/rest/inventory",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/submit-inventory",
          label: "Submit an inventory feed for one node (SubmitInventory)",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Nodes",
      link: {
        type: "doc",
        id: "api-reference/rest/nodes",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/list-vendor-nodes",
          label: "List the vendor's nodes (ListVendorNodes)",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/register-vendor-node",
          label: "Register a vendor node (RegisterVendorNode)",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-node",
          label: "Get one of the vendor's nodes (GetNode)",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/declare-node-profile",
          label: "Declare the node's operational profile (DeclareNodeProfile)",
          className: "api-method put",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-scorecard",
          label: "Get the node's measured scorecard (GetScorecard)",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Shipping Labels",
      link: {
        type: "doc",
        id: "api-reference/rest/shipping-labels",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/request-label",
          label: "Request labels for an accepted purchase order (RequestLabel)",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Shipment Confirmations",
      link: {
        type: "doc",
        id: "api-reference/rest/shipment-confirmations",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/submit-shipment-confirmation",
          label: "Confirm that a purchase order shipped (SubmitShipmentConfirmation)",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Inbound Shipments",
      link: {
        type: "doc",
        id: "api-reference/rest/inbound-shipments",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/poll-inbound-shipments",
          label: "Poll inbound shipments (PollInboundShipments)",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-inbound-shipment",
          label: "Get one inbound shipment (GetInboundShipment)",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Inbound Receipts",
      link: {
        type: "doc",
        id: "api-reference/rest/inbound-receipts",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/submit-inbound-receipt",
          label: "Report an inbound receipt (SubmitInboundReceipt)",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Transactions",
      link: {
        type: "doc",
        id: "api-reference/rest/transactions",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/get-transaction",
          label: "Get the outcome of an asynchronous write (GetTransaction)",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Vendors",
      link: {
        type: "doc",
        id: "api-reference/rest/vendors",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/onboard-vendor",
          label: "Onboard a vendor (OnboardVendor)",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-vendor",
          label: "Get a vendor account (GetVendor)",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/suspend-vendor",
          label: "Suspend a vendor (SuspendVendor)",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/reinstate-vendor",
          label: "Reinstate a vendor (ReinstateVendor)",
          className: "api-method post",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
