Feature: Withdrawn releases (cross-order consolidation)
  order-sourcing may merge several orders of one shopper and address into ONE
  release; the vendor sees one purchase order with the union of the lines. When
  order-sourcing dissolves the merge before the vendor acknowledged it, it
  withdraws the release and this context cancels the issued order (ADR 0013).

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes

  Scenario: A merged release becomes one purchase order with every member's lines
    When order-sourcing releases merged assignment "asg-1" with follower "asg-2" to node "N-1"
    Then there are 1 purchase orders
    And the purchase order of "asg-1" is in state "ISSUED"
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response lists 3 items in "lines"

  Scenario: Withdrawing a merged release cancels the issued order
    Given order-sourcing releases merged assignment "asg-1" with follower "asg-2" to node "N-1"
    When order-sourcing withdraws release 1 of assignment "asg-1"
    Then the purchase order of "asg-1" is in state "CANCELLED"
    And 1 "PurchaseOrderCancelled" events were published

  Scenario: A withdraw after acceptance leaves the order accepted
    Given order-sourcing releases merged assignment "asg-1" with follower "asg-2" to node "N-1"
    And vendor "V-1" has accepted the merged purchase order of "asg-1"
    When order-sourcing withdraws release 1 of assignment "asg-1"
    Then the purchase order of "asg-1" is in state "ACCEPTED"
