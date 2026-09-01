// This file verifies that direct and Pact sources produce the same validated execution boundary.
package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"go.k6.io/k6/lib/netext/httpext"
	benchmarkpkg "k6-as-a-library/internal/benchmark"
	"k6-as-a-library/internal/dsl"
)

func TestExecutionPlanMapsDirectAndPactSources(t *testing.T) {
	t.Parallel()

	config := defaultRunConfig()
	config.virtualUsers = 1
	config.iterations = 4
	directTarget, err := httpext.NewURL("http://provider.example/base?z=last&a=first", "direct")
	if err != nil {
		t.Fatalf("create direct target: %v", err)
	}
	direct, err := synthesizeBenchmark(config, directTarget.GetURL(), nil)
	if err != nil {
		t.Fatalf("create direct plan: %v", err)
	}
	directModel := direct.Benchmark()
	if len(directModel.Cases) != 1 || directModel.Cases[0].Source.Kind != "generated" {
		t.Fatalf("direct source mapping = %#v", directModel.Cases)
	}
	if got := directModel.Cases[0].Request.Query; len(got) != 2 || got[0].Name != "a" || got[1].Name != "z" {
		t.Fatalf("direct query mapping = %#v", got)
	}
	if directModel.Report.GroupBy == nil || len(directModel.Report.GroupBy) != 0 {
		t.Fatalf("direct report grouping = %#v", directModel.Report)
	}
	for ordinal := range 4 {
		selected, err := direct.SelectAt(0, uint64(ordinal))
		if err != nil {
			t.Fatalf("select direct case %d: %v", ordinal, err)
		}
		if selected.Case.ID != directCaseID {
			t.Fatalf("direct selection %d = %q", ordinal, selected.Case.ID)
		}
	}

	interactions, err := loadPactDirectory(pactFixtureDirectory())
	if err != nil {
		t.Fatalf("load Pact interactions: %v", err)
	}
	pactTarget, err := httpext.NewURL("http://provider.example/api", "pact")
	if err != nil {
		t.Fatalf("create Pact target: %v", err)
	}
	pact, err := synthesizeBenchmark(config, pactTarget.GetURL(), interactions)
	if err != nil {
		t.Fatalf("create Pact plan: %v", err)
	}
	pactModel := pact.Benchmark()
	if len(pactModel.Cases) != len(interactions) {
		t.Fatalf("Pact case count = %d, want %d", len(pactModel.Cases), len(interactions))
	}
	if len(pactModel.Thresholds) != 1 || pactModel.Thresholds[0].Metric != "checks{"+pactResponseCheckSubmetric+"}" {
		t.Fatalf("Pact thresholds = %#v", pactModel.Thresholds)
	}
	wantGroupBy := []string{pactConsumerTag, pactProviderTag, pactEndpointTag, pactInteractionTag, pactProviderStateTag}
	if !slices.Equal(pactModel.Report.GroupBy, wantGroupBy) {
		t.Fatalf("Pact report grouping = %v, want %v", pactModel.Report.GroupBy, wantGroupBy)
	}
	for index, item := range pactModel.Cases {
		if item.Source.Kind != "pact" || item.Source.Locator == "" || item.Source.Interaction == "" {
			t.Errorf("Pact case %d has incomplete provenance: %#v", index, item.Source)
		}
		if item.Check == nil || !item.Check.Enabled {
			t.Errorf("Pact case %d has no enabled response check", index)
		}
		if item.Request.Behavior == nil || len(item.Request.Behavior.Matching) == 0 {
			t.Errorf("Pact case %d has no response matching description", index)
		}
	}
	for ordinal := range len(pactModel.Cases) * 2 {
		selected, err := pact.SelectAt(0, uint64(ordinal))
		if err != nil {
			t.Fatalf("select Pact case %d: %v", ordinal, err)
		}
		want := pactModel.Cases[ordinal%len(pactModel.Cases)].ID
		if selected.Case.ID != want {
			t.Fatalf("Pact selection %d = %q, want %q", ordinal, selected.Case.ID, want)
		}
	}
}

