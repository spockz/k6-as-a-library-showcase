// This file verifies strict SLA YAML adaptation and endpoint-to-case resolution at the source boundary.
package agreement_test

import (
	"os"
	"path/filepath"
	"slices"
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
            permittedFailures:
              - category: transport
                amount: 2
              - category: http_5xx
                amount: 4
              - category: functional
                amount: 8
                statusCodes: [400, 404, 409, 422]
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
	failures := envelope.Constraints[1].PermittedFailures
	if len(failures) != 3 || failures[0].Category != dsl.FailureCategoryTransport || failures[0].Amount != 2 || failures[1].Category != dsl.FailureCategoryHTTP5xx || failures[2].Category != dsl.FailureCategoryFunctional || !slices.Equal(failures[2].StatusCodes, []string{"400", "404", "409", "422"}) {
		t.Fatalf("unexpected permitted failures: %#v", failures)
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

func TestAgreementExamplesDecodeForDefaultDirectRequest(t *testing.T) {
	filenames, err := filepath.Glob(filepath.Join("..", "..", "examples", "agreements", "*.yaml"))
	if err != nil {
		t.Fatalf("find agreement examples: %v", err)
	}
	if len(filenames) != 3 {
		t.Fatalf("agreement example count = %d, want 3", len(filenames))
	}

	cases := []dsl.Case{{ID: "direct-request", Operation: dsl.OperationRef{Method: "GET", Path: "/headers"}}}
	for _, filename := range filenames {
		t.Run(filepath.Base(filename), func(t *testing.T) {
			contents, err := os.ReadFile(filename)
			if err != nil {
				t.Fatalf("read agreement example: %v", err)
			}
			result, err := agreement.Decode(strings.NewReader(string(contents)), filename, cases)
			if err != nil {
				t.Fatalf("decode agreement example: %v", err)
			}
			if len(result.Requirements) != 1 {
				t.Fatalf("load requirements = %d, want 1", len(result.Requirements))
			}
		})
	}
}
