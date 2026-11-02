Feature: Polling purchase orders
  The vendor polls its purchase orders in pages with cursor. Pages are
  ordered oldest first; a page may repeat its first order (the
  PO_REDELIVERY_RATE simulation knob) because delivery is at-least-once.
  There is no Background: the simulation knob scenario must set its knob
  before the vendor exists (a knob rebuilds the service).

  Scenario: Polling pages through purchase orders with cursor
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    And 1 minute passes
    And order-sourcing releases assignment "asg-2" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders?vendorId=V-1&limit=1"
    Then the response status is 200
    And the response lists 1 items in "items"
    And the response field "items.0.poNumber" is "{po:asg-1}"
    And the response field "nextCursor" is present
    When I poll again with the returned cursor for vendor "V-1"
    Then the response status is 200
    And the response lists 1 items in "items"
    And the response field "items.0.poNumber" is "{po:asg-2}"
    And the response field "nextCursor" is absent

  Scenario: A cursor this API did not issue is refused
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders?vendorId=V-1&cursor=Zm9vYmFy"
    Then the response status is 400
    And the problem type is "invalid-cursor"

  Scenario: A poll page may repeat its first order
    Given every poll page repeats its first order
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders?vendorId=V-1"
    Then the response status is 200
    And the response lists 2 items in "items"
    And the response field "items.0.poNumber" is "{po:asg-1}"
    And the response field "items.1.poNumber" is "{po:asg-1}"
