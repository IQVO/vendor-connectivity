Feature: Node registration and inventory transactions
  Registrations, profiles and inventory feeds are asynchronous: the vendor gets
  a transaction id and reconciles the outcome, which may fail on purpose.

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes

  Scenario: A node registration returns its nodeId on the processed transaction
    Given node-topology answers RegisterNode with node "N-9" for vendor "V-1"
    When I send POST "/vendor/nodes" with:
      """
      {"vendorId":"V-1","vendorNodeCode":"SITE-2","nodeType":"FULFILLMENT_CENTER","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"01000-000","countryCode":"BR"}
      """
    Then the response status is 202
    When the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"
    And the response field "nodeId" is "N-9"
    And node-topology received 1 RegisterNode call
    And 1 "SubmissionProcessed" events were published
    And the "SubmissionProcessed" event about "{tx}" carries "status" "SUCCESS"

  Scenario: A refusal of node-topology fails the transaction with its code
    Given node-topology refuses with "ErrDuplicateVendorNode"
    When I send POST "/vendor/nodes" with:
      """
      {"vendorId":"V-1","vendorNodeCode":"SITE-2","nodeType":"FULFILLMENT_CENTER","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"01000-000","countryCode":"BR"}
      """
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "FAILURE"
    And the response field "errors.0.code" is "ErrDuplicateVendorNode"

  Scenario: A vendor lists only its own nodes
    Given node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    And node "N-2" of vendor "V-2" has a handling time of 45 minutes
    When I send GET "/vendor/nodes?vendorId=V-1"
    Then the response status is 200
    And the response lists 1 items in "nodes"
    And the response field "nodes.0.nodeId" is "N-1"
    And the response field "nodes.0.handlingTimeMinutes" is "45"

  Scenario: Another vendor's node is not found
    Given node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    When I send GET "/vendor/nodes/N-1?vendorId=V-2"
    Then the response status is 404
    And the problem type is "node-not-owned-by-vendor"

  Scenario: An inventory feed for a known listing reaches offer-availability
    Given node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And the listing "lst-1" is on node "N-1"
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[{"listingId":"lst-1","availableQty":7,"basis":"PHYSICAL"}]}
      """
    Then the response status is 202
    When the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"
    And offer-availability received 1 inventory feed

  Scenario: A feed line for a listing of another node is refused
    Given node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And node "N-2" of vendor "V-1" has a handling time of 45 minutes
    And the listing "lst-1" is on node "N-2"
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[{"listingId":"lst-1","availableQty":7,"basis":"PHYSICAL"}]}
      """
    Then the response status is 422
    And the problem type is "validation-failed"
    And offer-availability received 0 inventory feeds

  Scenario: A node suspended by node-topology is shown as suspended
    Given node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And node "N-1" is suspended by node-topology
    When I send GET "/vendor/nodes/N-1?vendorId=V-1"
    Then the response status is 200
    And the response field "status" is "SUSPENDED"

  Scenario: An unknown transaction is not found
    When I send GET "/vendor/transactions/tx-404?vendorId=V-1"
    Then the response status is 404
    And the problem type is "not-found"

  Scenario: A transaction of another vendor is not found
    Given node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[]}
      """
    And I send GET "/vendor/transactions/{tx}?vendorId=V-2"
    Then the response status is 404
    And the problem type is "not-found"
