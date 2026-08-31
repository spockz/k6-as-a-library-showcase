// combined_report_table.go keeps exhaustive result tables independent from the one-tag dashboard graph model.
package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"strconv"
	"time"
)

type combinedTableStatus string

const (
	combinedTablePassed       combinedTableStatus = "passed"
	combinedTableFailed       combinedTableStatus = "failed"
	combinedTableNotEvaluated combinedTableStatus = "not-evaluated"
)

type combinedTableTagState string

const (
	combinedTableTagMissing combinedTableTagState = "missing"
	combinedTableTagNull    combinedTableTagState = "null"
	combinedTableTagEmpty   combinedTableTagState = "empty"
	combinedTableTagPresent combinedTableTagState = "value"
)

type combinedTableSummary struct {
	RunDuration      time.Duration
	SampleCount      int
	FailedRequests   int64
	FailedChecks     int64
	FailedThresholds int64
}

type combinedTableValue struct {
	Name    string
	Value   float64
	Display string
}

type combinedTableMetric struct {
	Name     string
	Type     string
	Contains string
	Values   []combinedTableValue
}

type combinedTableTagValue struct {
	State combinedTableTagState
	Value string
}

type combinedTableSeries struct {
	Name     string
	Type     string
	Contains string
	Tags     map[string]combinedTableTagValue
	Values   []combinedTableValue
}

type combinedTableCheck struct {
	Name      string
	GroupPath string
	Passes    int64
	Fails     int64
	Rate      float64
	Status    combinedTableStatus
}

type combinedTableGroup struct {
	Name   string
	Path   string
	Checks []combinedTableCheck
	Groups []combinedTableGroup
}

type combinedTableThreshold struct {
	Metric     string
	Expression string
	Result     string
	Status     combinedTableStatus
	Evaluated  bool
}

type combinedTableDiagnostic struct {
	Code    string
	Message string
	Count   int
}

type combinedTableModel struct {
	Summary      combinedTableSummary
	Metrics      []combinedTableMetric
	TagColumns   []string
	TaggedSeries []combinedTableSeries
	RootGroup    combinedTableGroup
	Thresholds   []combinedTableThreshold
	Diagnostics  []combinedTableDiagnostic
}

type combinedTableMetricRow struct {
	ID        string
	Name      string
	Type      string
	Contains  string
	Statistic string
	Value     string
}

type combinedTableSeriesRow struct {
	ID        string
	Tags      []combinedTableTagCell
	Name      string
	Type      string
	Contains  string
	Statistic string
	Value     string
}

type combinedTableTagCell struct {
	Text  string
	Class string
}

type combinedTableCheckRow struct {
	ID        string
	GroupPath string
	Name      string
	Passes    string
	Fails     string
	Rate      string
	Status    string
	Class     string
}

type combinedTableThresholdRow struct {
	ID         string
	Metric     string
	Expression string
	Result     string
	Status     string
	Class      string
}

type combinedTableDiagnosticRow struct {
	Code    string
	Message string
	Count   string
}

type combinedTablePage struct {
	Summary      combinedTableSummary
	Duration     string
	Metrics      []combinedTableMetricRow
	TagColumns   []string
	TaggedSeries []combinedTableSeriesRow
	Checks       []combinedTableCheckRow
	Thresholds   []combinedTableThresholdRow
	Diagnostics  []combinedTableDiagnosticRow
}

