package app

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"k6-as-a-library/internal/report"

	"github.com/sirupsen/logrus"
	"go.k6.io/k6/lib"
	"go.k6.io/k6/lib/fsext"
	"go.k6.io/k6/metrics"
	"go.k6.io/k6/output"
)

func TestLiveDashboardServesUIAndStreamsSnapshots(t *testing.T) {
	port := availableTCPPort(t)
	config := defaultRunConfig()
	config.dashboard = true
	config.dashboardPort = port

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	liveDashboard, err := report.NewLiveDashboardOutput(output.Params{
		Logger:      logger,
		Environment: map[string]string{},
		StdOut:      io.Discard,
		StdErr:      io.Discard,
		FS:          fsext.NewOsFs(),
		ExecutionPlan: []lib.ExecutionStep{
			{TimeOffset: 0, PlannedVUs: 1},
			{TimeOffset: 2 * time.Second, PlannedVUs: 0},
		},
	}, report.LiveDashboardOptions{
		Host: config.dashboardHost, Port: config.dashboardPort, Period: dashboardMinPeriod,
	})
	if err != nil {
		t.Fatalf("create live dashboard: %v", err)
	}
	if err := liveDashboard.Start(); err != nil {
		t.Fatalf("start live dashboard: %v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			if err := liveDashboard.Stop(); err != nil {
				t.Errorf("stop live dashboard: %v", err)
			}
		}
	})

	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(dashboardURL(config.dashboardHost, config.dashboardPort) + "/ui/")
	if err != nil {
		t.Fatalf("request live dashboard UI: %v", err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("read live dashboard UI: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close live dashboard UI response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("live dashboard UI returned %s", response.Status)
	}

	registry := metrics.NewRegistry()
	httpRequests := registry.MustNewMetric(metrics.HTTPReqsName, metrics.Counter)
	at := time.Now()
	liveDashboard.AddMetricSamples([]metrics.SampleContainer{metrics.ConnectedSamples{
		Time: at,
		Samples: []metrics.Sample{
			newSample(httpRequests, registry.RootTagSet(), at, 1),
		},
	}})

	requestContext, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		dashboardURL(config.dashboardHost, config.dashboardPort)+"/events",
		nil,
	)
	if err != nil {
		cancel()
		t.Fatalf("create dashboard events request: %v", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err = client.Do(request)
	if err != nil {
		cancel()
		t.Fatalf("request dashboard events: %v", err)
	}

	foundSnapshot := false
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if scanner.Text() == "event: snapshot" {
			foundSnapshot = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read dashboard events: %v", err)
	}
	if !foundSnapshot {
		t.Fatal("live dashboard did not stream a snapshot")
	}

	stopResult := make(chan error, 1)
	go func() {
		stopResult <- liveDashboard.Stop()
	}()
	select {
	case err := <-stopResult:
		if err != nil {
			t.Fatalf("stop live dashboard: %v", err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		if err := response.Body.Close(); err != nil {
			t.Errorf("close dashboard events response: %v", err)
		}
		t.Fatal("live dashboard shutdown waited for the SSE client")
	}
	stopped = true
	cancel()
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close dashboard events response: %v", err)
	}
}

func availableTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", net.JoinHostPort(defaultDashboardHost, "0"))
	if err != nil {
		t.Fatalf("allocate dashboard port: %v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		if closeErr := listener.Close(); closeErr != nil {
			t.Errorf("release dashboard port: %v", closeErr)
		}
		t.Fatalf("unexpected listener address %T", listener.Addr())
	}
	port := address.Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release dashboard port %s: %v", strconv.Itoa(port), err)
	}
	if port < 1 {
		t.Fatalf("invalid allocated dashboard port %d", port)
	}
	return port
}

func TestDashboardURLSupportsIPv6(t *testing.T) {
	t.Parallel()

	actual := dashboardURL("::1", defaultDashboardPort)
	expected := "http://[::1]:" + strconv.Itoa(defaultDashboardPort)
	if actual != expected {
		t.Fatalf("expected %s, got %s", expected, actual)
	}
}
