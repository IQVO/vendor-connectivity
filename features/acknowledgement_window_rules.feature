Feature: The acknowledgement window and the expiry clock
  ackBy = min(issuedAt + vendor ackWindow, requiredShipBy - node handlingTime)
  is fixed at issue. An acknowledgement is judged on the receipt instant and
  now == ackBy is still inside the window; the sweep expires an ISSUED order
  only strictly after ackBy (business-context decision 2, canvas invariants,
  ExpireUnacknowledged policy, ADR 0002 A3, A5, A6, A26).

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes

  Scenario: A node with no declared profile has no handling time to subtract
    Given node "N-2" of vendor "V-1" has no declared profile
    When order-sourcing releases assignment "asg-1" to node "N-2" with required ship-by "2026-10-22T13:30:00Z" for 1 line
    Then the purchase order of "asg-1" must be acknowledged by "2026-10-22T13:30:00Z"

  Scenario: The latest profile revision sets the handling time and a stale one never lowers it
    Given node-topology declares profile revision 2 of node "N-1" with a handling time of 120 minutes
    And node-topology declares profile revision 1 of node "N-1" with a handling time of 10 minutes
    When order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T15:30:00Z" for 1 line
    Then the purchase order of "asg-1" must be acknowledged by "2026-10-22T13:30:00Z"
    When I send GET "/vendor/nodes/N-1?vendorId=V-1"
    Then the response field "handlingTimeMinutes" is "120"
    And the response field "profileRevision" is "2"

  Scenario: An ackBy already past at issue is accepted as computed and the sweep expires the order
    When order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T13:30:00Z" for 2 lines
    Then the purchase order of "asg-1" must be acknowledged by "2026-10-22T12:30:00Z"
    And the purchase order of "asg-1" is in state "ISSUED"
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 409
    And the problem type is "ack-window-closed"
    When the expiry sweep expires 1 order
    Then the purchase order of "asg-1" is in state "EXPIRED"

  Scenario: An acknowledgement received at exactly ackBy is still inside the window
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When 2 hours pass
    And I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 202
    When the submission queue is processed
    Then the purchase order of "asg-1" is in state "ACCEPTED"

  Scenario: One second after ackBy the acknowledgement is refused although nothing swept the order
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When 2 hours pass
    And 1 second passes
    And I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 409
    And the problem type is "ack-window-closed"
    And the purchase order of "asg-1" is in state "ISSUED"
    And there are 0 transactions

  Scenario: The sweep does not expire an order at exactly ackBy but does one second later
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When 2 hours pass
    Then the expiry sweep expires 0 orders
    And the purchase order of "asg-1" is in state "ISSUED"
    When 1 second passes
    Then the expiry sweep expires 1 order
    And the purchase order of "asg-1" is in state "EXPIRED"
    And 1 "PurchaseOrderExpired" events were published
    And the "PurchaseOrderExpired" event about "{po:asg-1}" carries "assignmentId" "asg-1"
    And the "PurchaseOrderExpired" event about "{po:asg-1}" carries "releaseNo" "1"

  Scenario: An expired order is not expired again by the next pass
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When 3 hours pass
    Then the expiry sweep expires 1 order
    And the expiry sweep expires 0 orders
    And 1 "PurchaseOrderExpired" events were published

  Scenario: The sweep expires only orders still ISSUED
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And order-sourcing releases assignment "asg-2" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And order-sourcing releases assignment "asg-3" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And order-sourcing releases assignment "asg-4" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And the purchase order of "asg-1" has reached state "ACCEPTED"
    And the purchase order of "asg-2" has reached state "REJECTED"
    And the purchase order of "asg-3" has reached state "CANCELLED"
    When 3 hours pass
    Then the expiry sweep expires 1 order
    And the purchase order of "asg-1" is in state "ACCEPTED"
    And the purchase order of "asg-2" is in state "REJECTED"
    And the purchase order of "asg-3" is in state "CANCELLED"
    And the purchase order of "asg-4" is in state "EXPIRED"
    And 1 "PurchaseOrderExpired" events were published
