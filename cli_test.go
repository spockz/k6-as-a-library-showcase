package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCommandHelpListsConfigurationFlags(t *testing.T) {
	t.Parallel()

	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"run", "--help"})

	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("execute help: %v", err)
	}
	for _, flag := range []string{
		"--url",
		"--pacts-dir",
		"--vus",
		"--iterations",
		"--min-iteration-duration",
		"--request-timeout",
		"--max-duration",
		"--json-output",
		"--html-output",
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
}

func TestRunCommandRejectsInvalidArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "positional argument", args: []string{"run", "unexpected"}},
		{name: "invalid URL", args: []string{"run", "--url", "localhost:8080"}},
		{name: "missing PACT directory", args: []string{"run", "--pacts-dir", "/path/does/not/exist"}},
		{name: "zero VUs", args: []string{"run", "--vus", "0"}},
		{name: "too few iterations", args: []string{"run", "--vus", "2", "--iterations", "1"}},
		{
			name: "negative minimum iteration duration",
			args: []string{"run", "--min-iteration-duration", "-1ms"},
		},
		{name: "short max duration", args: []string{"run", "--max-duration", "999ms"}},
		{name: "empty JSON path", args: []string{"run", "--json-output", ""}},
		{name: "empty HTML path", args: []string{"run", "--html-output", ""}},
		{name: "empty dashboard host", args: []string{"run", "--dashboard", "--dashboard-host", ""}},
		{name: "zero dashboard port", args: []string{"run", "--dashboard", "--dashboard-port", "0"}},
		{name: "large dashboard port", args: []string{"run", "--dashboard", "--dashboard-port", "65536"}},
		{name: "open disabled dashboard", args: []string{"run", "--dashboard-open"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			command := newRootCommand()
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(test.args)
			if err := command.ExecuteContext(t.Context()); err == nil {
				t.Fatal("expected command to reject invalid arguments")
			}
		})
	}
}
