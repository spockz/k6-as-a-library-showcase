// This file verifies strict SLA YAML adaptation and endpoint-to-case resolution at the source boundary.
package agreement_test

import (
	"strings"
	"testing"

	"k6-as-a-library/internal/agreement"
	"k6-as-a-library/internal/dsl"
)

func TestDecodePreservesRollingConstraintsAndDurationAssumption(t *testing.T) {
	input := `agreements:
  - consumer: consumer
    provider: provider
    slo:
      - endpoint:
          host: api.example.com
          method: GET
          pathTemplate: /foo/bar/{id}
        loadConstraints:
          - amount: 400
            per-time-unit: ms
          - amount: 800
            per-time-unit: day
        responseTimes:
          - statusCode: 200
            p100: 150ms
          - statusCode: 5xx
            p100: 50ms
`
	result, err := agreement.Decode(strings.NewReader(input), "agreements.yaml", []dsl.Case{{ID: "case-a", Operation: dsl.OperationRef{Method: "GET", Path: "/foo/bar/123"}}})
	if err != nil {
		t.Fatalf("decode agreements: %v", err)
	}
	if len(result.Requirements) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	envelope := result.Requirements[0]
	if len(envelope.Constraints) != 2 || envelope.Constraints[0].Window != "1ms" || envelope.Constraints[1].Window != "24h0m0s" {
		t.Fatalf("unexpected constraints: %#v", envelope.Constraints)
	}
	if len(envelope.Scope.CaseIDs) != 1 || envelope.Scope.CaseIDs[0] != "case-a" {
		t.Fatalf("unexpected scope: %#v", envelope.Scope)
	}
	if len(envelope.ResponseTimes) != 2 || envelope.ResponseTimes[0].P100 != "150ms" || envelope.ResponseTimes[1].StatusCode != "5xx" {
		t.Fatalf("unexpected response-time objectives: %#v", envelope.ResponseTimes)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := agreement.Decode(strings.NewReader("agreements: []\nunknown: true\n"), "agreements.yaml", nil)
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("expected strict decode error, got %v", err)
	}
}
