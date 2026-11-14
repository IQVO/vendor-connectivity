Feature: Inbound shipment and receipt rules
  A received shipment leaves the poll. A receipt is judged on its own shape
  (SSCC and GTIN check digits, statuses, quantities in eaches) before any
  shipment is looked at, a refusal creates no transaction and no event, and an
  accepted one publishes InboundReceiptSubmitted in the same unit of work
  (use-cases SubmitInboundReceipt, domain-events inbound section, service ADR
  0005).

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes

  Scenario: A shipment the dock has received leaves the poll
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    And 1 minute passes
    And seller-inbound ships shipment "SHP-2" to node "N-1"
    And seller-inbound closes shipment "SHP-1" for node "N-1"
    When I send GET "/vendor/inbound-shipments?vendorId=V-1"
    Then the response lists 1 items in "items"
    And the response field "items.0.shipmentId" is "SHP-2"

  Scenario: An accepted receipt publishes InboundReceiptSubmitted about its shipment
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    When I send POST "/vendor/inbound-receipts" with:
      """
      {"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"N-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[{"sscc":"006141410000000012","status":"RECEIVED","items":[{"gtin":"00614141000036","qtyEaches":12}]}]}
      """
    Then the response status is 202
    And 1 "InboundReceiptSubmitted" events were published
    And the "InboundReceiptSubmitted" event about "SHP-1" carries "transactionId" "{tx}"
    And the "InboundReceiptSubmitted" event about "SHP-1" carries "vendorId" "V-1"
    And the "InboundReceiptSubmitted" event about "SHP-1" carries "nodeId" "N-1"

  Scenario: Counting more eaches than the manifest is reported, never refused
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    When I send POST "/vendor/inbound-receipts" with:
      """
      {"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"N-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[{"sscc":"006141410000000012","status":"RECEIVED","items":[{"gtin":"00614141000036","qtyEaches":50}]}]}
      """
    Then the response status is 202

  Scenario Outline: A receipt that breaks a rule is refused with no transaction and no event
    Given seller-inbound ships shipment "SHP-1" to node "N-1"
    When I send POST "/vendor/inbound-receipts" with:
      """
      {"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"N-1","receivedAt":"2026-10-25T09:30:00Z","cartons":<cartons>}
      """
    Then the response status is 422
    And the problem type is "validation-failed"
    And there are 0 transactions
    And 0 "InboundReceiptSubmitted" events were published

    Examples:
      | cartons                                                                                                                                              |
      | [{"sscc":"006141410000000013","status":"RECEIVED"}]                                                                                                  |
      | [{"sscc":"006141410000000012","status":"LOST"}]                                                                                                      |
      | [{"sscc":"006141410000000012","status":"RECEIVED","items":[{"gtin":"00614141000037","qtyEaches":1}]}]                                                |
      | [{"sscc":"006141410000000012","status":"RECEIVED","items":[{"gtin":"00614141000036","qtyEaches":-1}]}]                                               |
      | [{"sscc":"006141410000000012","status":"MISSING","items":[{"gtin":"00614141000036","qtyEaches":1}]}]                                                 |
      | [{"sscc":"006141410000000012","status":"RECEIVED"},{"sscc":"006141410000000012","status":"DAMAGED"}]                                                 |
      | [{"sscc":"006141410000000012","status":"RECEIVED","items":[{"gtin":"10614141000019","qtyEaches":1}]}]                                                |
      | []                                                                                                                                                   |
