package dsl_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"k6-as-a-library/internal/dsl"
)

func TestRequestSpecRuntimeDefaultsAreIdentityAndMatch(t *testing.T) {
	t.Parallel()

	original := dsl.RequestSpec{
		Method:  "POST",
		Path:    "/items",
		Query:   []dsl.Parameter{{Name: "page", Value: "1"}},
		Headers: []dsl.Header{{Name: "Accept", Values: []string{"application/json"}}},
		Cookies: []dsl.Cookie{{Name: "session", Value: "one"}},
		Body:    &dsl.Payload{Encoding: dsl.PayloadEncodingJSON, Content: `{"id":1}`, ContentPresence: dsl.PresenceValue},
	}

	materialized, err := original.Materialize(t.Context())
	if err != nil {
		t.Fatalf("materialize hand-written request: %v", err)
	}
	materialized.Query[0].Value = "2"
	materialized.Headers[0].Values[0] = "text/plain"
	materialized.Cookies[0].Value = "two"
	materialized.Body.Content = `{"id":2}`
	if original.Query[0].Value != "1" || original.Headers[0].Values[0] != "application/json" || original.Cookies[0].Value != "one" || original.Body.Content != `{"id":1}` {
		t.Fatalf("materialization mutated original request: %#v", original)
	}

	result, err := materialized.Match(t.Context(), nil)
	if err != nil {
		t.Fatalf("match hand-written request: %v", err)
	}
	if !result.Matched {
		t.Fatalf("hand-written request match = %#v, want success", result)
	}
}

func TestRequestSpecRuntimeMaterializesAndMatches(t *testing.T) {
	t.Parallel()

	matcherCalled := false
	request := dsl.RequestSpec{Method: "GET", Path: "/items"}.WithRuntime(dsl.RequestRuntime{
		Materialize: func(_ context.Context, input dsl.RequestSpec) (dsl.RequestSpec, error) {
			input.Query = append(input.Query, dsl.Parameter{Name: "generated", Value: "yes"})
			input.Headers = append(input.Headers, dsl.Header{Name: "X-Generated", Values: []string{"yes"}})
			return input, nil
		},
		Match: func(_ context.Context, response *dsl.HTTPResponse) (dsl.MatchResult, error) {
			matcherCalled = true
			if response == nil || response.StatusCode != 202 {
				return dsl.MatchResult{Matched: false, Kind: "status", MismatchCount: 1, Mismatch: errors.New("expected status 202")}, nil
			}
			return dsl.MatchResult{Matched: true, ActualStatus: response.StatusCode}, nil
		},
	}, dsl.BehaviorDescription{
		Materialization: []string{"Add a generated query parameter and header."},
		Matching:        []string{"Require HTTP status 202."},
	})

	materialized, err := request.Materialize(t.Context())
	if err != nil {
		t.Fatalf("materialize request: %v", err)
	}
	if len(materialized.Query) != 1 || materialized.Query[0].Value != "yes" {
		t.Fatalf("materialized query = %#v", materialized.Query)
	}
	if len(request.Query) != 0 || len(request.Headers) != 0 {
		t.Fatalf("runtime behavior mutated source request: %#v", request)
	}
	result, err := materialized.Match(t.Context(), &dsl.HTTPResponse{StatusCode: 202})
	if err != nil {
		t.Fatalf("match materialized request: %v", err)
	}
	if !matcherCalled || !result.Matched {
		t.Fatalf("match result = %#v, matcher called = %t", result, matcherCalled)
	}
}

func TestRequestSpecJSONKeepsDescriptionsAndDropsRuntime(t *testing.T) {
	t.Parallel()

	materializerCalled := false
	matcherCalled := false
	request := dsl.RequestSpec{Method: "GET", Path: "/items", Redirects: dsl.RedirectNone}.WithRuntime(dsl.RequestRuntime{
		Materialize: func(_ context.Context, input dsl.RequestSpec) (dsl.RequestSpec, error) {
			materializerCalled = true
			return input, nil
		},
		Match: func(_ context.Context, _ *dsl.HTTPResponse) (dsl.MatchResult, error) {
			matcherCalled = true
			return dsl.MatchResult{Matched: false}, nil
		},
	}, dsl.BehaviorDescription{
		Materialization: []string{"Generate an identifier."},
		Matching:        []string{"Match the generated identifier."},
	})

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var decoded dsl.RequestSpec
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if decoded.Behavior == nil || len(decoded.Behavior.Materialization) != 1 || len(decoded.Behavior.Matching) != 1 {
		t.Fatalf("decoded behavior description = %#v", decoded.Behavior)
	}
	if _, err := decoded.Materialize(t.Context()); err != nil {
		t.Fatalf("materialize decoded request: %v", err)
	}
	result, err := decoded.Match(t.Context(), nil)
	if err != nil {
		t.Fatalf("match decoded request: %v", err)
	}
	if materializerCalled || matcherCalled || !result.Matched {
		t.Fatalf("decoded runtime behavior: materializer=%t matcher=%t result=%#v", materializerCalled, matcherCalled, result)
	}
}

func TestRequestSpecMaterializePreservesCallbackError(t *testing.T) {
	t.Parallel()

	expected := errors.New("generator failed")
	request := dsl.RequestSpec{}.WithRuntime(dsl.RequestRuntime{
		Materialize: func(context.Context, dsl.RequestSpec) (dsl.RequestSpec, error) {
			return dsl.RequestSpec{}, expected
		},
	}, dsl.BehaviorDescription{Materialization: []string{"Fail for testing."}})
	_, err := request.Materialize(t.Context())
	if !errors.Is(err, expected) {
		t.Fatalf("materialize error = %v, want wrapped sentinel", err)
	}
}
