Feature: Simulation knobs
  The Vendor API can be told to fail or delay its transactions so a vendor
  integration can be exercised against a hostile network. The knobs are set
  before the vendor exists because they rebuild the service.

  Scenario: A transaction can be made to fail on purpose
    Given every transaction fails on purpose
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[{"listingId":"lst-1","availableQty":7,"basis":"PHYSICAL"}]}
      """
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "FAILURE"
    And the response field "errors.0.code" is "ErrSimulatedFailure"

  Scenario: A transaction stays PROCESSING for the configured delay
    Given a transaction takes 30 seconds to process
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[{"listingId":"lst-1","availableQty":7,"basis":"PHYSICAL"}]}
      """
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "PROCESSING"
    When 31 seconds pass
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"
