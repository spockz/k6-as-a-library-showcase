// This file synthesizes direct and Pact sources into one validated benchmark before k6 execution.
package app

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"

	"go.k6.io/k6/lib/netext/httpext"
	benchmarkpkg "k6-as-a-library/internal/benchmark"
	"k6-as-a-library/internal/dsl"
	"k6-as-a-library/internal/pact"
)

const directCaseID = "direct-request"

type benchmarkExecution struct {
	validated     benchmarkpkg.ValidatedBenchmark
	pactSources   map[string]pact.Interaction
	tagNames      []string
	metadataNames []string
}

func synthesizeBenchmark(config runConfig, targetURL *httpext.URL, interactions []pact.Interaction) (benchmarkExecution, error) {
	if targetURL == nil || targetURL.GetURL() == nil {
		return benchmarkExecution{}, errors.New("synthesize benchmark: target URL is nil")
	}
	model := dsl.SynthesizedBenchmark{
		SchemaVersion: dsl.CurrentSchemaVersion,
		ID:            "native-go",
		Baseline: dsl.LoadSpec{
			Kind:       dsl.LoadSharedIterations,
			VUs:        config.virtualUsers,
			Iterations: config.iterations,
		},
		Segments: []dsl.Segment{{
			ID:        "all",
			Start:     dsl.Duration("0s"),
			Selection: dsl.SelectionSpec{Mode: dsl.SelectionRoundRobin},
			Checks:    dsl.CheckInherit,
		}},
		Provenance: []dsl.Provenance{{Kind: "generated", Identifier: "native-go"}},
	}

	pactSources := make(map[string]pact.Interaction, len(interactions))
	if len(interactions) == 0 {
		item, err := directCase(targetURL.GetURL())
		if err != nil {
			return benchmarkExecution{}, err
		}
		model.Cases = []dsl.Case{item}
	} else {
		for index, interaction := range interactions {
			item, err := pact.Case(interaction, index)
			if err != nil {
				return benchmarkExecution{}, fmt.Errorf("synthesize benchmark for PACT interaction %q: %w", interaction.Name, err)
			}
			if _, exists := pactSources[item.ID]; exists {
				return benchmarkExecution{}, fmt.Errorf("synthesize benchmark: duplicate PACT case ID %q", item.ID)
			}
			model.Cases = append(model.Cases, item)
			pactSources[item.ID] = interaction
			model.Provenance = append(model.Provenance, item.Source)
		}
		model.Thresholds = []dsl.Threshold{{
			ID:          "pact-responses-valid",
			Metric:      "checks{" + pactResponseCheckSubmetric + "}",
			Aggregation: dsl.ThresholdAggregationRate,
			Operator:    "==",
			Target:      1,
			Source:      dsl.Provenance{Kind: "pact", Identifier: "response-matches"},
		}}
	}

	validated, err := benchmarkpkg.Compose(model)
	if err != nil {
		return benchmarkExecution{}, fmt.Errorf("validate synthesized benchmark: %w", err)
	}
	tagNames := make(map[string]struct{})
	metadataNames := make(map[string]struct{})
	for _, item := range validated.Benchmark().Cases {
		for _, label := range item.Labels {
			tagNames[label.Name] = struct{}{}
		}
		for _, metadata := range item.Metadata {
			metadataNames[metadata.Name] = struct{}{}
		}
	}
	return benchmarkExecution{
		validated:     validated,
		pactSources:   pactSources,
		tagNames:      slices.Sorted(maps.Keys(tagNames)),
		metadataNames: slices.Sorted(maps.Keys(metadataNames)),
	}, nil
}

func directCase(target *url.URL) (dsl.Case, error) {
	if target == nil {
		return dsl.Case{}, errors.New("direct request target URL is nil")
	}
	path := target.Path
	if path == "" {
		path = "/"
	}
	return dsl.Case{
		ID:   directCaseID,
		Name: "fixed request",
		Operation: dsl.OperationRef{
			ID: directCaseID, Method: http.MethodGet, Path: path,
		},
		Request: dsl.RequestSpec{
			Method:    http.MethodGet,
			Path:      path,
			Query:     dsl.ParametersFromQuery(target.Query()),
			Redirects: dsl.RedirectFollow,
		},
		Source: dsl.Provenance{Kind: "generated", Identifier: directCaseID},
	}, nil
}
