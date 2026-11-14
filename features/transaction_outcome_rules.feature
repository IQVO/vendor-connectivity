Feature: How a processed transaction lands on the purchase order
  A write is a transaction reconciled later. ProcessSubmission applies the
  outcome to the order: success moves ACK_PROCESSING to ACCEPTED, REJECTED, and
  SHIPMENT_PROCESSING to SHIPPED; a failed transaction sends the order back to
  ISSUED or ACCEPTED so the vendor can try again. The acknowledgement is
  judged at receipt, not when it is processed (canvas state transitions,
  ADR 0002 A25, A26). Every scenario sets its knobs first, so there is no
  Background (a knob rebuilds the service).

  Scenario: An acknowledgement still processing past ackBy is never expired and its success stands
    Given a transaction takes 7200 seconds to process
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And vendor "V-1" has acknowledged the purchase order of "asg-1" and its transaction is still processing
    When 150 minutes pass
    Then the expiry sweep expires 0 orders
    And the purchase order of "asg-1" is in state "ACK_PROCESSING"
    When the submission queue is processed
    Then the purchase order of "asg-1" is in state "ACCEPTED"
    And 1 "PurchaseOrderAccepted" events were published
    And 0 "PurchaseOrderExpired" events were published

  Scenario: A failed acknowledgement after ackBy returns the order to ISSUED and the next sweep expires it
    Given a transaction takes 7200 seconds to process
    And the next transactions end as follows: "fail"
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And vendor "V-1" has acknowledged the purchase order of "asg-1" and its transaction is still processing
    When 150 minutes pass
    And the submission queue is processed
    Then the purchase order of "asg-1" is in state "ISSUED"
    And 0 "PurchaseOrderAccepted" events were published
    And the "SubmissionProcessed" event about "{tx}" carries "status" "FAILURE"
    When the expiry sweep expires 1 order
    Then the purchase order of "asg-1" is in state "EXPIRED"

  Scenario: A failed acknowledgement returns the order to ISSUED with no decision on record and the vendor can answer again
    Given the next transactions end as follows: "fail"
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    And the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "kind" is "ACKNOWLEDGEMENT"
    And the response field "status" is "FAILURE"
    And the response field "errors.0.code" is "ErrSimulatedFailure"
    And the purchase order of "asg-1" is in state "ISSUED"
    And 0 "PurchaseOrderAccepted" events were published
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response field "state" is "ISSUED"
    And the response field "acknowledgement" is absent
    When I send POST "/vendor/acknowledgements" with:
      """
      {"vendorId":"V-1","poNumber":"{po:asg-1}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}
      """
    Then the response status is 202
    When the submission queue is processed
    Then the purchase order of "asg-1" is in state "ACCEPTED"
    And 1 "PurchaseOrderAccepted" events were published

  Scenario: A failed shipment confirmation returns the order to ACCEPTED and the vendor can confirm again
    Given the next transactions end as follows: "ok, fail"
    And vendor "V-1" is onboarded with an acknowledgement window of 120 minutes
    And node "N-1" of vendor "V-1" has a handling time of 60 minutes
    And order-sourcing releases assignment "asg-1" to node "N-1" with required ship-by "2026-10-22T21:00:00Z" for 2 lines
    And vendor "V-1" has accepted the purchase order of "asg-1"
    And vendor "V-1" has requested labels for 1 package of the purchase order of "asg-1"
    And vendor "V-1" has confirmed the shipment of the purchase order of "asg-1"
    When the submission queue is processed
    And I send GET "/vendor/transactions/{tx}?vendorId=V-1"
    Then the response field "kind" is "SHIPMENT_CONFIRMATION"
    And the response field "status" is "FAILURE"
    And the purchase order of "asg-1" is in state "ACCEPTED"
    And 0 "VendorShipmentConfirmed" events were published
    When I send GET "/vendor/purchase-orders/{po:asg-1}?vendorId=V-1"
    Then the response field "shippedAt" is absent
    When vendor "V-1" has confirmed the shipment of the purchase order of "asg-1"
    And the submission queue is processed
    Then the purchase order of "asg-1" is in state "SHIPPED"
    And 1 "VendorShipmentConfirmed" events were published
