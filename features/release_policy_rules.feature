Feature: Releases, cancellations and withdrawals from order-sourcing
  Policy IssueOnRelease issues one purchase order per (assignmentId, releaseNo)
  for a VENDOR node whose vendor has an account; policies CancelOnSourcingCancel
  (the open, highest release) and CancelOnReleaseWithdrawn (exactly the
  withdrawn release) cancel an order only while it is ISSUED. Everything else
  is logged and acknowledged, never retried (use-cases, canvas policies, ADR
  0002 A4, A13, A15, A16, ADR 0013).

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes

  Scenario: A release for a node the view does not know yet is retried, not lost
    When order-sourcing delivers the release of assignment "asg-1" to node "N-7" but the consumer cannot place it yet
    Then there are 0 purchase orders
    When node "N-7" of vendor "V-1" has a handling time of 60 minutes
    And that release event is delivered again
    Then the purchase order of "asg-1" is in state "ISSUED"
    And 1 "PurchaseOrderIssued" events were published

  Scenario: A release for a network-operated node issues nothing
    Given node "N-8" is operated by the network
    When order-sourcing releases assignment "asg-1" to node "N-8" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    Then there are 0 purchase orders
    And the outbox event types are "VendorOnboarded"

  Scenario: A release for a node whose vendor has no account is dropped
    Given node "N-5" of vendor "V-9" has a handling time of 60 minutes
    When order-sourcing releases assignment "asg-1" to node "N-5" with required ship-by "2026-10-22T21:00:00Z" for 1 line
    Then there are 0 purchase orders
    And the outbox event types are "VendorOnboarded"

  Scenario: The same release re-sent under a new event id still issues one purchase order
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing delivers the release of assignment "asg-1" to node "N-1" again under another event id
    Then there are 1 purchase orders
    And 1 "PurchaseOrderIssued" events were published

  Scenario: A new release of the same assignment is a new purchase order
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And vendor "V-1" has rejected the purchase order of "asg-1" with reason "OUT_OF_STOCK"
    When order-sourcing releases release 2 of assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    Then there are 2 purchase orders
    And release 2 of "asg-1" is in state "ISSUED"
    And release 1 of "asg-1" is in state "REJECTED"

  Scenario: A cancellation addresses the open release, never an older one
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And vendor "V-1" has rejected the purchase order of "asg-1" with reason "OUT_OF_STOCK"
    And order-sourcing releases release 2 of assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing cancels assignment "asg-1"
    Then release 2 of "asg-1" is in state "CANCELLED"
    And release 1 of "asg-1" is in state "REJECTED"
    And 1 "PurchaseOrderCancelled" events were published

  Scenario: A withdrawal addresses its own release number, never the latest
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And order-sourcing releases release 2 of assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing withdraws release 1 of assignment "asg-1"
    Then release 1 of "asg-1" is in state "CANCELLED"
    And release 2 of "asg-1" is in state "ISSUED"

  Scenario Outline: A cancellation after the vendor moved on leaves the order as it is
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And the purchase order of "asg-1" has reached state "<state>"
    When order-sourcing cancels assignment "asg-1"
    Then the purchase order of "asg-1" is in state "<state>"
    And 0 "PurchaseOrderCancelled" events were published

    Examples:
      | state               |
      | ACK_PROCESSING      |
      | REJECTED            |
      | EXPIRED             |
      | SHIPMENT_PROCESSING |
      | SHIPPED             |

  Scenario Outline: A withdrawal after the vendor moved on leaves the order as it is
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And the purchase order of "asg-1" has reached state "<state>"
    When order-sourcing withdraws release 1 of assignment "asg-1"
    Then the purchase order of "asg-1" is in state "<state>"
    And 0 "PurchaseOrderCancelled" events were published

    Examples:
      | state               |
      | ACK_PROCESSING      |
      | REJECTED            |
      | EXPIRED             |
      | SHIPMENT_PROCESSING |
      | SHIPPED             |

  Scenario: A withdrawal of an order already cancelled publishes nothing more
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And order-sourcing cancels assignment "asg-1"
    When order-sourcing withdraws release 1 of assignment "asg-1"
    Then the purchase order of "asg-1" is in state "CANCELLED"
    And 1 "PurchaseOrderCancelled" events were published

  Scenario: A cancelled order is cancelled once however often the cancellation arrives
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing cancels assignment "asg-1"
    And order-sourcing cancels assignment "asg-1"
    Then the purchase order of "asg-1" is in state "CANCELLED"
    And 1 "PurchaseOrderCancelled" events were published

  Scenario: A cancellation for an assignment with no order does nothing
    When order-sourcing cancels assignment "asg-404"
    Then there are 0 purchase orders
    And the outbox event types are "VendorOnboarded"

  Scenario: A withdrawal of a release that was never issued does nothing
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing withdraws release 5 of assignment "asg-1" because "PROMISE_REVISED"
    Then the purchase order of "asg-1" is in state "ISSUED"
    And 0 "PurchaseOrderCancelled" events were published

  Scenario Outline: The cancel reason is sanitised before it is kept
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing cancels assignment "asg-1" because "<reason>"
    Then the "PurchaseOrderCancelled" event about "{po:asg-1}" carries "reason" "<kept>"

    Examples:
      | reason           | kept                        |
      |                  | cancelled by order-sourcing |
      | late\tfee        | late fee                    |
      | \tlate\t         | late                        |
      | customer changed | customer changed            |

  Scenario Outline: A cancel reason is cut at 200 characters
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing cancels assignment "asg-1" with a reason of <length> characters
    Then the "PurchaseOrderCancelled" event about "{po:asg-1}" has a "reason" of <kept> characters

    Examples:
      | length | kept |
      | 199    | 199  |
      | 200    | 200  |
      | 201    | 200  |
      | 500    | 200  |

  Scenario Outline: A withdrawal keeps its reason after the fixed prefix
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing withdraws release 1 of assignment "asg-1" because "<reason>"
    Then the "PurchaseOrderCancelled" event about "{po:asg-1}" carries "reason" "<kept>"

    Examples:
      | reason          | kept                               |
      | PROMISE_REVISED | release withdrawn: PROMISE_REVISED |
      |                 | release withdrawn                  |
      | \t\t            | release withdrawn                  |

  Scenario Outline: A withdrawal reason is cut so the whole text stays within 200 characters
    Given order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When order-sourcing withdraws release 1 of assignment "asg-1" with a reason of <length> characters
    Then the "PurchaseOrderCancelled" event about "{po:asg-1}" has a "reason" of <kept> characters

    Examples:
      | length | kept |
      | 180    | 199  |
      | 181    | 200  |
      | 300    | 200  |
