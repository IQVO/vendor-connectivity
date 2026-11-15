Feature: Rate limits are part of the contract
  Every /vendor/... route may answer 429 rate-limited with Retry-After before
  the use case runs (RATE_LIMIT_RPS, one token bucket per process, burst =
  max(1, rps), ADR 0002 A21). Account routes and the health probe are not
  under /vendor/. The limiter is set before the vendor exists because it
  rebuilds the service, so there is no Background.

  Scenario: A request over the limit is answered 429 with Retry-After in whole seconds
    Given the Vendor API allows 0.5 requests per second
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    When I send GET "/vendor/nodes?vendorId=V-1"
    Then the response status is 200
    When I send GET "/vendor/nodes?vendorId=V-1"
    Then the response status is 429
    And the problem type is "rate-limited"
    And the response header "Retry-After" is "2"

  Scenario: A rate-limited write never reaches the use case
    Given the Vendor API allows 0.5 requests per second
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When I send GET "/vendor/nodes?vendorId=V-1"
    And I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 429
    And the purchase order of "asg-1" is in state "ISSUED"
    And there are 0 transactions

  Scenario: The account routes and the health probe are outside the limit
    Given the Vendor API allows 0.5 requests per second
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    When I send GET "/vendor/nodes?vendorId=V-1"
    And I send GET "/vendors/V-1"
    Then the response status is 200
    When I send GET "/healthz"
    Then the response status is 200
