// This file keeps DSL contract tests separate so model and wire compatibility stay observable.
package dsl_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"k6-as-a-library/internal/dsl"
)

func TestAttributeSetProvidesNamedLookupAndOverrides(t *testing.T) {
	base := dsl.AttributeSet{
		{Name: "tenant", Value: "case"},
		{Name: "empty", Value: ""},
	}
	if value, found := base.Get("TENANT"); !found || value != "case" {
		t.Fatalf("case-insensitive lookup = %q, %t", value, found)
	}
	if value, found := base.Get("empty"); !found || value != "" {
		t.Fatalf("empty attribute lookup = %q, %t", value, found)
	}
	if _, found := base.Get("missing"); found {
		t.Fatal("missing attribute was reported as present")
	}
	merged := base.WithOverrides(dsl.AttributeSet{
		{Name: "TENANT", Value: "segment"},
		{Name: "phase", Value: "steady"},
	})
	if value, _ := merged.Get("tenant"); value != "segment" {
		t.Fatalf("overridden tenant = %q", value)
	}
	if value, _ := base.Get("tenant"); value != "case" {
		t.Fatalf("override mutated source set: %q", value)
	}
	if names := merged.Names(); !slices.Equal(names, []string{"empty", "phase", "TENANT"}) {
		t.Fatalf("attribute names = %v", names)
	}
}

