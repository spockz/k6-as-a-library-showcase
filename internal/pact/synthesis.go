package pact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"k6-as-a-library/internal/dsl"
)

func Case(interaction Interaction, index int) (dsl.Case, error) {
	pathURL, err := url.Parse(interaction.Request.Path)
	if err != nil {
		return dsl.Case{}, fmt.Errorf("parse request path: %w", err)
	}
	if pathURL.IsAbs() || pathURL.Host != "" || pathURL.Fragment != "" {
		return dsl.Case{}, fmt.Errorf("request path %q must be a relative path", interaction.Request.Path)
	}
	query := pathURL.Query()
	trimmedQuery := bytes.TrimSpace(interaction.Request.Query)
	if len(trimmedQuery) != 0 && !bytes.Equal(trimmedQuery, []byte("null")) {
		encodedQuery, err := pactQueryString(interaction.Request.Query)
		if err != nil {
			return dsl.Case{}, err
		}
		query, err = url.ParseQuery(encodedQuery)
		if err != nil {
			return dsl.Case{}, fmt.Errorf("parse Pact query: %w", err)
		}
	}
	path := pathURL.Path
	if path == "" {
		path = "/"
	}
	response, err := responseExpectation(interaction.Response)
	if err != nil {
		return dsl.Case{}, err
	}
	requestHeaders, err := requestHeaders(interaction.Request.Headers)
	if err != nil {
		return dsl.Case{}, err
	}
	requestCookies, err := cookieValues(interaction.Request.Cookies)
	if err != nil {
		return dsl.Case{}, fmt.Errorf("decode request cookies: %w", err)
	}
	requestBody, err := payload(interaction.Request.Body)
	if err != nil {
		return dsl.Case{}, fmt.Errorf("decode request body: %w", err)
	}
	caseID := interaction.Name
	if caseID == "" {
		caseID = fmt.Sprintf("pact-interaction-%d", index)
	}
	source := dsl.Provenance{
		Kind:        "pact",
		Locator:     interaction.PactFile,
		Document:    interaction.PactFile,
		Identifier:  firstNonEmpty(interaction.ID, interaction.Key, caseID),
		Interaction: interaction.Description,
	}
	request := dsl.RequestSpec{
		Method: strings.ToUpper(interaction.Request.Method), Path: path,
		Query: dsl.ParametersFromQuery(query), Headers: requestHeaders,
		Cookies: requestCookies, Body: requestBody, Redirects: dsl.RedirectNone,
	}
	request = request.WithRuntime(dsl.RequestRuntime{
		Match: pactResponseMatcher(interaction.Response),
	}, pactBehaviorDescription(interaction))
	return dsl.Case{
		ID:          caseID,
		Name:        interaction.Name,
		Operation:   dsl.OperationRef{ID: caseID, Method: strings.ToUpper(interaction.Request.Method), Path: path},
		Request:     request,
		Expectation: response,
		Check: &dsl.CheckSpec{
			ID: "pact-check:" + caseID, Name: ResponseCheckName, Enabled: true, Source: source,
		},
		Attributes: attributes(interaction.Attributes),
		Metadata:   caseMetadata(interaction),
		Source:     source,
	}, nil
}

func ReportSpec(interactions []Interaction) dsl.ReportSpec {
	available := make(map[string]bool)
	for _, interaction := range interactions {
		for name := range interaction.Attributes {
			available[name] = true
		}
	}
	candidates := []string{
		AttributeConsumerService,
		AttributeProviderService,
		AttributeEndpoint,
		AttributeInteraction,
		AttributeProviderState,
	}
	groupBy := make([]string, 0, len(candidates))
	for _, name := range candidates {
		if available[name] {
			groupBy = append(groupBy, name)
		}
	}
	return dsl.ReportSpec{GroupBy: groupBy, GroupByPresence: dsl.PresenceValue}
}

func Thresholds() []dsl.Threshold {
	return []dsl.Threshold{{
		ID:          "pact-responses-valid",
		Metric:      "checks{check:" + ResponseCheckName + "}",
		Aggregation: dsl.ThresholdAggregationRate,
		Operator:    "==",
		Target:      1,
		Source:      dsl.Provenance{Kind: "pact", Identifier: "response-matches"},
	}}
}

func pactResponseMatcher(expected HTTPResponse) dsl.ResponseMatcher {
	return func(_ context.Context, actual *dsl.HTTPResponse) (dsl.MatchResult, error) {
		result := verifyPactResponse(expected, actual)
		result.MismatchMetadata = MismatchMetadata
		return result, nil
	}
}

