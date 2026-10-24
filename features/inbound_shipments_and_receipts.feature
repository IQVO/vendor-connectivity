Feature: Inbound shipments and receipts
  Sellers ship cartons to the vendor's nodes. The vendor polls the SHIPPED
  shipments as a pallet/carton/item tree (SSCC and GTIN, quantities in eaches)
  and reports what its dock received. The receipt is an asynchronous write:
  202 with a transactionId, SubmissionKind INBOUND_RECEIPT, and the network
  publishes InboundReceiptSubmitted.

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    And node "N-9" of vendor "V-2" has a handling time of 45 minutes

  Scenario: The poll lists only shipped shipments of the vendor's own nodes
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    And seller-inbound ships shipment "SHP-2" to node "N-9"
    And seller-inbound labels shipment "SHP-3" for node "N-1" without shipping it
    When I send GET "/vendor/inbound-shipments?vendorId=V-1"
    Then the response status is 200
    And the response lists 1 items in "inboundShipments"
    And the response field "inboundShipments.0.shipmentId" is "SHP-1"
    And the response field "inboundShipments.0.status" is "SHIPPED"
    And the response field "inboundShipments.0.pallets.0.sscc" is "006141410000000036"
    And the response field "inboundShipments.0.pallets.0.cartons.0.contents.0.gtin" is "00614141000036"
    And the response field "inboundShipments.0.cartons.0.kind" is "MIXED"
    And the response field "nextToken" is absent

  Scenario: One shipment is read as a tree, another vendor's is not found
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    When I send GET "/vendor/inbound-shipments/SHP-1?vendorId=V-1"
    Then the response status is 200
    And the response field "sellerId" is "S-1"
    And the response field "cartons.0.sscc" is "006141410000000029"
    When I send GET "/vendor/inbound-shipments/SHP-1?vendorId=V-2"
    Then the response status is 404
    And the problem type is "not-found"

  Scenario: Polling pages through shipments with nextToken
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    And 1 minute passes
    And seller-inbound ships shipment "SHP-2" to node "N-1"
    When I send GET "/vendor/inbound-shipments?vendorId=V-1&limit=1"
    Then the response lists 1 items in "inboundShipments"
    And the response field "inboundShipments.0.shipmentId" is "SHP-1"
    And the response field "nextToken" is present
    When I poll inbound shipments again with the returned nextToken for vendor "V-1"
    Then the response lists 1 items in "inboundShipments"
    And the response field "inboundShipments.0.shipmentId" is "SHP-2"
    And the response field "nextToken" is absent

  Scenario: A receipt is accepted, processed and published
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    When I send POST "/vendor/inbound-receipts" with:
      """
      {"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"N-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[
        {"sscc":"006141410000000012","status":"RECEIVED","items":[{"gtin":"0614141000036","qtyEaches":12}]},
        {"sscc":"006141410000000029","status":"DAMAGED","items":[{"gtin":"00614141000036","qtyEaches":1}],"complianceCodes":["CARTON_CRUSHED"]},
        {"sscc":"006141410000000104","status":"UNEXPECTED"}]}
      """
    Then the response status is 202
    When I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "kind" is "INBOUND_RECEIPT"
    And the response field "status" is "PROCESSING"
    When the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"
    And the outbox event types are "VendorOnboarded, VendorOnboarded, InboundReceiptSubmitted, SubmissionProcessed"

  Scenario: A carton that is not on the shipment is refused unless it is UNEXPECTED
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    When I send POST "/vendor/inbound-receipts" with:
      """
      {"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"N-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[
        {"sscc":"006141410000000104","status":"RECEIVED"}]}
      """
    Then the response status is 422
    And the problem type is "validation-failed"

  Scenario: A receipt for an unknown or foreign shipment is refused
    Given seller-inbound ships shipment "SHP-2" to node "N-9"
    When I send POST "/vendor/inbound-receipts" with:
      """
      {"vendorId":"V-1","shipmentId":"SHP-404","nodeId":"N-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[{"sscc":"006141410000000012","status":"MISSING"}]}
      """
    Then the response status is 404
    When I send POST "/vendor/inbound-receipts" with:
      """
      {"vendorId":"V-1","shipmentId":"SHP-2","nodeId":"N-9","receivedAt":"2026-10-25T09:30:00Z","cartons":[{"sscc":"006141410000000012","status":"MISSING"}]}
      """
    Then the response status is 404
    And the problem type is "node-not-owned-by-vendor"

  Scenario: A shipment that was already received cannot be received again
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    And seller-inbound closes shipment "SHP-1" for node "N-1"
    When I send POST "/vendor/inbound-receipts" with:
      """
      {"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"N-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[{"sscc":"006141410000000012","status":"MISSING"}]}
      """
    Then the response status is 409
    And the problem type is "illegal-transition"

  Scenario: Purchase order lines carry the active packaging of their catalogue item
    Given the listing "lst-1" is on node "N-1"
    And offer-availability declares a "CASE" level with gtin "10614141000019" of 12 eaches for catalog item "c-1"
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response status is 200
    And the response field "lines.0.packaging.0.level" is "CASE"
    And the response field "lines.0.packaging.0.gtin" is "10614141000019"
    And the response field "lines.0.packaging.0.unitsPerPack" is "12"
