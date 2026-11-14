Feature: Polling the purchase orders
  The poll returns the vendor's own orders in every state, oldest first, in
  keyset pages (limit 1..500, opaque cursor bound to the vendor, createdAfter
  strict on issuedAt); a vendor may narrow it to one of its own nodes (ADR
  0002 A17, A23, A25; REST paging guideline).

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes

  Scenario: A page that exactly fills the limit has no next cursor
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    And 1 minute passes
    And order-sourcing releases assignment "asg-2" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders?vendorId=V-1&limit=2"
    Then the response status is 200
    And the response lists 2 items in "items"
    And the response field "nextCursor" is absent

  Scenario: createdAfter is exclusive
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    And 1 minute passes
    And order-sourcing releases assignment "asg-2" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders?vendorId=V-1&createdAfter=2026-10-22T13:00:00Z"
    Then the response status is 200
    And the response lists 1 items in "items"
    And the response field "items.0.poNumber" is "{po:asg-2}"

  Scenario Outline: A limit outside 1 to 500 is refused
    When I send GET "/vendor/purchase-orders?vendorId=V-1&limit=<limit>"
    Then the response status is 400
    And the problem type is "invalid-limit"

    Examples:
      | limit |
      | 0     |
      | 501   |
      | abc   |

  Scenario Outline: A limit of 1 or 500 is accepted
    When I send GET "/vendor/purchase-orders?vendorId=V-1&limit=<limit>"
    Then the response status is 200

    Examples:
      | limit |
      | 1     |
      | 500   |

  Scenario: A cursor is bound to the vendor that received it
    Given vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    And 1 minute passes
    And order-sourcing releases assignment "asg-2" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders?vendorId=V-1&limit=1"
    Then the response field "nextCursor" is present
    When I poll again with the returned cursor for vendor "V-2"
    Then the response status is 400
    And the problem type is "invalid-cursor"

  Scenario: The poll lists the vendor's own orders in every state and nobody else's
    Given vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    And node "N-2" of vendor "V-2" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And the purchase order of "asg-1" has reached state "ACCEPTED"
    And 1 minute passes
    And order-sourcing releases assignment "asg-2" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And the purchase order of "asg-2" has reached state "CANCELLED"
    And 1 minute passes
    And order-sourcing releases assignment "asg-3" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And order-sourcing releases assignment "asg-9" to node "N-2" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When I send GET "/vendor/purchase-orders?vendorId=V-1"
    Then the response lists 3 items in "items"
    And the response field "items.0.state" is "ACCEPTED"
    And the response field "items.1.state" is "CANCELLED"
    And the response field "items.2.state" is "ISSUED"
    When I send GET "/vendor/purchase-orders?vendorId=V-2"
    Then the response lists 1 items in "items"
    And the response field "items.0.nodeId" is "N-2"

  Scenario: The poll can be narrowed to a node of the vendor but not to someone else's
    Given node "N-2" of vendor "V-1" has a handling time of 60 minutes
    And vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    And node "N-9" of vendor "V-2" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    And order-sourcing releases assignment "asg-2" to node "N-2" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders?vendorId=V-1&nodeId=N-2"
    Then the response lists 1 items in "items"
    And the response field "items.0.poNumber" is "{po:asg-2}"
    When I send GET "/vendor/purchase-orders?vendorId=V-1&nodeId=N-9"
    Then the response status is 404
    And the problem type is "node-not-owned-by-vendor"

  Scenario: A paging parameter the poll does not declare is refused
    When I send GET "/vendor/purchase-orders?vendorId=V-1&after=abc"
    Then the response status is 400
    And the problem type is "unknown-query-parameter"
