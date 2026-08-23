package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunCommandHelpListsConfigurationFlags(t *testing.T) {
	t.Parallel()

	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"run", "--help"})

	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("execute help: %v", err)
	}
	for _, flag := range []string{
		"--url",
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

func TestDashboardPeriodTracksObservedRuntime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		runtime  time.Duration
		expected time.Duration
	}{
		{name: "short run", runtime: 250 * time.Millisecond, expected: time.Second},
		{name: "thirty seconds", runtime: 30 * time.Second, expected: time.Second},
		{name: "five minutes", runtime: 5 * time.Minute, expected: 2 * time.Second},
		{name: "long run", runtime: time.Hour, expected: 10 * time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			actual := dashboardPeriod(test.runtime)
			if actual != test.expected {
				t.Fatalf("expected dashboard period %s, got %s", test.expected, actual)
			}
		})
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
			if err := command.ExecuteContext(context.Background()); err == nil {
				t.Fatal("expected command to reject invalid arguments")
			}
		})
	}
}