func TestJSONPresenceDistinguishesMissingNullAndEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantState   dsl.Presence
		wantBody    bool
		wantContent string
	}{
		{name: "missing", input: `{"method":"POST","path":"/items"}`, wantState: dsl.PresenceAbsent},
		{name: "null", input: `{"method":"POST","path":"/items","body":null}`, wantState: dsl.PresenceNull},
		{name: "empty", input: `{"method":"POST","path":"/items","body":{"encoding":"text","content":""}}`, wantState: dsl.PresenceValue, wantBody: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var request dsl.RequestSpec
			if err := json.Unmarshal([]byte(test.input), &request); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if request.BodyPresence != test.wantState {
				t.Fatalf("body presence: expected %v, got %v", test.wantState, request.BodyPresence)
			}
			if (request.Body != nil) != test.wantBody {
				t.Fatalf("body pointer presence: expected %t, got %t", test.wantBody, request.Body != nil)
			}
			if test.wantBody && request.Body.Content != test.wantContent {
				t.Fatalf("body content: expected %q, got %q", test.wantContent, request.Body.Content)
			}
		})
	}

	var absent, null, empty dsl.ResponseExpectation
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatalf("decode absent expectation: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"body":null}`), &null); err != nil {
		t.Fatalf("decode null expectation: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"body":{}}`), &empty); err != nil {
		t.Fatalf("decode empty expectation: %v", err)
	}
	if absent.BodyPresence != dsl.PresenceAbsent || null.BodyPresence != dsl.PresenceNull || empty.BodyPresence != dsl.PresenceValue {
		t.Fatalf("body expectation states were not preserved: absent=%v null=%v empty=%v", absent.BodyPresence, null.BodyPresence, empty.BodyPresence)
	}
	if empty.Body == nil {
		t.Fatal("explicit empty body expectation was lost")
	}

	for _, test := range []struct {
		name        string
		request     dsl.RequestSpec
		mustContain string
		mustOmit    string
	}{
		{name: "absent", request: dsl.RequestSpec{Method: "GET", Path: "/items"}, mustOmit: `"body"`},
		{name: "null", request: dsl.RequestSpec{Method: "GET", Path: "/items", BodyPresence: dsl.PresenceNull}, mustContain: `"body":null`},
		{name: "empty", request: dsl.RequestSpec{Method: "POST", Path: "/items", Body: &dsl.Payload{Encoding: dsl.PayloadEncodingText, Content: ""}}, mustContain: `"body":{"encoding":"text","content":""`},
	} {
		t.Run("marshal-"+test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.request)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			text := string(encoded)
			if test.mustContain != "" && !strings.Contains(text, test.mustContain) {
				t.Fatalf("serialized request lacks %q: %s", test.mustContain, text)
			}
			if test.mustOmit != "" && strings.Contains(text, test.mustOmit) {
				t.Fatalf("serialized request unexpectedly contains %q: %s", test.mustOmit, text)
			}
		})
	}

	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "missing header values", input: `{"name":"X-Test"}`, want: `{"name":"X-Test"}`},
		{name: "null header values", input: `{"name":"X-Test","values":null}`, want: `{"name":"X-Test","values":null}`},
		{name: "empty header values", input: `{"name":"X-Test","values":[]}`, want: `{"name":"X-Test","values":[]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var header dsl.Header
			if err := json.Unmarshal([]byte(test.input), &header); err != nil {
				t.Fatalf("decode header: %v", err)
			}
			encoded, err := json.Marshal(header)
			if err != nil {
				t.Fatalf("encode header: %v", err)
			}
			if string(encoded) != test.want {
				t.Fatalf("header presence changed: expected %s, got %s", test.want, encoded)
			}
		})
	}
}

func TestResponseExpectationNestedPresenceSurvivesJSONNormalizationAndValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		state      dsl.Presence
		isNil      bool
		length     int
		stateOf    func(*dsl.ResponseExpectation) dsl.Presence
		valuesOf   func(*dsl.ResponseExpectation) []string
		matchersOf func(*dsl.ResponseExpectation) []dsl.Matcher
	}{
		{
			name: "header values missing", input: `{"headers":[{"name":"X-Test"}]}`,
			state: dsl.PresenceAbsent, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Headers[0].ValuesPresence
			}, valuesOf: func(expectation *dsl.ResponseExpectation) []string { return expectation.Headers[0].Values },
		},
		{
			name: "header values null", input: `{"headers":[{"name":"X-Test","values":null}]}`,
			state: dsl.PresenceNull, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Headers[0].ValuesPresence
			}, valuesOf: func(expectation *dsl.ResponseExpectation) []string { return expectation.Headers[0].Values },
		},
		{
			name: "header values empty", input: `{"headers":[{"name":"X-Test","values":[]}]}`,
			state: dsl.PresenceValue, length: 0, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Headers[0].ValuesPresence
			}, valuesOf: func(expectation *dsl.ResponseExpectation) []string { return expectation.Headers[0].Values },
		},
		{
			name: "header matchers missing", input: `{"headers":[{"name":"X-Test"}]}`,
			state: dsl.PresenceAbsent, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Headers[0].MatchersPresence
			}, matchersOf: func(expectation *dsl.ResponseExpectation) []dsl.Matcher { return expectation.Headers[0].Matchers },
		},
		{
			name: "header matchers null", input: `{"headers":[{"name":"X-Test","matchers":null}]}`,
			state: dsl.PresenceNull, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Headers[0].MatchersPresence
			}, matchersOf: func(expectation *dsl.ResponseExpectation) []dsl.Matcher { return expectation.Headers[0].Matchers },
		},
		{
			name: "header matchers empty", input: `{"headers":[{"name":"X-Test","matchers":[]}]}`,
			state: dsl.PresenceValue, length: 0, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Headers[0].MatchersPresence
			}, matchersOf: func(expectation *dsl.ResponseExpectation) []dsl.Matcher { return expectation.Headers[0].Matchers },
		},
		{
			name: "cookie values missing", input: `{"cookies":[{"name":"session"}]}`,
			state: dsl.PresenceAbsent, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Cookies[0].ValuesPresence
			}, valuesOf: func(expectation *dsl.ResponseExpectation) []string { return expectation.Cookies[0].Values },
		},
		{
			name: "cookie values null", input: `{"cookies":[{"name":"session","values":null}]}`,
			state: dsl.PresenceNull, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Cookies[0].ValuesPresence
			}, valuesOf: func(expectation *dsl.ResponseExpectation) []string { return expectation.Cookies[0].Values },
		},
		{
			name: "cookie values empty", input: `{"cookies":[{"name":"session","values":[]}]}`,
			state: dsl.PresenceValue, length: 0, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Cookies[0].ValuesPresence
			}, valuesOf: func(expectation *dsl.ResponseExpectation) []string { return expectation.Cookies[0].Values },
		},
		{
			name: "cookie matchers missing", input: `{"cookies":[{"name":"session"}]}`,
			state: dsl.PresenceAbsent, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Cookies[0].MatchersPresence
			}, matchersOf: func(expectation *dsl.ResponseExpectation) []dsl.Matcher { return expectation.Cookies[0].Matchers },
		},
		{
			name: "cookie matchers null", input: `{"cookies":[{"name":"session","matchers":null}]}`,
			state: dsl.PresenceNull, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Cookies[0].MatchersPresence
			}, matchersOf: func(expectation *dsl.ResponseExpectation) []dsl.Matcher { return expectation.Cookies[0].Matchers },
		},
		{
			name: "cookie matchers empty", input: `{"cookies":[{"name":"session","matchers":[]}]}`,
			state: dsl.PresenceValue, length: 0, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Cookies[0].MatchersPresence
			}, matchersOf: func(expectation *dsl.ResponseExpectation) []dsl.Matcher { return expectation.Cookies[0].Matchers },
		},
		{
			name: "body matchers missing", input: `{"body":{}}`,
			state: dsl.PresenceAbsent, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Body.MatchersPresence
			}, matchersOf: func(expectation *dsl.ResponseExpectation) []dsl.Matcher { return expectation.Body.Matchers },
		},
		{
			name: "body matchers null", input: `{"body":{"matchers":null}}`,
			state: dsl.PresenceNull, isNil: true, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Body.MatchersPresence
			}, matchersOf: func(expectation *dsl.ResponseExpectation) []dsl.Matcher { return expectation.Body.Matchers },
		},
		{
			name: "body matchers empty", input: `{"body":{"matchers":[]}}`,
			state: dsl.PresenceValue, length: 0, stateOf: func(expectation *dsl.ResponseExpectation) dsl.Presence {
				return expectation.Body.MatchersPresence
			}, matchersOf: func(expectation *dsl.ResponseExpectation) []dsl.Matcher { return expectation.Body.Matchers },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var expectation dsl.ResponseExpectation
			if err := json.Unmarshal([]byte(test.input), &expectation); err != nil {
				t.Fatalf("decode expectation: %v", err)
			}
			plan := testBenchmark()
			plan.Cases[0].Expectation = &expectation
			normalized := plan.Normalize()
			got := normalized.Cases[0].Expectation
			if got == nil || test.stateOf(got) != test.state {
				t.Fatalf("presence changed during normalization: expected %v, got %#v", test.state, got)
			}
			if test.valuesOf != nil {
				values := test.valuesOf(got)
				if (values == nil) != test.isNil || len(values) != test.length {
					t.Fatalf("values changed during normalization: nil=%t length=%d", values == nil, len(values))
				}
			}
			if test.matchersOf != nil {
				matchers := test.matchersOf(got)
				if (matchers == nil) != test.isNil || len(matchers) != test.length {
					t.Fatalf("matchers changed during normalization: nil=%t length=%d", matchers == nil, len(matchers))
				}
			}
			if err := dsl.Validate(normalized); err != nil {
				t.Fatalf("validate normalized expectation: %v", err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("encode normalized expectation: %v", err)
			}
			if string(encoded) != test.input {
				t.Fatalf("presence changed during encoding: expected %s, got %s", test.input, encoded)
			}
		})
	}
}

func TestValidationRejectsInconsistentNestedPresence(t *testing.T) {
	t.Parallel()

	plan := testBenchmark()
	plan.Cases[0].Expectation = &dsl.ResponseExpectation{
		Headers: []dsl.HeaderExpectation{{
			Name:             "X-Test",
			ValuesPresence:   dsl.PresenceValue,
			MatchersPresence: dsl.PresenceValue,
		}},
		Cookies: []dsl.CookieExpectation{{
			Name:             "session",
			ValuesPresence:   dsl.PresenceValue,
			MatchersPresence: dsl.PresenceValue,
		}},
		Body: &dsl.BodyExpectation{MatchersPresence: dsl.PresenceValue},
	}

	err := dsl.Validate(plan)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, fragment := range []string{
		"header values are marked present but have no value",
		"header matchers are marked present but have no value",
		"cookie values are marked present but have no value",
		"cookie matchers are marked present but have no value",
		"body matchers are marked present but have no value",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("validation error is missing %q: %s", fragment, err)
		}
	}
}

func TestMarshalBenchmarkManifestIsDeterministicAndPreservesRepeatedValues(t *testing.T) {
	t.Parallel()

	model := testBenchmark()
	model.Cases[0].Request.Query = []dsl.Parameter{
		{Name: "z", Value: "last"},
		{Name: "a", Value: "first"},
		{Name: "a", Value: "second"},
	}
	model.Cases[0].Request.Headers = []dsl.Header{
		{Name: "X-Trace", Values: []string{"one", "two"}},
	}
	model.Cases[0].Request.Body = &dsl.Payload{Encoding: dsl.PayloadEncodingText, Content: ""}

	first, err := dsl.MarshalBenchmarkManifest(model)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	second, err := dsl.MarshalBenchmarkManifest(model)
	if err != nil {
		t.Fatalf("marshal plan a second time: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("repeated serialization differs:\n%s\n---\n%s", first, second)
	}
	text := string(first)
	for _, fragment := range []string{
		`"body": {`,
		`"content": ""`,
		`"values": [`,
		`"one",`,
		`"value": "first"`,
		`"value": "second"`,
	} {
		if !strings.Contains(text, fragment) {
			t.Errorf("serialized plan is missing %q:\n%s", fragment, text)
		}
	}

	decoded, err := dsl.UnmarshalBenchmarkManifest(first)
	if err != nil {
		t.Fatalf("round-trip plan: %v", err)
	}
	if decoded.Cases[0].Request.Body == nil || decoded.Cases[0].Request.Body.Content != "" {
		t.Fatal("round-trip lost explicit empty request body")
	}
	if got := decoded.Cases[0].Request.Query; len(got) != 3 || got[0].Value != "first" || got[1].Value != "second" || got[2].Value != "last" {
		t.Fatalf("round-trip changed repeated query order: %#v", got)
	}
	if got := decoded.Cases[0].Request.Headers[0].Values; len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("round-trip changed repeated header values: %#v", got)
	}
	third, err := dsl.MarshalBenchmarkManifest(decoded)
	if err != nil {
		t.Fatalf("marshal normalized round-trip: %v", err)
	}
	if !bytes.Equal(first, third) {
		t.Fatalf("normalized round-trip is not deterministic:\n%s\n---\n%s", first, third)
	}
}

func TestMarshalBenchmarkManifestMatchesGoldenSnapshot(t *testing.T) {
	t.Parallel()

	model := testBenchmark()
	model.Cases[0].Request.Query = []dsl.Parameter{
		{Name: "z", Value: "last"},
		{Name: "a", Value: "first"},
		{Name: "a", Value: "second"},
	}
	model.Cases[0].Request.Headers = []dsl.Header{{Name: "X-Trace", Values: []string{"one", "two"}}}
	model.Cases[0].Request.Body = &dsl.Payload{Encoding: dsl.PayloadEncodingText, Content: ""}
	got, err := dsl.MarshalBenchmarkManifest(model)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	want, err := os.ReadFile("testdata/benchmark-manifest.golden.json")
	if err != nil {
		t.Fatalf("read golden plan: %v", err)
	}
	want = bytes.TrimSuffix(want, []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("golden plan differs:\n%s", got)
	}
}

func TestCloneDoesNotShareMutableModelStorage(t *testing.T) {
	t.Parallel()

	original := testBenchmark()
	clone := original.Clone()
	clone.Cases[0].Request.Query[0].Value = "changed"
	clone.Cases[0].Attributes[0].Value = "changed"
	clone.Cases[0].Check.Enabled = false
	clone.Segments = append(clone.Segments, dsl.Segment{ID: "new"})
	if original.Cases[0].Request.Query[0].Value == "changed" || original.Cases[0].Attributes[0].Value == "changed" || !original.Cases[0].Check.Enabled {
		t.Fatal("clone mutation changed the original plan")
	}
	if len(original.Segments) != 1 {
		t.Fatal("clone append changed the original segment slice")
	}
}

func TestValidationReportsContextAndUnsupportedValues(t *testing.T) {
	t.Parallel()

	model := testBenchmark()
	model.Cases[0].Request.Method = "BAD METHOD"
	model.Cases[0].Operation.Method = "BAD METHOD"
	model.Cases[0].Request.Body = &dsl.Payload{Encoding: dsl.PayloadEncodingJSON, Content: "{"}
	model.Cases[0].Expectation = &dsl.ResponseExpectation{
		Status: &dsl.StatusExpectation{Equals: 700},
		Body:   &dsl.BodyExpectation{Matchers: []dsl.Matcher{{Path: "$.body", Kind: dsl.MatcherRegex, Pattern: "["}}},
	}
	err := dsl.Validate(model)
	if err == nil {
		t.Fatal("expected validation error")
	}
	var validation *dsl.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected typed validation error, got %T: %v", err, err)
	}
	message := err.Error()
	for _, fragment := range []string{"plan \"example\"", "case \"case-a\"", "request.body.content", "invalid", "unsupported"} {
		if !strings.Contains(message, fragment) {
			t.Errorf("validation error is missing %q: %s", fragment, message)
		}
	}
}

func TestUnknownJSONFieldsAreRejected(t *testing.T) {
	t.Parallel()

	var request dsl.RequestSpec
	err := json.Unmarshal([]byte(`{"method":"GET","path":"/","unexpected":true}`), &request)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
	err = json.Unmarshal([]byte(`{"method":"POST","path":"/","body":{"encoding":"text","content":"","future":true}}`), &request)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected nested unknown-field error, got %v", err)
	}
}

func TestNormalizationCanonicalizesMatchersAndEnforcesReportAllowlist(t *testing.T) {
	t.Parallel()

	model := testBenchmark()
	model.Cases[0].Expectation = &dsl.ResponseExpectation{
		Body: &dsl.BodyExpectation{Matchers: []dsl.Matcher{
			{Path: "$.b", Kind: "regexp", Pattern: "value"},
			{Path: "$.a", Kind: "equalto", Value: "one"},
		}},
	}
	normalized := model.Normalize()
	matchers := normalized.Cases[0].Expectation.Body.Matchers
	if len(matchers) != 2 || matchers[0].Path != "$.a" || matchers[0].Kind != dsl.MatcherEquality || matchers[1].Kind != dsl.MatcherRegex {
		t.Fatalf("matcher normalization was not deterministic: %#v", matchers)
	}
	empty, err := json.Marshal(dsl.ResponseExpectation{Body: &dsl.BodyExpectation{}})
	if err != nil {
		t.Fatalf("marshal empty body expectation: %v", err)
	}
	if string(empty) != `{"body":{}}` {
		t.Fatalf("empty body expectation was normalized unexpectedly: %s", empty)
	}

	model.Report = dsl.ReportSpec{
		GroupBy: []string{"unapproved_dimension"},
	}
	err = dsl.Validate(model)
	if err == nil || !strings.Contains(err.Error(), "not declared by any case or segment") {
		t.Fatalf("expected unavailable report grouping error, got %v", err)
	}
}

func TestReportGroupByPresenceSurvivesJSONRoundTrip(t *testing.T) {
	t.Parallel()

	model := testBenchmark()
	model.Report = dsl.ReportSpec{GroupBy: []string{}}
	encoded, err := dsl.MarshalBenchmarkManifest(model)
	if err != nil {
		t.Fatalf("marshal plan with empty report dimensions: %v", err)
	}
	decoded, err := dsl.UnmarshalBenchmarkManifest(encoded)
	if err != nil {
		t.Fatalf("unmarshal plan with empty report dimensions: %v", err)
	}
	if decoded.Report.GroupBy == nil || len(decoded.Report.GroupBy) != 0 {
		t.Fatalf("empty report group-by became defaults: %#v", decoded.Report.GroupBy)
	}
	for _, input := range []string{
		`{"groupBy":null}`,
		`{"groupBy":[]}`,
	} {
		var report dsl.ReportSpec
		if err := json.Unmarshal([]byte(input), &report); err != nil {
			t.Fatalf("decode report %s: %v", input, err)
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("encode report %s: %v", input, err)
		}
		if string(encoded) != input {
			t.Fatalf("report presence changed: expected %s, got %s", input, encoded)
		}
	}
}

func TestManifestRejectsVersionOneVocabulary(t *testing.T) {
	_, err := dsl.UnmarshalBenchmarkManifest([]byte(`{"schemaVersion":1,"cases":[{"labels":[]} ]}`))
	if err == nil || !strings.Contains(err.Error(), "unsupported schema version 1") {
		t.Fatalf("version 1 manifest error = %v", err)
	}
}

func TestProvenanceKindsAreSourceDefined(t *testing.T) {
	model := testBenchmark()
	model.Cases[0].Source.Kind = "custom-contract-format"
	if err := dsl.Validate(model); err != nil {
		t.Fatalf("pure DSL rejected a source-defined provenance kind: %v", err)
	}
}

func TestPayloadBytesDecodeDeclaredEncoding(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		payload dsl.Payload
		want    string
		wantErr bool
	}{
		{name: "text", payload: dsl.Payload{Encoding: dsl.PayloadEncodingText, Content: "hello"}, want: "hello"},
		{name: "json", payload: dsl.Payload{Encoding: dsl.PayloadEncodingJSON, Content: `{"ok":true}`}, want: `{"ok":true}`},
		{name: "base64", payload: dsl.Payload{Encoding: dsl.PayloadEncodingBase64, Content: "aGVsbG8="}, want: "hello"},
		{name: "invalid-json", payload: dsl.Payload{Encoding: dsl.PayloadEncodingJSON, Content: "{"}, wantErr: true},
		{name: "invalid-base64", payload: dsl.Payload{Encoding: dsl.PayloadEncodingBase64, Content: "!"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.payload.Bytes()
			if (err != nil) != test.wantErr {
				t.Fatalf("payload error: %v", err)
			}
			if !test.wantErr && string(got) != test.want {
				t.Fatalf("payload bytes: expected %q, got %q", test.want, got)
			}
		})
	}

	var missing dsl.Payload
	if err := json.Unmarshal([]byte(`{"encoding":"text"}`), &missing); err != nil {
		t.Fatalf("decode missing payload content: %v", err)
	}
	if _, err := missing.Bytes(); err == nil {
		t.Fatal("missing decoded payload content was treated as an empty payload")
	}
}

func testBenchmark() dsl.SynthesizedBenchmark {
	return dsl.SynthesizedBenchmark{
		SchemaVersion: dsl.CurrentSchemaVersion,
		ID:            "example",
		Baseline: dsl.LoadSpec{
			Kind:       dsl.LoadSharedIterations,
			VUs:        1,
			Iterations: 3,
		},
		Cases: []dsl.Case{
			{
				ID:   "case-a",
				Name: "case A",
				Operation: dsl.OperationRef{
					ID: "operation-a", Method: "get", Path: "/a",
				},
				Request: dsl.RequestSpec{
					Method: "get", Path: "/a", Redirects: dsl.RedirectNone,
					Query:   []dsl.Parameter{{Name: "q", Value: "value"}},
					Headers: []dsl.Header{{Name: "Accept", Values: []string{"application/json"}}},
				},
				Check:      &dsl.CheckSpec{ID: "check-a", Name: "response matches", Enabled: true},
				Attributes: dsl.AttributeSet{{Name: "tenant", Value: "consumer"}},
				Source:     dsl.Provenance{Kind: "generated", Locator: "example"},
			},
			{
				ID:   "case-b",
				Name: "case B",
				Operation: dsl.OperationRef{
					ID: "operation-b", Method: "POST", Path: "/b",
				},
				Request: dsl.RequestSpec{Method: "POST", Path: "/b", Redirects: dsl.RedirectNone},
				Source:  dsl.Provenance{Kind: "generated", Locator: "example"},
			},
		},
		Segments: []dsl.Segment{{
			ID: "all", Start: dsl.Duration("0s"),
			Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin},
			Checks:    dsl.CheckInherit,
		}},
	}
}