func pactBehaviorDescription(interaction Interaction) dsl.BehaviorDescription {
	matching := []string{fmt.Sprintf("Require response status %d.", interaction.Response.Status)}
	if len(interaction.Response.Headers) > 0 {
		matching = append(matching, fmt.Sprintf("Match %d expected response header(s) using applicable Pact rules.", len(interaction.Response.Headers)))
	}
	if interaction.Response.Cookies != nil {
		matching = append(matching, "Match expected response cookies using Pact semantics.")
	}
	if interaction.Response.Body != nil {
		matching = append(matching, fmt.Sprintf("Match the response body using the Pact example and %d compiled rule(s).", len(interaction.Response.rules)))
	}
	return dsl.BehaviorDescription{Matching: matching}
}

func responseExpectation(response HTTPResponse) (*dsl.ResponseExpectation, error) {
	expectation := &dsl.ResponseExpectation{
		Status:          &dsl.StatusExpectation{Equals: response.Status},
		StatusPresence:  dsl.PresenceValue,
		HeadersPresence: dsl.PresenceValue,
		CookiesPresence: dsl.PresenceValue,
	}
	var err error
	if expectation.Headers, err = responseHeaders(response.Headers); err != nil {
		return nil, err
	}
	if expectation.Cookies, err = cookieExpectations(response.Cookies); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(response.Body)) != 0 {
		body, err := payload(response.Body)
		if err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		expectation.Body = &dsl.BodyExpectation{Example: body, ExamplePresence: dsl.PresenceValue}
		expectation.BodyPresence = dsl.PresenceValue
	}
	return expectation, nil
}

func requestHeaders(raw map[string]json.RawMessage) ([]dsl.Header, error) {
	keys := slices.Sorted(maps.Keys(raw))
	result := make([]dsl.Header, 0, len(keys))
	for _, key := range keys {
		values, err := pactRawStringValues(raw[key])
		if err != nil {
			return nil, fmt.Errorf("decode request header %q: %w", key, err)
		}
		result = append(result, dsl.Header{Name: key, Values: values, ValuesPresence: dsl.PresenceValue})
	}
	return result, nil
}

func responseHeaders(raw map[string]json.RawMessage) ([]dsl.HeaderExpectation, error) {
	keys := slices.Sorted(maps.Keys(raw))
	result := make([]dsl.HeaderExpectation, 0, len(keys))
	for _, key := range keys {
		values, err := pactRawStringValues(raw[key])
		if err != nil {
			return nil, fmt.Errorf("decode response header %q: %w", key, err)
		}
		result = append(result, dsl.HeaderExpectation{Name: key, Values: values})
	}
	return result, nil
}

func cookieExpectations(raw json.RawMessage) ([]dsl.CookieExpectation, error) {
	values, err := cookieValues(raw)
	if err != nil {
		return nil, err
	}
	result := make([]dsl.CookieExpectation, 0, len(values))
	for _, item := range values {
		result = append(result, dsl.CookieExpectation{Name: item.Name, Values: []string{item.Value}})
	}
	return result, nil
}

func cookieValues(raw json.RawMessage) ([]dsl.Cookie, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return nil, fmt.Errorf("decode cookies: %w", err)
	}
	keys := slices.Sorted(maps.Keys(fields))
	result := make([]dsl.Cookie, 0, len(keys))
	for _, key := range keys {
		values, err := pactRawStringValues(fields[key])
		if err != nil {
			return nil, fmt.Errorf("decode cookie %q: %w", key, err)
		}
		if len(values) > 0 {
			result = append(result, dsl.Cookie{Name: key, Value: values[0]})
		}
	}
	return result, nil
}

func payload(raw json.RawMessage) (*dsl.Payload, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	content, err := pactBodyBytes(trimmed)
	if err != nil {
		return nil, err
	}
	encoding := dsl.PayloadEncodingText
	if trimmed[0] != '"' && json.Valid(trimmed) {
		encoding = dsl.PayloadEncodingJSON
	}
	return &dsl.Payload{Encoding: encoding, Content: string(content), ContentPresence: dsl.PresenceValue}, nil
}

func attributes(values map[string]string) dsl.AttributeSet {
	keys := slices.Sorted(maps.Keys(values))
	result := make(dsl.AttributeSet, 0, len(keys))
	for _, key := range keys {
		result = append(result, dsl.Attribute{Name: key, Value: values[key]})
	}
	return result
}

func caseMetadata(interaction Interaction) dsl.AttributeSet {
	metadata := make(dsl.AttributeSet, 0, 2)
	if interaction.PactFile != "" {
		metadata = append(metadata, dsl.Attribute{Name: pactFileMetadata, Value: interaction.PactFile})
	}
	if interaction.Description != "" {
		metadata = append(metadata, dsl.Attribute{Name: pactDescriptionMeta, Value: interaction.Description})
	}
	return metadata
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