var combinedTableTemplate = template.Must(template.New("combined-table").Parse(`
<section id="combined-tables" aria-labelledby="combined-tables-heading">
  <h2 id="combined-tables-heading">Detailed results</h2>
  <p id="combined-tables-summary">Run duration: {{.Duration}}; samples: {{.Summary.SampleCount}}; failed requests: {{.Summary.FailedRequests}}; failed checks: {{.Summary.FailedChecks}}; failed thresholds: {{.Summary.FailedThresholds}}.</p>
  <section id="combined-metrics" aria-labelledby="combined-metrics-heading">
    <h3 id="combined-metrics-heading">Metrics</h3>
    <table id="combined-metrics-table" class="combined-table">
      <caption>Finalized base metrics and aggregate values</caption>
      <thead><tr><th scope="col">Metric</th><th scope="col">Type</th><th scope="col">Contains</th><th scope="col">Statistic</th><th scope="col">Value</th></tr></thead>
      <tbody>{{range .Metrics}}
        <tr id="{{.ID}}"><th scope="row">{{.Name}}</th><td>{{.Type}}</td><td>{{.Contains}}</td><td>{{.Statistic}}</td><td>{{.Value}}</td></tr>
      {{end}}</tbody>
    </table>
  </section>
  <section id="combined-tagged-series" aria-labelledby="combined-tagged-series-heading">
    <h3 id="combined-tagged-series-heading">Tagged series</h3>
    <table id="combined-tagged-series-table" class="combined-table">
      <caption>Finalized tagged series, including tag combinations not represented by the graph frontend</caption>
      <thead><tr>{{range .TagColumns}}<th scope="col">{{.}}</th>{{end}}<th scope="col">Metric</th><th scope="col">Type</th><th scope="col">Contains</th><th scope="col">Statistic</th><th scope="col">Value</th></tr></thead>
      <tbody>{{range .TaggedSeries}}
        <tr id="{{.ID}}">{{range .Tags}}<td class="{{.Class}}">{{.Text}}</td>{{end}}<th scope="row">{{.Name}}</th><td>{{.Type}}</td><td>{{.Contains}}</td><td>{{.Statistic}}</td><td>{{.Value}}</td></tr>
      {{end}}</tbody>
    </table>
  </section>
  <section id="combined-checks" aria-labelledby="combined-checks-heading">
    <h3 id="combined-checks-heading">Checks</h3>
    <table id="combined-checks-table" class="combined-table">
      <caption>Checks in ordered named and nested groups</caption>
      <thead><tr><th scope="col">Group</th><th scope="col">Check</th><th scope="col">Passes</th><th scope="col">Fails</th><th scope="col">Rate</th><th scope="col">Status</th></tr></thead>
      <tbody>{{range .Checks}}
        <tr id="{{.ID}}"><td>{{.GroupPath}}</td><th scope="row">{{.Name}}</th><td>{{.Passes}}</td><td>{{.Fails}}</td><td>{{.Rate}}</td><td><span class="combined-status {{.Class}}">{{.Status}}</span></td></tr>
      {{end}}</tbody>
    </table>
  </section>
  <section id="combined-thresholds" aria-labelledby="combined-thresholds-heading">
    <h3 id="combined-thresholds-heading">Thresholds</h3>
    <table id="combined-thresholds-table" class="combined-table">
      <caption>Configured threshold definitions and their finalized results</caption>
      <thead><tr><th scope="col">Metric</th><th scope="col">Expression</th><th scope="col">Result</th><th scope="col">Status</th></tr></thead>
      <tbody>{{range .Thresholds}}
        <tr id="{{.ID}}"><th scope="row">{{.Metric}}</th><td>{{.Expression}}</td><td>{{.Result}}</td><td><span class="combined-status {{.Class}}">{{.Status}}</span></td></tr>
      {{end}}</tbody>
    </table>
  </section>
  {{if .Diagnostics}}
  <section id="combined-diagnostics" aria-labelledby="combined-diagnostics-heading">
    <h3 id="combined-diagnostics-heading">Report diagnostics</h3>
    {{range .Diagnostics}}<p class="combined-diagnostic" role="status"><strong>{{.Code}}</strong>: {{.Message}}{{if .Count}} (count: {{.Count}}){{end}}. <a href="#combined-tagged-series">View all tagged series.</a></p>{{end}}
  </section>
  {{end}}
  <section id="combined-licenses" aria-labelledby="combined-licenses-heading">
    <h3 id="combined-licenses-heading">Licenses and source</h3>
    <p>The interactive graphs use <a href="https://github.com/grafana/xk6-dashboard">xk6-dashboard v0.8.1</a> and <a href="https://github.com/grafana/xk6-dashboard-assets">xk6-dashboard-assets v0.1.2</a>, licensed under AGPL-3.0. The report layout uses <a href="https://github.com/benc-uk/k6-reporter">k6-reporter v3.0.4</a>, licensed under MIT.</p>
  </section>
</section>
`))

func renderCombinedTableFragment(model combinedTableModel) ([]byte, error) {
	page, err := combinedTablePageFor(model)
	if err != nil {
		return nil, err
	}
	var rendered bytes.Buffer
	if err := combinedTableTemplate.Execute(&rendered, page); err != nil {
		return nil, fmt.Errorf("render combined table fragment: %w", err)
	}
	return rendered.Bytes(), nil
}

