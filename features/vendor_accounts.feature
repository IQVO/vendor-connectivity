Feature: Vendor accounts
  A vendor account gates every write of the Vendor API. The acknowledgement
  window is bounded and a suspended vendor's writes are refused.

  Scenario: Onboarding a vendor publishes VendorOnboarded
    When I send POST "/vendors" with:
      """
      {"vendorId":"V-1","name":"Carrier One","ackWindowMinutes":120}
      """
    Then the response status is 201
    And the response field "state" is "ACTIVE"
    And the response field "ackWindowMinutes" is "120"
    And the outbox event types are "VendorOnboarded"

  Scenario: An acknowledgement window outside 1h to 24h is refused
    When I send POST "/vendors" with:
      """
      {"vendorId":"V-1","name":"Carrier One","ackWindowMinutes":30}
      """
    Then the response status is 422
    And the problem type is "ack-window-out-of-range"

  Scenario: Onboarding the same vendor again is a replay, not a second event
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "/vendors" with:
      """
      {"vendorId":"V-1","name":"Carrier V-1","ackWindowMinutes":120}
      """
    Then the response status is 200
    And the outbox event types are "VendorOnboarded"

  Scenario: A suspended vendor's writes are refused until it is reinstated
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "/vendors/V-1:suspend"
    Then the response status is 200
    And the response field "state" is "SUSPENDED"
    When I send POST "/vendor/nodes" with:
      """
      {"vendorId":"V-1","vendorNodeCode":"SITE-1","nodeType":"FULFILLMENT_CENTER","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"01000-000","countryCode":"BR"}
      """
    Then the response status is 409
    And the problem type is "vendor-suspended"
    When I send POST "/vendors/V-1:reinstate"
    Then the response status is 200
    And the response field "state" is "ACTIVE"
    And the outbox event types are "VendorOnboarded, VendorSuspended, VendorReinstated"

  Scenario: An unknown vendor is not found
    When I send GET "/vendors/V-404"
    Then the response status is 404
    And the problem type is "not-found"

  Scenario: A suspended vendor can still read its nodes and orders
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    When I send POST "/vendors/V-1:suspend"
    Then the response status is 200
    When I send GET "/vendor/nodes?vendorId=V-1"
    Then the response status is 200
    And the response lists 1 items in "nodes"

  Scenario: Suspending an already suspended vendor returns the account with no new event
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "/vendors/V-1:suspend"
    Then the response status is 200
    When I send POST "/vendors/V-1:suspend"
    Then the response status is 200
    And the response field "state" is "SUSPENDED"
    And the outbox event types are "VendorOnboarded, VendorSuspended"

  Scenario: Reinstating an active vendor returns the account with no new event
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "/vendors/V-1:reinstate"
    Then the response status is 200
    And the response field "state" is "ACTIVE"
    And the outbox event types are "VendorOnboarded"
