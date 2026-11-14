Feature: Labels and shipment confirmation rules
  A label only for an ACCEPTED order, a shipment confirmation only after a
  label for every package and naming exactly those tracking numbers. States
  without a dedicated Err are illegal transitions; repeating a label request
  returns the stored labels and issues only the missing ones with a
  deterministic idempotency key (business-context decision 3, canvas
  invariants, use-cases RequestLabel and SubmitShipmentConfirmation, ADR 0002
  A7, A19, A20).

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines

  Scenario Outline: A label is refused until the order is accepted
    Given the purchase order of "asg-1" has reached state "<state>"
    When I request labels for packages 1 to 1 of the purchase order of "asg-1" for vendor "V-1"
    Then the response status is 409
    And the problem type is "<slug>"
    And shipping-and-tracking received 0 IssueLabel calls

    Examples:
      | state          | slug                    |
      | ACK_PROCESSING | label-before-acceptance |
      | REJECTED       | illegal-transition      |
      | EXPIRED        | illegal-transition      |
      | CANCELLED      | illegal-transition      |

  Scenario: Repeating a label request returns the stored labels without calling shipping-and-tracking again
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    And vendor "V-1" has requested labels for 2 packages of the purchase order of "asg-1"
    When I request labels for packages 1 to 2 of the purchase order of "asg-1" for vendor "V-1"
    Then the response status is 200
    And the response lists 2 items in "labels"
    And the response field "labels.0.trackingNumber" is "{trk:1}"
    And the response field "labels.1.trackingNumber" is "{trk:2}"
    And shipping-and-tracking received 2 IssueLabel calls
    And the IssueLabel idempotency keys were "vc-label-{po:asg-1}-1, vc-label-{po:asg-1}-2"

  Scenario: A package that is already labelled is not issued again, only the new one is
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    And vendor "V-1" has requested labels for 1 package of the purchase order of "asg-1"
    When I request labels for packages 1 to 2 of the purchase order of "asg-1" for vendor "V-1"
    Then the response status is 200
    And the response lists 2 items in "labels"
    And shipping-and-tracking was asked for packages "1, 2"

  Scenario: Labels issued before an outage are kept and the retry issues only the missing one
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    And shipping-and-tracking cannot be reached when issuing package 2
    When I request labels for packages 1 to 2 of the purchase order of "asg-1" for vendor "V-1"
    Then the response status is 503
    And the problem type is "upstream-unavailable"
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response lists 1 items in "labels"
    And the response field "labels.0.packageNo" is "1"
    Given shipping-and-tracking is reachable again
    When I request labels for packages 1 to 2 of the purchase order of "asg-1" for vendor "V-1"
    Then the response status is 200
    And the response lists 2 items in "labels"
    And shipping-and-tracking was asked for packages "1, 2, 2"

  Scenario: A refusal by shipping-and-tracking is not an outage
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    And shipping-and-tracking refuses with "ErrLabelAlreadyIssued"
    When I request labels for packages 1 to 1 of the purchase order of "asg-1" for vendor "V-1"
    Then the response status is 502
    And the problem type is "upstream-refused"

  Scenario Outline: A label request names 1 to 100 distinct packages numbered from 1
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    When I send POST "/vendor/shipping-labels" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","packages":<packages>}
      """
    Then the response status is 422
    And the problem type is "validation-failed"
    And shipping-and-tracking received 0 IssueLabel calls

    Examples:
      | packages                              |
      | []                                    |
      | [{"packageNo":0}]                     |
      | [{"packageNo":1},{"packageNo":1}]     |
      | [{"packageNo":1000000}]               |

  Scenario Outline: The most packages one label request may carry is 100
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    When I request labels for packages 1 to <packages> of the purchase order of "asg-1" for vendor "V-1"
    Then the response status is <status>
    And shipping-and-tracking received <calls> IssueLabel calls

    Examples:
      | packages | status | calls |
      | 100      | 200    | 100   |
      | 101      | 422    | 0     |

  Scenario: A label and the purchase order never carry ship-to data
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    When I request labels for packages 1 to 1 of the purchase order of "asg-1" for vendor "V-1"
    Then the response field "labels.0.labelRef" is present
    And the response field "labels.0.trackingNumber" is present
    And the response field "labels.0.carrier" is "carrier-a"
    And the response never mentions "address"
    And the response never mentions "shipTo"
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response status is 200
    And the response never mentions "address"
    And the response never mentions "shipTo"
    And the response never mentions "orderId"
    And the response never mentions "tier"

  Scenario Outline: A shipment confirmation names exactly the labelled tracking numbers
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    And vendor "V-1" has requested labels for 2 packages of the purchase order of "asg-1"
    When I send POST "/vendor/shipment-confirmations" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","shippedAt":"2026-10-22T14:00:00Z","trackingNumbers":<numbers>}
      """
    Then the response status is 409
    And the problem type is "confirm-without-label"
    And the purchase order of "asg-1" is in state "ACCEPTED"
    And there are 1 transactions

    Examples:
      | numbers                              |
      | ["{trk:1}"]                          |
      | ["{trk:1}","{trk:2}","TRK-EXTRA"]    |
      | ["{trk:1}","{trk:1}"]                |
      | ["TRK-A","TRK-B"]                    |
      | []                                   |

  Scenario: The order of the tracking numbers does not matter
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    And vendor "V-1" has requested labels for 2 packages of the purchase order of "asg-1"
    When I send POST "/vendor/shipment-confirmations" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","shippedAt":"2026-10-22T14:00:00Z","trackingNumbers":["{trk:2}","{trk:1}"]}
      """
    Then the response status is 202
    When the submission queue is processed
    Then the purchase order of "asg-1" is in state "SHIPPED"

  Scenario Outline: A shipment confirmation of an order that cannot ship is refused by its state
    Given the purchase order of "asg-1" has reached state "<state>"
    When I send POST "/vendor/shipment-confirmations" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","shippedAt":"2026-10-22T14:00:00Z","trackingNumbers":["TRK-1"]}
      """
    Then the response status is 409
    And the problem type is "<slug>"

    Examples:
      | state               | slug                  |
      | ISSUED              | confirm-without-label |
      | ACK_PROCESSING      | confirm-without-label |
      | REJECTED            | illegal-transition    |
      | EXPIRED             | illegal-transition    |
      | CANCELLED           | illegal-transition    |
      | SHIPMENT_PROCESSING | illegal-transition    |
      | SHIPPED             | illegal-transition    |

  Scenario: A shipment confirmation without shippedAt is a validation failure
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    And vendor "V-1" has requested labels for 1 package of the purchase order of "asg-1"
    When I send POST "/vendor/shipment-confirmations" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","trackingNumbers":["{trk:1}"]}
      """
    Then the response status is 422
    And the problem type is "validation-failed"
    And the purchase order of "asg-1" is in state "ACCEPTED"
    And there are 1 transactions
