package pact

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"go.k6.io/k6/lib/netext/httpext"
)

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
		if interaction.Tags[pactConsumerTag] != want.consumer {
			t.Errorf("interaction %d consumer = %q, want %q", index, interaction.Tags[pactConsumerTag], want.consumer)
		}
		if interaction.Tags[pactProviderTag] != want.provider {
			t.Errorf("interaction %d provider = %q, want %q", index, interaction.Tags[pactProviderTag], want.provider)
		}
		if interaction.Tags[pactEndpointTag] != want.endpoint {
			t.Errorf("interaction %d endpoint = %q, want %q", index, interaction.Tags[pactEndpointTag], want.endpoint)
		}
		if interaction.Tags[pactProviderStateTag] != want.providerState {
			t.Errorf("interaction %d provider state = %q, want %q", index, interaction.Tags[pactProviderStateTag], want.providerState)
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
	matching := &httpext.Response{
		Status:  200,
		Headers: map[string]string{"content-type": "application/json; charset=utf-8"},
		Body:    []byte(`{"json":{"message":"hello from Pact"},"origin":"127.0.0.1"}`),
	}
	if err := matchPactResponse(expected, matching); err != nil {
		t.Fatalf("matching PACT response: %v", err)
	}

	mismatch := &httpext.Response{
		Status:  201,
		Headers: map[string]string{"Content-Type": "text/plain"},
		Body:    []byte(`{"json":{"message":"wrong"}}`),
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

func TestResponseMatchingChecksCookies(t *testing.T) {
	t.Parallel()

	expected := pactHTTPResponse{
		Status:  http.StatusOK,
		Cookies: json.RawMessage(`{"session":"active"}`),
	}
	actual := &httpext.Response{
		Status: http.StatusOK,
		Cookies: map[string][]*httpext.HTTPCookie{
			"session": {&httpext.HTTPCookie{Value: "active"}},
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
