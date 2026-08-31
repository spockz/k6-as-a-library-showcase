package app

import (
	"fmt"

	"k6-as-a-library/internal/otel"
)

func resolveRunConfig(config runConfig, environment map[string]string) (runConfig, error) {
	if !config.outputsFlagSet {
		if raw, ok := environment["K6_OUT"]; ok {
			outputs, err := otel.ParseOutputSelection(raw)
			if err != nil {
				return config, fmt.Errorf("parse K6_OUT: %w", err)
			}
			config.outputs = outputs
		}
	}
	config.outputs = otel.NormalizeOutputNames(config.outputs)

	if !config.tracesOutputFlagSet {
		if value, ok := environment["K6_TRACES_OUTPUT"]; ok {
			config.tracesOutput = value
		} else if config.tracesOutput == "" {
			config.tracesOutput = otel.DefaultTracesOutput
		}
	}
	traceConfiguration, err := otel.ParseTracesOutput(config.tracesOutput)
	if err != nil {
		return config, fmt.Errorf("parse traces output: %w", err)
	}
	config.traceConfiguration = traceConfiguration
	return config, nil
}
