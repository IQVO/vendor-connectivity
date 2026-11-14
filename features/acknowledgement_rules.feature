Feature: What a valid acknowledgement is
  An acknowledgement covers every line once at its ordered quantity with one
  decision (fill-or-kill), is made at most once (one still processing counts),
  and is checked in the order state, window, completeness, decision: the first
  failure wins (ADR 0002 A6). Rules checkable at receipt are refused
  synchronously and create no transaction. A vendor acts only on its own
  orders (canvas invariants, use-cases error table).

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines

  Scenario Outline: An acknowledgement that does not match the order line by line is partial
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":<lines>}
      """
    Then the response status is 422
    And the problem type is "partial-acknowledgement"
    And the purchase order of "asg-1" is in state "ISSUED"
    And there are 0 transactions

    Examples:
      | lines                                                                            |
      | []                                                                               |
      | [{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":2}]                            |
      | [{"lineNo":1,"quantity":2},{"lineNo":3,"quantity":1}]                            |
      | [{"lineNo":0,"quantity":2},{"lineNo":2,"quantity":1}]                            |
      | [{"lineNo":1,"quantity":2},{"lineNo":1,"quantity":2}]                            |
      | [{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1},{"lineNo":2,"quantity":1}]  |

  Scenario Outline: A decision the order cannot take is a validation failure
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"<decision>"<reason>,"lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 422
    And the problem type is "validation-failed"
    And the purchase order of "asg-1" is in state "ISSUED"
    And there are 0 transactions

    Examples:
      | decision | reason                         |
      | REJECT   |                                |
      | REJECT   | ,"reasonCode":"TOO_BUSY"       |
      | ACCEPT   | ,"reasonCode":"OUT_OF_STOCK"   |
      | MAYBE    |                                |

  Scenario Outline: Every documented reject reason is relayed to order-sourcing
    Given vendor "V-1" has rejected the purchase order of "asg-1" with reason "<reason>"
    Then the purchase order of "asg-1" is in state "REJECTED"
    And 1 "PurchaseOrderRejected" events were published
    And the "PurchaseOrderRejected" event about "{po:asg-1}" carries "reasonCode" "<reason>"
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response field "acknowledgement.decision" is "REJECT"
    And the response field "acknowledgement.reasonCode" is "<reason>"

    Examples:
      | reason              |
      | UNKNOWN_LISTING     |
      | CANNOT_MEET_SHIP_BY |
      | DISCONTINUED        |

  Scenario: The state is judged before completeness: a partial answer to an accepted order is already-acknowledged
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2}]}
      """
    Then the response status is 409
    And the problem type is "already-acknowledged"

  Scenario: The window is judged before completeness: a partial answer to an expired order is ack-window-closed
    Given the purchase order of "asg-1" has reached state "EXPIRED"
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2}]}
      """
    Then the response status is 409
    And the problem type is "ack-window-closed"

  Scenario: Completeness is judged before the decision: a partial answer with no reason code is partial
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"REJECT","lines":[{"lineNo":1,"quantity":2}]}
      """
    Then the response status is 422
    And the problem type is "partial-acknowledgement"

  Scenario: An acknowledgement of a cancelled order is an illegal transition
    Given the purchase order of "asg-1" has reached state "CANCELLED"
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 409
    And the problem type is "illegal-transition"
    And the purchase order of "asg-1" is in state "CANCELLED"

  Scenario: A second acknowledgement while the first is still processing is refused
    Given vendor "V-1" has acknowledged the purchase order of "asg-1" and its transaction is still processing
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"REJECT","reasonCode":"DISCONTINUED","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 409
    And the problem type is "already-acknowledged"
    And the purchase order of "asg-1" is in state "ACK_PROCESSING"
    And there are 1 transactions

  Scenario: The acknowledgement stays hidden from the vendor until its transaction succeeded
    Given vendor "V-1" has acknowledged the purchase order of "asg-1" and its transaction is still processing
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response field "state" is "ACK_PROCESSING"
    And the response field "acknowledgement" is absent
    When the submission queue is processed
    And I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response field "state" is "ACCEPTED"
    And the response field "acknowledgement.decision" is "ACCEPT"

  Scenario Outline: Another vendor cannot act on this purchase order
    Given vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "<path>" with:
      """
      <body>
      """
    Then the response status is 404
    And the problem type is "node-not-owned-by-vendor"
    And the purchase order of "asg-1" is in state "ISSUED"
    And there are 0 transactions
    And shipping-and-tracking received 0 IssueLabel calls

    Examples:
      | path                           | body                                                                                                                  |
      | /vendor/acknowledgements       | {"vendorId":"V-2","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]} |
      | /vendor/shipping-labels        | {"vendorId":"V-2","poNumber":"{po:asg-1}","packages":[{"packageNo":1}]}                                                |
      | /vendor/shipment-confirmations | {"vendorId":"V-2","poNumber":"{po:asg-1}","shippedAt":"2026-10-22T14:00:00Z","trackingNumbers":["TRK-1"]}             |

  Scenario Outline: An unknown purchase order or vendor is not found
    When I send POST "<path>" with:
      """
      <body>
      """
    Then the response status is 404
    And the problem type is "not-found"
    And there are 0 transactions

    Examples:
      | path                           | body                                                                                                     |
      | /vendor/acknowledgements       | {"vendorId":"V-1","poNumber":"PO-nope","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2}]}           |
      | /vendor/shipping-labels        | {"vendorId":"V-1","poNumber":"PO-nope","packages":[{"packageNo":1}]}                                      |
      | /vendor/shipment-confirmations | {"vendorId":"V-1","poNumber":"PO-nope","shippedAt":"2026-10-22T14:00:00Z","trackingNumbers":["TRK-1"]}    |
      | /vendor/acknowledgements       | {"vendorId":"V-404","poNumber":"PO-nope","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2}]}         |
