Feature: Labels and shipment confirmation
  A label is the one synchronous vendor write; the shipment confirmation is
  asynchronous and needs a label for every package.

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines

  Scenario: A label before acceptance is refused
    When I send POST "/vendor/shipping-labels" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","packages":[{"packageNo":1}]}
      """
    Then the response status is 409
    And the problem type is "label-before-acceptance"
    And shipping-and-tracking received 0 IssueLabel calls

  Scenario: Labels are answered synchronously with the order and tier of the release
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    When I send POST "/vendor/shipping-labels" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","packages":[{"packageNo":1},{"packageNo":2}]}
      """
    Then the response status is 200
    And the response lists 2 items in "labels"
    And the response field "labels.0.packageNo" is "1"
    And shipping-and-tracking received 2 IssueLabel calls
    And the IssueLabel call for package 1 carried order "ord-asg-1" and tier "NEXT_DAY"
    And the response never mentions "ord-asg-1"

  Scenario: A label request survives shipping-and-tracking being down for one package
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    And shipping-and-tracking cannot be reached when issuing package 2
    When I send POST "/vendor/shipping-labels" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","packages":[{"packageNo":1},{"packageNo":2}]}
      """
    Then the response status is 503
    And the problem type is "upstream-unavailable"
    Given shipping-and-tracking is reachable again
    When I send POST "/vendor/shipping-labels" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","packages":[{"packageNo":1},{"packageNo":2}]}
      """
    Then the response status is 200
    And the response lists 2 items in "labels"

  Scenario: A shipment confirmation without a label is refused
    Given vendor "V-1" has accepted the purchase order of "asg-1"
    When I send POST "/vendor/shipment-confirmations" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","shippedAt":"2026-10-22T14:00:00Z","trackingNumbers":["TRK-1"]}
      """
    Then the response status is 409
    And the problem type is "confirm-without-label"
