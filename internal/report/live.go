package report

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/grafana/xk6-dashboard/dashboard"
	"go.k6.io/k6/errext"
	"go.k6.io/k6/output"
)

type LiveDashboardOptions struct {
	Host   string
	Port   int
	Period time.Duration
	Tags   []string
	Open   bool
}

func NewLiveDashboardOutput(params output.Params, options LiveDashboardOptions) (output.Output, error) {
	values := url.Values{
		"host":   {options.Host},
		"period": {options.Period.String()},
		"port":   {strconv.Itoa(options.Port)},
	}
	for _, tag := range options.Tags {
		values.Add("tag", tag)
	}
	if options.Open {
		values.Set("open", "true")
	}
	params.OutputType = dashboard.OutputName
	params.ConfigArgument = values.Encode()
	dashboardOutput, err := dashboard.New(params)
	if err != nil {
		return nil, err
	}
	stopper, ok := dashboardOutput.(output.WithStopWithTestError)
	if !ok {
		return nil, fmt.Errorf("live dashboard does not support test-aware shutdown")
	}
	return &liveDashboardOutput{Output: dashboardOutput, stopper: stopper}, nil
}

func DashboardTags(groupBy []string) []string {
	if len(groupBy) == 0 {
		return []string{DashboardDefaultTag}
	}
	return append([]string(nil), groupBy...)
}

type liveDashboardOutput struct {
	output.Output
	stopper output.WithStopWithTestError
}

func (dashboardOutput *liveDashboardOutput) Stop() error {
	return dashboardOutput.StopWithTestError(nil)
}

func (dashboardOutput *liveDashboardOutput) StopWithTestError(testErr error) error {
	if testErr == nil {
		testErr = errors.New("live dashboard server shutdown")
	}
	testErr = errext.WithAbortReasonIfNone(testErr, errext.AbortedByOutput)
	return dashboardOutput.stopper.StopWithTestError(testErr)
}
