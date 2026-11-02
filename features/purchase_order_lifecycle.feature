Feature: Purchase order lifecycle
  order-sourcing releases an assignment to a vendor node; the release becomes a
  purchase order the vendor polls, acknowledges in full inside the window,
  labels and confirms as shipped.

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes

  Scenario: A release becomes a polled purchase order with an ackBy deadline
    When order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    Then the purchase order of "asg-1" is in state "ISSUED"
    And the purchase order of "asg-1" must be acknowledged by "2026-10-22T15:00:00Z"
    When I send GET "/vendor/purchase-orders?vendorId=V-1"
    Then the response status is 200
    And the response lists 1 items in "items"
    And the response field "items.0.state" is "ISSUED"
    And the response never mentions "orderId"

  Scenario: ackBy is capped by the required ship-by minus the handling time
    When order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T15:30:00Z" for 1 line
    Then the purchase order of "asg-1" must be acknowledged by "2026-10-22T14:30:00Z"

  Scenario: Delivering the same release twice issues one purchase order
    When order-sourcing delivers the release of assignment "asg-1" to node "N-1" twice
    Then there are 1 purchase orders
    And the outbox event types are "VendorOnboarded, PurchaseOrderIssued"

  Scenario: A full acknowledgement is accepted asynchronously
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 202
    When I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "PROCESSING"
    When the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"
    And the purchase order of "asg-1" is in state "ACCEPTED"
    And 1 "PurchaseOrderAccepted" events were published
    And the "PurchaseOrderAccepted" event about "{po:asg-1}" carries "transactionId" "{tx}"

  Scenario: A partial acknowledgement is refused
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2}]}
      """
    Then the response status is 422
    And the problem type is "partial-acknowledgement"

  Scenario: A rejection carries its reason code
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"REJECT","reasonCode":"OUT_OF_STOCK","lines":[{"lineNo":1,"quantity":2}]}
      """
    Then the response status is 202
    When the submission queue is processed
    Then the purchase order of "asg-1" is in state "REJECTED"
    And 1 "PurchaseOrderRejected" events were published
    And the "PurchaseOrderRejected" event about "{po:asg-1}" carries "reasonCode" "OUT_OF_STOCK"

  Scenario: A second acknowledgement is refused
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And vendor "V-1" has accepted the purchase order of "asg-1"
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 409
    And the problem type is "already-acknowledged"

  Scenario: An acknowledgement after ackBy is refused and the sweep expires the order
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When 3 hours pass
    And the expiry sweep runs
    Then the purchase order of "asg-1" is in state "EXPIRED"
    And 1 "PurchaseOrderExpired" events were published
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 409
    And the problem type is "ack-window-closed"

  Scenario: A purchase order of another vendor is not found
    Given vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-2"
    Then the response status is 404

  Scenario: Cancelling an unacknowledged order cancels it
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing cancels assignment "asg-1"
    Then the purchase order of "asg-1" is in state "CANCELLED"
    And 1 "PurchaseOrderCancelled" events were published
    And the "PurchaseOrderCancelled" event about "{po:asg-1}" carries "assignmentId" "asg-1"

  Scenario: A malformed body is refused as validation-failed
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":
      """
    Then the response status is 422
    And the problem type is "validation-failed"

  Scenario: A cancel after acceptance leaves the order accepted
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And vendor "V-1" has accepted the purchase order of "asg-1"
    When order-sourcing cancels assignment "asg-1"
    Then the purchase order of "asg-1" is in state "ACCEPTED"