func TestExecutionPlanRequestAdapterPreservesPactRequest(t *testing.T) {
	t.Parallel()

	interactions, err := loadPactDirectory(pactFixtureDirectory())
	if err != nil {
		t.Fatalf("load Pact interactions: %v", err)
	}
	target, err := httpext.NewURL("http://provider.example/api", "pact")
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	config := defaultRunConfig()
	execution, err := synthesizeBenchmark(config, target.GetURL(), interactions)
	if err != nil {
		t.Fatalf("create Pact plan: %v", err)
	}
	var post dsl.Case
	for _, item := range execution.Benchmark().Cases {
		if item.Operation.Method == http.MethodPost {
			post = item
			break
		}
	}
	if post.ID == "" {
		t.Fatal("POST Pact case was not mapped")
	}
	prepared, err := benchmarkpkg.PrepareRequest(target, false, post)
	if err != nil {
		t.Fatalf("prepare plan request: %v", err)
	}
	if got := prepared.Request.Method; got != http.MethodPost {
		t.Fatalf("request method = %s", got)
	}
	if got := prepared.Request.URL.String(); got != "http://provider.example/api/post" {
		t.Fatalf("request URL = %q", got)
	}
	if got := prepared.Request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	body, err := io.ReadAll(prepared.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if !strings.Contains(string(body), "hello from Pact") {
		t.Fatalf("request body = %q", body)
	}
}

func TestExecutionPlanRequestAdapterPreservesDirectRequest(t *testing.T) {
	t.Parallel()

	target, err := httpext.NewURL("http://provider.example/headers?source=direct", "direct")
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	execution, err := synthesizeBenchmark(defaultRunConfig(), target.GetURL(), nil)
	if err != nil {
		t.Fatalf("create direct plan: %v", err)
	}
	prepared, err := benchmarkpkg.PrepareRequest(target, true, execution.Benchmark().Cases[0])
	if err != nil {
		t.Fatalf("prepare direct request: %v", err)
	}
	if got := prepared.Request.Method; got != http.MethodGet {
		t.Fatalf("request method = %s", got)
	}
	if got := prepared.Request.URL.String(); got != "http://provider.example/headers?source=direct" {
		t.Fatalf("request URL = %q", got)
	}
}

func TestPactRequestUsesConfiguredProviderDespiteHostHeader(t *testing.T) {
	t.Parallel()

	var expectedHost string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Host != expectedHost {
			t.Errorf("request host = %q, want configured provider %q", request.Host, expectedHost)
		}
		if request.URL.Path != "/api/resource" {
			t.Errorf("request path = %q, want %q", request.URL.Path, "/api/resource")
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	expectedHost = server.Listener.Addr().String()

	target, err := httpext.NewURL(server.URL+"/api", "pact provider")
	if err != nil {
		t.Fatalf("create Pact provider URL: %v", err)
	}
	item := dsl.Case{
		Name: "configured provider",
		Request: dsl.RequestSpec{
			Method:  http.MethodGet,
			Path:    "/resource",
			Headers: []dsl.Header{{Name: "Host", Values: []string{"contract.example:9443"}}},
		},
		Source: dsl.Provenance{Kind: "pact"},
	}
	prepared, err := benchmarkpkg.PrepareRequest(target, false, item)
	if err != nil {
		t.Fatalf("prepare Pact request: %v", err)
	}
	if prepared.Request.Host != "" {
		t.Fatalf("prepared request overrides configured provider host with %q", prepared.Request.Host)
	}
	result, err := server.Client().Do(prepared.Request)
	if err != nil {
		t.Fatalf("send Pact request: %v", err)
	}
	if err := result.Body.Close(); err != nil {
		t.Fatalf("close Pact response body: %v", err)
	}
	if result.StatusCode != http.StatusNoContent {
		t.Fatalf("response status = %d, want %d", result.StatusCode, http.StatusNoContent)
	}
}

func TestExecutionPlanRejectsInvalidSourceBeforeExecution(t *testing.T) {
	t.Parallel()

	config := defaultRunConfig()
	config.iterations = 0
	target, err := httpext.NewURL("http://provider.example/headers", "direct")
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	_, err = synthesizeBenchmark(config, target.GetURL(), nil)
	if err == nil || !strings.Contains(err.Error(), "shared-iteration load requires positive VUs and iterations") {
		t.Fatalf("invalid source was not rejected before execution: %v", err)
	}
}
