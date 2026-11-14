Feature: Vendor account rules
  The acknowledgement window is bounded to 1h..24h. A suspended vendor's
  writes are refused, while reads, new releases, the expiry sweep and
  transactions already received keep working (ADR 0002 A8, A9, A14; canvas
  VendorAccount invariants).

  Scenario Outline: A window of 60 or 1440 minutes is accepted
    When I send POST "/vendors" with:
      """
      {"vendorId":"V-1","name":"Carrier One","ackWindowMinutes":<minutes>}
      """
    Then the response status is 201
    And the response field "ackWindowMinutes" is "<minutes>"

    Examples:
      | minutes |
      | 60      |
      | 1440    |

  Scenario Outline: A window below 1h or above 24h is refused
    When I send POST "/vendors" with:
      """
      {"vendorId":"V-1","name":"Carrier One","ackWindowMinutes":<minutes>}
      """
    Then the response status is 422
    And the problem type is "ack-window-out-of-range"
    And the outbox event types are ""

    Examples:
      | minutes |
      | 0       |
      | 59      |
      | 1441    |

  Scenario: Onboarding an existing vendor again returns the account unchanged
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "/vendors" with:
      """
      {"vendorId":"V-1","name":"Another Name","ackWindowMinutes":600}
      """
    Then the response status is 200
    And the response field "name" is "Carrier V-1"
    And the response field "ackWindowMinutes" is "120"
    And the outbox event types are "VendorOnboarded"

  Scenario Outline: A suspended vendor's write is refused and creates no transaction
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And I send POST "/vendors/V-1/suspensions"
    When I send <method> "<path>" with:
      """
      <body>
      """
    Then the response status is 409
    And the problem type is "vendor-suspended"
    And there are 0 transactions
    And shipping-and-tracking received 0 IssueLabel calls

    Examples:
      | method | path                           | body                                                                                                                                                                                  |
      | POST   | /vendor/acknowledgements       | {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}                                                           |
      | POST   | /vendor/shipping-labels        | {"vendorId":"V-1","poNumber":"{po:asg-1}","packages":[{"packageNo":1}]}                                                                                                               |
      | POST   | /vendor/shipment-confirmations | {"vendorId":"V-1","poNumber":"{po:asg-1}","shippedAt":"2026-10-22T14:00:00Z","trackingNumbers":["TRK-1"]}                                                                             |
      | POST   | /vendor/inventory              | {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[]}                                                                                         |
      | PUT    | /vendor/nodes/N-1/profile      | {"vendorId":"V-1","profileRevision":1,"timezone":"America/Sao_Paulo","operatingHoursByDay":{},"handlingTimeMinutes":45,"cutoffs":[],"throughputCap":{"value":600,"timeUnit":"DAY","unit":"ORDER"}} |
      | POST   | /vendor/inbound-receipts       | {"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"N-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[{"sscc":"006141410000000012","status":"MISSING"}]}                                |

  Scenario: A suspended vendor still receives purchase orders and the sweep still expires them
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And I send POST "/vendors/V-1/suspensions"
    When order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    Then the purchase order of "asg-1" is in state "ISSUED"
    When I send GET "/vendor/purchase-orders?vendorId=V-1"
    Then the response lists 1 items in "items"
    When 3 hours pass
    Then the expiry sweep expires 1 order
    And the purchase order of "asg-1" is in state "EXPIRED"

  Scenario: A transaction received before the suspension is still processed
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And vendor "V-1" has acknowledged the purchase order of "asg-1" and its transaction is still processing
    When I send POST "/vendors/V-1/suspensions"
    And the submission queue is processed
    Then the purchase order of "asg-1" is in state "ACCEPTED"

  Scenario: A reinstated vendor writes again
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And I send POST "/vendors/V-1/suspensions"
    And I send POST "/vendors/V-1/reinstatements"
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 202

  Scenario Outline: Suspending or reinstating an unknown vendor is not found
    When I send POST "<path>"
    Then the response status is 404
    And the problem type is "not-found"

    Examples:
      | path                            |
      | /vendors/V-404/suspensions      |
      | /vendors/V-404/reinstatements   |

  Scenario: The suspension reason travels on the VendorSuspended event
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "/vendors/V-1/suspensions" with:
      """
      {"reason":"payment dispute"}
      """
    Then the response status is 200
    And the "VendorSuspended" event about "V-1" carries "reason" "payment dispute"
