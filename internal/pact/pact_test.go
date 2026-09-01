package pact

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"k6-as-a-library/internal/dsl"
)

func TestAttributeNamesUsePactNamespace(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"consumer service": AttributeConsumerService,
		"provider service": AttributeProviderService,
		"endpoint":         AttributeEndpoint,
		"interaction":      AttributeInteraction,
		"provider state":   AttributeProviderState,
	}
	expected := map[string]string{
		"consumer service": "pact.consumer_service",
		"provider service": "pact.provider_service",
		"endpoint":         "pact.endpoint",
		"interaction":      "pact.interaction",
		"provider state":   "pact.provider_state",
	}
	for meaning, name := range want {
		if name != expected[meaning] {
			t.Errorf("%s attribute = %q, want %q", meaning, name, expected[meaning])
		}
	}
}

func TestLoadDirectoryLoadsAllInteractions(t *testing.T) {
	t.Parallel()

	interactions, err := loadPactDirectory(filepath.Join("..", "..", "testdata", "pacts"))
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	if len(interactions) != 9 {
		t.Fatalf("interaction count = %d, want 9", len(interactions))
	}

	expected := []struct {
		name          string
		consumer      string
		provider      string
		endpoint      string
		providerState string
	}{
		{name: "pact:inspect GET query parameters", consumer: "httpbin-request-consumer", provider: "httpbin", endpoint: "GET /get"},
		{name: "pact:echo a JSON POST body", consumer: "httpbin-request-consumer", provider: "httpbin", endpoint: "POST /post"},
		{name: "pact:return a JSON document", consumer: "httpbin-response-consumer", provider: "httpbin", endpoint: "GET /json"},
		{name: "pact:return decoded text", consumer: "httpbin-response-consumer", provider: "httpbin", endpoint: "GET /base64/UGFjdCBleGFtcGxl"},
		{name: "pact:return custom response headers", consumer: "httpbin-response-consumer", provider: "httpbin", endpoint: "GET /response-headers"},
		{name: "pact:set a cookie with a redirect", consumer: "httpbin-response-consumer", provider: "httpbin", endpoint: "GET /cookies/set"},
		{name: "pact:return no content", consumer: "httpbin-response-consumer", provider: "httpbin", endpoint: "GET /status/204"},
		{name: "pact:return a teapot response", consumer: "httpbin-response-consumer", provider: "httpbin", endpoint: "GET /status/418", providerState: "httpbin supports teapot responses"},
		{name: "pact:expect status 300 from the status 200 endpoint", consumer: "httpbin-response-consumer", provider: "httpbin", endpoint: "GET /status/200"},
	}
	for index, want := range expected {
		interaction := interactions[index]
		if interaction.Name != want.name {
			t.Errorf("interaction %d name = %q, want %q", index, interaction.Name, want.name)
		}
		if interaction.Attributes[AttributeConsumerService] != want.consumer {
			t.Errorf("interaction %d consumer = %q, want %q", index, interaction.Attributes[AttributeConsumerService], want.consumer)
		}
		if interaction.Attributes[AttributeProviderService] != want.provider {
			t.Errorf("interaction %d provider = %q, want %q", index, interaction.Attributes[AttributeProviderService], want.provider)
		}
		if interaction.Attributes[AttributeEndpoint] != want.endpoint {
			t.Errorf("interaction %d endpoint = %q, want %q", index, interaction.Attributes[AttributeEndpoint], want.endpoint)
		}
		if interaction.Attributes[AttributeProviderState] != want.providerState {
			t.Errorf("interaction %d provider state = %q, want %q", index, interaction.Attributes[AttributeProviderState], want.providerState)
		}
		if interaction.PactFile == "" {
			t.Errorf("interaction %d is missing its source Pact file", index)
		}
	}
}

func TestResponseMatchingChecksStatusHeadersAndBody(t *testing.T) {
	t.Parallel()

	interactions, err := loadPactDirectory(filepath.Join("..", "..", "testdata", "pacts"))
	if err != nil {
		t.Fatalf("load PACT directory: %v", err)
	}
	expected := interactions[1].Response
	matching := &dsl.HTTPResponse{
		StatusCode: 200,
		Headers:    map[string]string{"content-type": "application/json; charset=utf-8"},
		Body:       []byte(`{"json":{"message":"hello from Pact"},"origin":"127.0.0.1"}`),
	}
	if err := matchPactResponse(expected, matching); err != nil {
		t.Fatalf("matching PACT response: %v", err)
	}

	mismatch := &dsl.HTTPResponse{
		StatusCode: 201,
		Headers:    map[string]string{"Content-Type": "text/plain"},
		Body:       []byte(`{"json":{"message":"wrong"}}`),
	}
	err = matchPactResponse(expected, mismatch)
	if err == nil {
		t.Fatal("expected PACT response mismatch")
	}
	for _, fragment := range []string{"status", "header", "body"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("mismatch does not mention %s: %v", fragment, err)
		}
	}
}

func TestReportSpecOwnsPactAttributeVocabulary(t *testing.T) {
	want := []string{
		AttributeConsumerService,
		AttributeProviderService,
		AttributeEndpoint,
		AttributeInteraction,
		AttributeProviderState,
	}
	interaction := Interaction{Attributes: make(map[string]string, len(want))}
	for _, name := range want {
		interaction.Attributes[name] = "value"
	}
	report := ReportSpec([]Interaction{interaction})
	if !slices.Equal(report.GroupBy, want) {
		t.Fatalf("Pact report grouping = %#v", report)
	}
}

func TestReportSpecOmitsUnavailableOptionalAttributes(t *testing.T) {
	report := ReportSpec([]Interaction{{Attributes: map[string]string{
		AttributeConsumerService: "consumer",
		AttributeProviderService: "provider",
		AttributeEndpoint:        "GET /items",
		AttributeInteraction:     "read items",
	}}})
	if slices.Contains(report.GroupBy, AttributeProviderState) {
		t.Fatalf("unavailable provider state is configured for grouping: %v", report.GroupBy)
	}
}

func TestResponseMatchingChecksCookies(t *testing.T) {
	t.Parallel()

	expected := pactHTTPResponse{
		Status:  http.StatusOK,
		Cookies: json.RawMessage(`{"session":"active"}`),
	}
	actual := &dsl.HTTPResponse{
		StatusCode: http.StatusOK,
		Cookies: map[string][]dsl.ResponseCookie{
			"session": {{Value: "active"}},
		},
	}
	if err := matchPactResponse(expected, actual); err != nil {
		t.Fatalf("matching PACT response cookie: %v", err)
	}
	actual.Cookies["session"][0].Value = "expired"
	if err := matchPactResponse(expected, actual); err == nil {
		t.Fatal("expected PACT cookie mismatch")
	}
}
