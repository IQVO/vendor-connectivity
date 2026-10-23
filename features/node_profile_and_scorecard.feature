Feature: Node profiles and scorecards
  The operational profile a vendor declares for one of its nodes is an
  asynchronous transaction relayed to node-topology; the node's scorecard
  is relayed synchronously from node-performance, which has nothing to
  show until the node has observations.

  Background:
    Given vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 45 minutes

  Scenario: A profile declaration is relayed to node-topology
    When I send PUT "/vendor/nodes/N-1/profile" with:
      """
      {"vendorId":"V-1","profileRevision":2,"timezone":"America/Sao_Paulo","operatingHoursByDay":{"monday":{"start":"06:00","end":"22:00"},"tuesday":{"start":"06:00","end":"22:00"}},"handlingTimeMinutes":45,"cutoffs":[{"localTime":"16:00","days":["monday","tuesday"],"maxOrders":80}],"throughputCap":{"value":600,"timeUnit":"DAY","unit":"ORDER"}}
      """
    Then the response status is 202
    When the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "SUCCESS"
    And node-topology received 1 DeclareOperationalProfile calls
    And node-topology received the profile revision 2

  Scenario: A refusal of node-topology fails the profile transaction with its code
    Given node-topology refuses with "ErrStaleProfileRevision"
    When I send PUT "/vendor/nodes/N-1/profile" with:
      """
      {"vendorId":"V-1","profileRevision":1,"timezone":"America/Sao_Paulo","operatingHoursByDay":{},"handlingTimeMinutes":45,"cutoffs":[],"throughputCap":{"value":600,"timeUnit":"DAY","unit":"ORDER"}}
      """
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "status" is "FAILURE"
    And the response field "errors.0.code" is "ErrStaleProfileRevision"

  Scenario: A profile for a node of another vendor is not found
    Given vendor "V-2" is onboarded with an acknowledgement window of 120 minutes
    When I send PUT "/vendor/nodes/N-1/profile" with:
      """
      {"vendorId":"V-2","profileRevision":1,"timezone":"America/Sao_Paulo","operatingHoursByDay":{},"handlingTimeMinutes":45,"cutoffs":[],"throughputCap":{"value":600,"timeUnit":"DAY","unit":"ORDER"}}
      """
    Then the response status is 404
    And the problem type is "node-not-owned-by-vendor"
    And node-topology received 0 DeclareOperationalProfile calls

  Scenario: A profile for an unknown node is not found
    When I send PUT "/vendor/nodes/N-404/profile" with:
      """
      {"vendorId":"V-1","profileRevision":1,"timezone":"America/Sao_Paulo","operatingHoursByDay":{},"handlingTimeMinutes":45,"cutoffs":[],"throughputCap":{"value":600,"timeUnit":"DAY","unit":"ORDER"}}
      """
    Then the response status is 404
    And the problem type is "not-found"

  Scenario: A scorecard is relayed from node-performance
    Given node-performance measured grade "A" over the last "30d"
    When I send GET "/vendor/nodes/N-1/scorecard?vendorId=V-1"
    Then the response status is 200
    And the response field "nodeId" is "N-1"
    And the response field "windows.0.window" is "30d"
    And the response field "windows.0.grade" is "A"

  Scenario: A node without observations has no scorecard
    Given node-performance has no observations yet
    When I send GET "/vendor/nodes/N-1/scorecard?vendorId=V-1"
    Then the response status is 404
    And the problem type is "not-found"

  Scenario: A node-performance outage answers upstream-unavailable
    Given node-performance cannot be reached
    When I send GET "/vendor/nodes/N-1/scorecard?vendorId=V-1"
    Then the response status is 503
    And the problem type is "upstream-unavailable"
