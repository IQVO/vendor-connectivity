Feature: Node and packaging read models
  VendorNodeView and CatalogPackagingView are local, event-fed copies. A node
  status follows the last event except that RETIRED is terminal; packaging
  events may arrive out of order and the retirement newer than the declaration
  wins (domain-events consumed table, ADR 0003, ADR 0005).

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes

  Scenario: A suspended node returns to ACTIVE when node-topology activates it again
    Given node "N-1" is suspended by node-topology
    When node-topology activates node "N-1"
    And I send GET "/vendor/nodes/N-1?vendorId=V-1"
    Then the response field "status" is "ACTIVE"

  Scenario: A retired node stays retired
    When node-topology retires node "N-1"
    And node-topology activates node "N-1"
    And I send GET "/vendor/nodes/N-1?vendorId=V-1"
    Then the response field "status" is "RETIRED"

  Scenario: A retired pack level leaves the packaging of the purchase order lines
    Given the listing "lst-1" is on node "N-1"
    And offer-availability declares a "CASE" level with gtin "10614141000019" of 12 eaches for catalog item "c-1"
    And offer-availability retires the "CASE" level with gtin "10614141000019" for catalog item "c-1" at "2026-10-21T08:00:00Z"
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response status is 200
    And the response lists 0 items in "lines.0.packaging"

  Scenario: A retirement that arrives before its declaration still wins
    Given the listing "lst-1" is on node "N-1"
    And offer-availability retires the "CASE" level with gtin "10614141000019" for catalog item "c-1" at "2026-10-21T08:00:00Z"
    And offer-availability declares a "CASE" level with gtin "10614141000019" of 12 eaches for catalog item "c-1"
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response lists 0 items in "lines.0.packaging"

  Scenario: A retirement older than the declaration changes nothing
    Given the listing "lst-1" is on node "N-1"
    And offer-availability declares a "CASE" level with gtin "10614141000019" of 12 eaches for catalog item "c-1"
    And offer-availability retires the "CASE" level with gtin "10614141000019" for catalog item "c-1" at "2026-10-19T08:00:00Z"
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response lists 1 items in "lines.0.packaging"
    And the response field "lines.0.packaging.0.level" is "CASE"