func combinedTablePageFor(model combinedTableModel) (combinedTablePage, error) {
	if err := validateCombinedTableModel(model); err != nil {
		return combinedTablePage{}, err
	}

	page := combinedTablePage{
		Summary:    model.Summary,
		Duration:   model.Summary.RunDuration.String(),
		TagColumns: append([]string(nil), model.TagColumns...),
	}
	for metricIndex, metric := range model.Metrics {
		for valueIndex, value := range combinedTableValues(metric.Values) {
			page.Metrics = append(page.Metrics, combinedTableMetricRow{
				ID:        combinedTableRowID("metric", metric.Name, value.Name, strconv.Itoa(metricIndex), strconv.Itoa(valueIndex)),
				Name:      metric.Name,
				Type:      metric.Type,
				Contains:  metric.Contains,
				Statistic: value.Name,
				Value:     combinedTableValueText(value),
			})
		}
	}
	for seriesIndex, series := range model.TaggedSeries {
		tags := make([]combinedTableTagCell, len(model.TagColumns))
		for tagIndex, column := range model.TagColumns {
			value, present := series.Tags[column]
			if !present {
				value = combinedTableTagValue{State: combinedTableTagMissing}
			}
			tags[tagIndex] = combinedTableTagCellFor(value)
		}
		for valueIndex, value := range combinedTableValues(series.Values) {
			page.TaggedSeries = append(page.TaggedSeries, combinedTableSeriesRow{
				ID:        combinedTableRowID("series", series.Name, value.Name, strconv.Itoa(seriesIndex), strconv.Itoa(valueIndex)),
				Tags:      tags,
				Name:      series.Name,
				Type:      series.Type,
				Contains:  series.Contains,
				Statistic: value.Name,
				Value:     combinedTableValueText(value),
			})
		}
	}
	combinedTableAppendChecks(&page.Checks, model.RootGroup, model.RootGroup.Path)
	for thresholdIndex, threshold := range model.Thresholds {
		status, class := combinedTableStatusView(threshold.Status, threshold.Evaluated)
		result := threshold.Result
		if status == "Not evaluated" && result == "" {
			result = "Not evaluated"
		}
		page.Thresholds = append(page.Thresholds, combinedTableThresholdRow{
			ID:         combinedTableRowID("threshold", threshold.Metric, threshold.Expression, strconv.Itoa(thresholdIndex)),
			Metric:     threshold.Metric,
			Expression: threshold.Expression,
			Result:     result,
			Status:     status,
			Class:      class,
		})
	}
	for _, diagnostic := range model.Diagnostics {
		count := ""
		if diagnostic.Count > 0 {
			count = strconv.Itoa(diagnostic.Count)
		}
		page.Diagnostics = append(page.Diagnostics, combinedTableDiagnosticRow{
			Code:    diagnostic.Code,
			Message: diagnostic.Message,
			Count:   count,
		})
	}
	return page, nil
}

func combinedTableAppendChecks(rows *[]combinedTableCheckRow, group combinedTableGroup, inheritedPath string) {
	path := group.Path
	if path == "" {
		path = inheritedPath
	}
	for checkIndex, check := range group.Checks {
		status, class := combinedTableCheckStatus(check)
		checkPath := check.GroupPath
		if checkPath == "" {
			checkPath = path
		}
		*rows = append(*rows, combinedTableCheckRow{
			ID:        combinedTableRowID("check", checkPath, check.Name, strconv.Itoa(checkIndex), strconv.Itoa(len(*rows))),
			GroupPath: checkPath,
			Name:      check.Name,
			Passes:    strconv.FormatInt(check.Passes, 10),
			Fails:     strconv.FormatInt(check.Fails, 10),
			Rate:      strconv.FormatFloat(check.Rate, 'g', -1, 64),
			Status:    status,
			Class:     class,
		})
	}
	for _, child := range group.Groups {
		combinedTableAppendChecks(rows, child, path)
	}
}

func combinedTableValues(values []combinedTableValue) []combinedTableValue {
	if len(values) != 0 {
		return values
	}
	return []combinedTableValue{{Name: "", Display: "Not evaluated"}}
}

func combinedTableValueText(value combinedTableValue) string {
	if value.Display != "" {
		return value.Display
	}
	return strconv.FormatFloat(value.Value, 'g', -1, 64)
}

func combinedTableTagCellFor(value combinedTableTagValue) combinedTableTagCell {
	state := value.State
	if state == "" {
		if value.Value == "" {
			state = combinedTableTagEmpty
		} else {
			state = combinedTableTagPresent
		}
	}
	switch state {
	case combinedTableTagMissing:
		return combinedTableTagCell{Text: "missing", Class: "combined-tag-missing"}
	case combinedTableTagNull:
		return combinedTableTagCell{Text: "null", Class: "combined-tag-null"}
	case combinedTableTagEmpty:
		return combinedTableTagCell{Text: "(empty)", Class: "combined-tag-empty"}
	default:
		return combinedTableTagCell{Text: value.Value}
	}
}

func combinedTableStatusView(status combinedTableStatus, evaluated bool) (string, string) {
	if !evaluated || !statusIsEvaluated(status) {
		return "Not evaluated", "combined-status-not-evaluated"
	}
	if status == combinedTablePassed {
		return "Passed", "combined-status-passed"
	}
	return "Failed", "combined-status-failed"
}

