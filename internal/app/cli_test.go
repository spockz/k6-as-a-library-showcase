// This file verifies command-line flags and configuration validation behavior.
package app

import (
	"bytes"
	"strings"
	"testing"

	"k6-as-a-library/internal/otel"
)

func TestRunCommandHelpListsConfigurationFlags(t *testing.T) {
	t.Parallel()

	command := NewRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"run", "--help"})

	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("execute help: %v", err)
	}
	for _, flag := range []string{
		"--url",
		"--pact-provider-url",
		"--pacts-dir",
		"--vus",
		"--iterations",
		"--min-iteration-duration",
		"--request-timeout",
		"--max-duration",
		"--json-output",
		"--html-output",
		"--dashboard-output",
		"--combined-output",
		"--benchmark-manifest-output",
		"--out",
		"--traces-output",
		"--dashboard",
		"--dashboard-host",
		"--dashboard-port",
		"--dashboard-open",
	} {
		if !strings.Contains(output.String(), flag) {
			t.Errorf("help does not contain %s", flag)
		}
	}
}

func TestDashboardIsDisabledByDefault(t *testing.T) {
	t.Parallel()

	config := defaultRunConfig()
	if config.dashboard {
		t.Fatal("dashboard is enabled by default")
	}
	if len(config.outputs) != 0 {
		t.Fatalf("outputs are enabled by default: %#v", config.outputs)
	}
	if config.tracesOutput != otel.DefaultTracesOutput {
		t.Fatalf("traces output = %q, want %q by default", config.tracesOutput, otel.DefaultTracesOutput)
	}
	if config.combinedFilename != "" {
		t.Fatalf("combined output = %q, want disabled by default", config.combinedFilename)
	}
	if config.benchmarkManifestFilename != "" {
		t.Fatalf("benchmark manifest output = %q, want disabled by default", config.benchmarkManifestFilename)
	}
}

func TestRunCommandRejectsInvalidArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "positional argument", args: []string{"run", "unexpected"}},
		{name: "invalid URL", args: []string{"run", "--url", "localhost:8080"}},
		{name: "missing PACT provider URL", args: []string{"run", "--pacts-dir", pactFixtureDirectory()}},
		{name: "invalid PACT provider URL", args: []string{"run", "--pacts-dir", pactFixtureDirectory(), "--pact-provider-url", "localhost:8080"}},
		{name: "missing PACT directory", args: []string{"run", "--pacts-dir", "/path/does/not/exist", "--pact-provider-url", "http://localhost:8080"}},
		{name: "zero VUs", args: []string{"run", "--vus", "0"}},
		{name: "too few iterations", args: []string{"run", "--vus", "2", "--iterations", "1"}},
		{
			name: "negative minimum iteration duration",
			args: []string{"run", "--min-iteration-duration", "-1ms"},
		},
		{name: "short max duration", args: []string{"run", "--max-duration", "999ms"}},
		{name: "empty JSON path", args: []string{"run", "--json-output", ""}},
		{name: "empty HTML path", args: []string{"run", "--html-output", ""}},
		{name: "dashboard standard output path", args: []string{"run", "--dashboard-output", "-"}},
		{name: "combined standard output path", args: []string{"run", "--combined-output", "-"}},
		{name: "benchmark manifest standard output path", args: []string{"run", "--benchmark-manifest-output", "-"}},
		{
			name: "dashboard path conflicts with HTML path",
			args: []string{"run", "--dashboard-output", "report.html", "--html-output", "./report.html"},
		},
		{
			name: "combined path conflicts with dashboard path",
			args: []string{"run", "--combined-output", "dashboard.html", "--dashboard-output", "./dashboard.html"},
		},
		{
			name: "plan path conflicts with JSON path",
			args: []string{"run", "--benchmark-manifest-output", "metrics.json", "--json-output", "./metrics.json"},
		},
		{
			name: "plan path conflicts with HTML path",
			args: []string{"run", "--benchmark-manifest-output", "report.html", "--html-output", "./report.html"},
		},
		{
			name: "plan path conflicts with dashboard path",
			args: []string{"run", "--benchmark-manifest-output", "dashboard.html", "--dashboard-output", "./dashboard.html"},
		},
		{
			name: "plan path conflicts with combined path",
			args: []string{"run", "--benchmark-manifest-output", "combined.html", "--combined-output", "./combined.html"},
		},
		{name: "unsupported output", args: []string{"run", "--out", "json"}},
		{name: "invalid traces output", args: []string{"run", "--traces-output", "invalid"}},
		{name: "empty traces output", args: []string{"run", "--traces-output", ""}},
		{name: "empty dashboard host", args: []string{"run", "--dashboard", "--dashboard-host", ""}},
		{name: "zero dashboard port", args: []string{"run", "--dashboard", "--dashboard-port", "0"}},
		{name: "large dashboard port", args: []string{"run", "--dashboard", "--dashboard-port", "65536"}},
		{name: "open disabled dashboard", args: []string{"run", "--dashboard-open"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			command := NewRootCommand()
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(test.args)
			if err := command.ExecuteContext(t.Context()); err == nil {
				t.Fatal("expected command to reject invalid arguments")
			}
		})
	}
}

func TestRunConfigAcceptsEmptyOptionalOutputPaths(t *testing.T) {
	t.Parallel()

	config := defaultRunConfig()
	config.dashboardFilename = ""
	config.combinedFilename = ""
	config.benchmarkManifestFilename = ""
	if err := config.validate(); err != nil {
		t.Fatalf("validate disabled optional outputs: %v", err)
	}
}

func TestRunCommandAcceptsRepeatedOpenTelemetryOutput(t *testing.T) {
	command := newRunCommand()
	if err := command.Flags().Set("out", otel.OutputName); err != nil {
		t.Fatalf("set OpenTelemetry output: %v", err)
	}
	if err := command.Flags().Set("out", otel.OutputName); err != nil {
		t.Fatalf("set duplicate OpenTelemetry output: %v", err)
	}
	if got := command.Flags().Lookup("out").Value.String(); got != otel.OutputName {
		t.Fatalf("--out value = %q, want one OpenTelemetry output", got)
	}
}
