Feature: Asynchronous transactions, neighbour outages and retention
  A submission is processed at or after notBefore = receivedAt +
  TX_PROCESSING_DELAY. A neighbour refusal (4xx) ends it in FAILURE with the
  neighbour's Err code; an outage (5xx, timeout) leaves it PROCESSING for the
  next pass, and after SUBMISSION_MAX_ATTEMPTS (5) it ends FAILURE with
  ErrUpstreamUnavailable. The relayed call carries the transaction id as its
  idempotency key; a refused write at receipt creates no transaction;
  processed submissions are kept 30 days (use-cases ProcessSubmission, canvas
  Submission, ADR 0002 A10, A11, A12, A22). No Background: the knobs and the
  outages are part of each set-up.

  Scenario: A transaction is processed at exactly its notBefore and not a second earlier
    Given a transaction takes 30 seconds to process
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[]}
      """
    And 29 seconds pass
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "PROCESSING"
    When 1 second passes
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"

  Scenario: A node registration waits while node-topology is down and completes once it is back
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node-topology cannot be reached
    When I send POST "/vendor/nodes" with:
      """
      {"vendorId":"V-1","vendorNodeCode":"SITE-2","nodeType":"FULFILLMENT_CENTER","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"01000-000","countryCode":"BR"}
      """
    Then the response status is 202
    When the submission queue is processed 2 times
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "PROCESSING"
    And node-topology received 2 RegisterNode calls
    And 0 "SubmissionProcessed" events were published
    Given node-topology answers RegisterNode with node "N-9" for vendor "V-1"
    And node-topology is reachable again
    When the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"
    And the response field "nodeId" is "N-9"
    And node-topology received 3 RegisterNode calls
    And every call to node-topology carried the transaction id as its key
    And 1 "SubmissionProcessed" events were published

  Scenario: After the fifth pass without an answer the transaction fails with ErrUpstreamUnavailable
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node-topology cannot be reached
    When I send POST "/vendor/nodes" with:
      """
      {"vendorId":"V-1","vendorNodeCode":"SITE-2","nodeType":"FULFILLMENT_CENTER","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"01000-000","countryCode":"BR"}
      """
    And the submission queue is processed 4 times
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "PROCESSING"
    When the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "FAILURE"
    And the response field "errors.0.code" is "ErrUpstreamUnavailable"
    And node-topology received 5 RegisterNode calls
    And the "SubmissionProcessed" event about "{tx}" carries "status" "FAILURE"
    When the submission queue is processed
    Then node-topology received 5 RegisterNode calls

  Scenario: An inventory feed rides out an offer-availability outage under one feed id
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And the listing "lst-1" is on node "N-1"
    And offer-availability cannot be reached
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[{"listingId":"lst-1","availableQty":7,"basis":"PHYSICAL"}]}
      """
    And the submission queue is processed 2 times
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "PROCESSING"
    Given offer-availability is reachable again
    When the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"
    And offer-availability received 3 inventory feeds
    And every call to offer-availability carried the transaction id as its key

  Scenario: A feed offer-availability already applied counts as a success
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And offer-availability refuses with "ErrFeedAlreadyApplied"
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[]}
      """
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"

  Scenario: A refusal of offer-availability fails the inventory transaction once, with its code
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And offer-availability refuses with "ErrListingNotOnNode"
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[{"listingId":"lst-9","availableQty":7,"basis":"PHYSICAL"}]}
      """
    And the submission queue is processed 2 times
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "FAILURE"
    And the response field "errors.0.code" is "ErrListingNotOnNode"
    And offer-availability received 1 inventory feed
    And the "SubmissionProcessed" event about "{tx}" carries "errorCount" "1"

  Scenario: A transaction that fails on purpose never reaches the neighbour
    Given every transaction fails on purpose
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[]}
      """
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "FAILURE"
    And the response field "errors.0.code" is "ErrSimulatedFailure"
    And offer-availability received 0 inventory feeds

  Scenario: A registered node is usable as soon as its transaction succeeded
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node-topology answers RegisterNode with node "N-9" for vendor "V-1"
    When I send POST "/vendor/nodes" with:
      """
      {"vendorId":"V-1","vendorNodeCode":"SITE-2","nodeType":"FULFILLMENT_CENTER","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"01000-000","countryCode":"BR"}
      """
    And the submission queue is processed
    And I send GET "/vendor/nodes/N-9?vendorId=V-1"
    Then the response status is 200
    And the response field "status" is "PROPOSED"
    And the response field "vendorNodeCode" is "SITE-2"
    When I send PUT "/vendor/nodes/N-9/profile" with:
      """
      {"vendorId":"V-1","profileRevision":1,"timezone":"America/Sao_Paulo","operatingHoursByDay":{},"handlingTimeMinutes":45,"cutoffs":[],"throughputCap":{"value":600,"timeUnit":"DAY","unit":"ORDER"}}
      """
    Then the response status is 202

  Scenario Outline: A vendor registers only fulfillment, cross-dock and returns nodes
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "/vendor/nodes" with:
      """
      {"vendorId":"V-1","vendorNodeCode":"SITE-2","nodeType":"<type>","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"01000-000","countryCode":"BR"}
      """
    Then the response status is <status>
    And there are <transactions> transactions

    Examples:
      | type                | status | transactions |
      | FULFILLMENT_CENTER  | 202    | 1            |
      | INBOUND_CROSS_DOCK  | 202    | 1            |
      | RETURNS_CENTER      | 202    | 1            |
      | SORTATION_CENTER    | 422    | 0            |
      | DELIVERY_STATION    | 422    | 0            |
      | AIR_HUB             | 422    | 0            |

  Scenario Outline: An inventory feed that breaks a rule is refused at receipt
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false<asOf>,"lines":<lines>}
      """
    Then the response status is 422
    And the problem type is "validation-failed"
    And there are 0 transactions
    And offer-availability received 0 inventory feeds

    Examples:
      | asOf                              | lines                                                                                                                         |
      | ,"asOf":"2026-10-22T12:55:00Z"    | [{"listingId":"lst-1","availableQty":-1,"basis":"PHYSICAL"}]                                                                  |
      | ,"asOf":"2026-10-22T12:55:00Z"    | [{"listingId":"lst-1","availableQty":1,"basis":"PHYSICAL"},{"listingId":"lst-1","availableQty":2,"basis":"PHYSICAL"}]        |
      | ,"asOf":"2026-10-22T12:55:00Z"    | [{"listingId":"lst-1","availableQty":1,"basis":"MAYBE"}]                                                                      |
      |                                   | []                                                                                                                            |

  Scenario: A listing the view does not know yet passes through for offer-availability to judge
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[{"listingId":"lst-new","availableQty":0,"basis":"UNKNOWN"}]}
      """
    Then the response status is 202
    When the submission queue is processed
    Then offer-availability received 1 inventory feed

  Scenario Outline: An inventory feed names a node of the vendor
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    And vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"<vendor>","nodeId":"<node>","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[]}
      """
    Then the response status is 404
    And the problem type is "<slug>"
    And there are 0 transactions

    Examples:
      | vendor | node  | slug                     |
      | V-2    | N-1   | node-not-owned-by-vendor |
      | V-1    | N-404 | not-found                |

  Scenario: A processed transaction is kept for 30 days and then purged
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes
    When I send POST "/vendor/inventory" with:
      """
      {"vendorId":"V-1","nodeId":"N-1","fullUpdate":false,"asOf":"2026-10-22T12:55:00Z","lines":[]}
      """
    And the submission queue is processed
    And 29 days pass
    And the retention sweep runs
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response status is 200
    And the response field "status" is "SUCCESS"
    When 2 days pass
    And the retention sweep runs
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response status is 404
    And the problem type is "not-found"

  Scenario: A transaction still processing is never purged
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node-topology cannot be reached
    When I send POST "/vendor/nodes" with:
      """
      {"vendorId":"V-1","vendorNodeCode":"SITE-2","nodeType":"FULFILLMENT_CENTER","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"01000-000","countryCode":"BR"}
      """
    And 40 days pass
    And the retention sweep runs
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response status is 200
    And the response field "status" is "PROCESSING"