func combinedTableCheckStatus(check combinedTableCheck) (string, string) {
	if check.Status != "" {
		return combinedTableStatusView(check.Status, check.Passes+check.Fails > 0)
	}
	if check.Passes+check.Fails == 0 {
		return "Not evaluated", "combined-status-not-evaluated"
	}
	if check.Fails == 0 {
		return "Passed", "combined-status-passed"
	}
	return "Failed", "combined-status-failed"
}

func statusIsEvaluated(status combinedTableStatus) bool {
	return status == combinedTablePassed || status == combinedTableFailed
}

func combinedTableRowID(kind string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		hash.Write([]byte{0})
		hash.Write([]byte(value))
	}
	return "combined-" + kind + "-" + hex.EncodeToString(hash.Sum(nil)[:8])
}

func validateCombinedTableModel(model combinedTableModel) error {
	if model.Summary.SampleCount < 0 {
		return errors.New("combined table sample count must not be negative")
	}
	if model.Summary.FailedRequests < 0 || model.Summary.FailedChecks < 0 || model.Summary.FailedThresholds < 0 {
		return errors.New("combined table failure counts must not be negative")
	}
	seenTags := make(map[string]struct{}, len(model.TagColumns))
	for _, tag := range model.TagColumns {
		if tag == "" {
			return errors.New("combined table tag column must not be empty")
		}
		if _, exists := seenTags[tag]; exists {
			return fmt.Errorf("combined table tag column %q is duplicated", tag)
		}
		seenTags[tag] = struct{}{}
	}
	for _, metric := range model.Metrics {
		if err := validateCombinedTableMetric(metric, "metric"); err != nil {
			return err
		}
	}
	for _, series := range model.TaggedSeries {
		if err := validateCombinedTableMetric(combinedTableMetric{Name: series.Name, Type: series.Type, Contains: series.Contains, Values: series.Values}, "tagged series"); err != nil {
			return err
		}
		for tag, value := range series.Tags {
			if _, configured := seenTags[tag]; !configured {
				return fmt.Errorf("combined table tagged series contains unconfigured tag %q", tag)
			}
			if value.State != "" && value.State != combinedTableTagMissing && value.State != combinedTableTagNull && value.State != combinedTableTagEmpty && value.State != combinedTableTagPresent {
				return fmt.Errorf("combined table tag %q has invalid state %q", tag, value.State)
			}
		}
	}
	if err := validateCombinedTableGroups(model.RootGroup); err != nil {
		return err
	}
	for _, threshold := range model.Thresholds {
		if threshold.Metric == "" || threshold.Expression == "" {
			return errors.New("combined table threshold metric and expression must not be empty")
		}
		if threshold.Status != "" && threshold.Status != combinedTablePassed && threshold.Status != combinedTableFailed && threshold.Status != combinedTableNotEvaluated {
			return fmt.Errorf("combined table threshold %q has invalid status %q", threshold.Expression, threshold.Status)
		}
		if !threshold.Evaluated && threshold.Status != "" && threshold.Status != combinedTableNotEvaluated {
			return fmt.Errorf("combined table threshold %q is unevaluated but has status %q", threshold.Expression, threshold.Status)
		}
	}
	for _, diagnostic := range model.Diagnostics {
		if diagnostic.Code == "" || diagnostic.Message == "" {
			return errors.New("combined table diagnostic code and message must not be empty")
		}
		if diagnostic.Count < 0 {
			return fmt.Errorf("combined table diagnostic %q count must not be negative", diagnostic.Code)
		}
	}
	return nil
}

func validateCombinedTableMetric(metric combinedTableMetric, kind string) error {
	if metric.Name == "" {
		return fmt.Errorf("combined table %s name must not be empty", kind)
	}
	for _, value := range metric.Values {
		if value.Name == "" {
			return fmt.Errorf("combined table %s %q has an empty statistic name", kind, metric.Name)
		}
	}
	return nil
}

func validateCombinedTableGroups(group combinedTableGroup) error {
	for _, check := range group.Checks {
		if check.Name == "" {
			return errors.New("combined table check name must not be empty")
		}
		if check.Passes < 0 || check.Fails < 0 {
			return fmt.Errorf("combined table check %q counts must not be negative", check.Name)
		}
		if check.Status != "" && check.Status != combinedTablePassed && check.Status != combinedTableFailed && check.Status != combinedTableNotEvaluated {
			return fmt.Errorf("combined table check %q has invalid status %q", check.Name, check.Status)
		}
	}
	for _, child := range group.Groups {
		if err := validateCombinedTableGroups(child); err != nil {
			return err
		}
	}
	return nil
}
