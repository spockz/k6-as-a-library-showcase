// This file synthesizes direct and Pact sources into one validated benchmark before k6 execution.
package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	benchmarkpkg "k6-as-a-library/internal/benchmark"
	"k6-as-a-library/internal/dsl"
	"k6-as-a-library/internal/pact"
)

const directCaseID = "direct-request"

func synthesizeBenchmark(config runConfig, targetURL *url.URL, interactions []pact.Interaction) (benchmarkpkg.ValidatedBenchmark, error) {
	if targetURL == nil {
		return benchmarkpkg.ValidatedBenchmark{}, errors.New("synthesize benchmark: target URL is nil")
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

	if len(interactions) == 0 {
		model.Report.GroupBy = []string{}
		model.Report.GroupByPresence = dsl.PresenceValue
		item, err := directCase(targetURL)
		if err != nil {
			return benchmarkpkg.ValidatedBenchmark{}, err
		}
		model.Cases = []dsl.Case{item}
	} else {
		model.Report = pact.ReportSpec(interactions)
		for index, interaction := range interactions {
			item, err := pact.Case(interaction, index)
			if err != nil {
				return benchmarkpkg.ValidatedBenchmark{}, fmt.Errorf("synthesize benchmark for PACT interaction %q: %w", interaction.Name, err)
			}
			model.Cases = append(model.Cases, item)
			model.Provenance = append(model.Provenance, item.Source)
		}
		model.Thresholds = pact.Thresholds()
	}

	validated, err := benchmarkpkg.Compose(model)
	if err != nil {
		return benchmarkpkg.ValidatedBenchmark{}, fmt.Errorf("validate synthesized benchmark: %w", err)
	}
	return validated, nil
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
