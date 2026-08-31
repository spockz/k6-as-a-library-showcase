package report

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.k6.io/k6/metrics"
)

const (
	consoleReportTitlePrefix    = "█"
	consoleReportSubtitlePrefix = "↳"
	consoleReportSuccessMark    = "✓"
	consoleReportFailureMark    = "✗"
)

// This compatibility renderer follows go.k6.io/k6 v1.8.1's internal/js/summary.js
// because an external module cannot import k6's internal summary model and renderer.
// Remove it when k6 exposes a public reporter or this module moves into the k6 tree.
type consoleReportRenderer struct {
	formatter  consoleANSIFormatter
	trendStats []string
	timeUnit   string
}

type consoleReportSection struct {
	title string
	lines []string
}

type consoleMetricCategory struct {
	name    string
	metrics map[string]k6SummaryMetric
}

type consoleMetricRenderInfo struct {
	maxNameWidth        int
	trendValues         map[string][]string
	trendKeys           map[string]map[string]string
	trendColumnWidths   []int
	nonTrendValues      map[string]string
	nonTrendExtras      map[string][]string
	maxNonTrendValueLen int
	nonTrendExtraWidths []int
}

type consoleANSIFormatter struct {
	enabled bool
}

func renderK6ConsoleReport(summary k6Summary, enableColors bool) (string, error) {
	renderer := consoleReportRenderer{
		formatter:  consoleANSIFormatter{enabled: enableColors},
		trendStats: slices.Clone(summary.Options.SummaryTrendStats),
		timeUnit:   summary.Options.SummaryTimeUnit,
	}

	sections := make([]consoleReportSection, 0, 2+len(summary.RootGroup.Groups))
	thresholdLines, err := renderer.renderThresholds(summary.Metrics)
	if err != nil {
		return "", err
	}
	if len(thresholdLines) > 0 {
		sections = append(sections, consoleReportSection{title: "THRESHOLDS", lines: thresholdLines})
	}

	totalLines, err := renderer.renderTotalResults(summary)
	if err != nil {
		return "", err
	}
	sections = append(sections, consoleReportSection{title: "TOTAL RESULTS", lines: totalLines})

	for _, group := range summary.RootGroup.Groups {
		groupLines, renderErr := renderer.renderGroup(group, summary.State.TestRunDurationMS, 4)
		if renderErr != nil {
			return "", renderErr
		}
		sections = append(sections, consoleReportSection{
			title: "GROUP: " + group.Name,
			lines: groupLines,
		})
	}

	var report strings.Builder
	for _, section := range sections {
		report.WriteByte('\n')
		report.WriteString("  " + consoleReportTitlePrefix + " ")
		report.WriteString(renderer.formatter.bold(section.title))
		report.WriteString(" \n\n")
		for _, line := range section.lines {
			report.WriteString(line)
			report.WriteByte('\n')
		}
	}
	return report.String(), nil
}

func (r consoleReportRenderer) renderTotalResults(summary k6Summary) ([]string, error) {
	lines := make([]string, 0)
	if checksMetric, exists := summary.Metrics[metrics.ChecksName]; exists {
		checkLines, err := r.renderChecks(&checksMetric, summary.RootGroup.Checks, summary.State.TestRunDurationMS, 4)
		if err != nil {
			return nil, err
		}
		lines = append(lines, checkLines...)
	}

	categories := consoleMetricCategories(summary.Metrics)
	allMetrics := make(map[string]k6SummaryMetric)
	for _, category := range categories {
		maps.Copy(allMetrics, category.metrics)
	}
	globalMaxNameWidth := maxConsoleMetricNameWidth(allMetrics, 4)
	metricLines, err := r.renderMetricCategories(categories, globalMaxNameWidth, 4)
	if err != nil {
		return nil, err
	}
	lines = append(lines, metricLines...)
	return lines, nil
}

func (r consoleReportRenderer) renderThresholds(metricsData map[string]k6SummaryMetric) ([]string, error) {
	thresholdMetrics := make(map[string]k6SummaryMetric)
	for name, metric := range metricsData {
		if len(metric.Thresholds) > 0 {
			thresholdMetrics[name] = metric
		}
	}
	if len(thresholdMetrics) == 0 {
		return nil, nil
	}

	info, err := r.computeMetricRenderInfo(thresholdMetrics, 4, 0)
	if err != nil {
		return nil, err
	}
	metricNames := sortedConsoleMetricNames(thresholdMetrics)
	lines := make([]string, 0)
	for _, name := range metricNames {
		metric := thresholdMetrics[name]
		parentName := consoleMetricParentName(name)
		indent := 4
		displayName := name
		if parentName != name {
			if _, parentExists := thresholdMetrics[parentName]; parentExists {
				indent = 6
				displayName = strings.TrimPrefix(name, parentName)
			}
		}
		lines = append(lines, strings.Repeat(" ", indent)+displayName)

		thresholdSources := orderedThresholdSources(metric)
		for _, source := range thresholdSources {
			threshold := metric.Thresholds[source]
			status := consoleReportFailureMark
			color := consoleANSIRed
			if threshold.OK {
				status = consoleReportSuccessMark
				color = consoleANSIGreen
			}
			aggregation, value, renderErr := r.renderThresholdValue(name, source, metric, info)
			if renderErr != nil {
				return nil, renderErr
			}
			lines = append(lines, strings.Repeat(" ", indent)+strings.Join([]string{
				r.formatter.decorate(status, color),
				r.formatter.decorate("'"+strings.TrimSpace(source)+"'", consoleANSIWhite),
				r.formatter.decorate(aggregation, consoleANSIWhite) + "=" +
					r.formatter.decorate(value, consoleANSICyan),
			}, " "))
		}
		lines = append(lines, "")
	}
	return lines, nil
}

func (r consoleReportRenderer) renderThresholdValue(
	name string,
	source string,
	metric k6SummaryMetric,
	info consoleMetricRenderInfo,
) (string, string, error) {
	aggregation := summaryThresholdAggregation(source)
	if aggregation == "" {
		return "", "", fmt.Errorf("render console threshold for metric %q: cannot determine aggregation from %q", name, source)
	}

	switch metric.Type {
	case metrics.Trend.String():
		for index, stat := range r.trendStats {
			if stat == aggregation {
				return aggregation, info.trendValues[name][index], nil
			}
		}
		value, exists := info.trendKeys[name][aggregation]
		if !exists {
			return "", "", fmt.Errorf("render console threshold for metric %q: value for %q is missing", name, aggregation)
		}
		return aggregation, value, nil
	case metrics.Counter.String():
		if aggregation == "count" {
			return aggregation, info.nonTrendValues[name], nil
		}
		extras := info.nonTrendExtras[name]
		if len(extras) == 0 {
			return "", "", fmt.Errorf("render console threshold for metric %q: rate is missing", name)
		}
		return aggregation, extras[0], nil
	case metrics.Gauge.String(), metrics.Rate.String():
		return aggregation, info.nonTrendValues[name], nil
	default:
		return "", "", fmt.Errorf("render console threshold for metric %q: unsupported type %q", name, metric.Type)
	}
}

func (r consoleReportRenderer) renderMetricCategories(
	categories []consoleMetricCategory,
	globalMaxNameWidth int,
	indent int,
) ([]string, error) {
	allMetrics := make(map[string]k6SummaryMetric)
	for _, category := range categories {
		maps.Copy(allMetrics, category.metrics)
	}
	if len(allMetrics) == 0 {
		return nil, nil
	}
	info, err := r.computeMetricRenderInfo(allMetrics, indent, globalMaxNameWidth)
	if err != nil {
		return nil, err
	}

	lines := make([]string, 0, len(allMetrics)+len(categories)*2)
	for _, category := range categories {
		if len(category.metrics) == 0 {
			continue
		}
		lines = append(lines, strings.Repeat(" ", indent)+r.formatter.bold(category.name))
		for _, name := range sortedConsoleMetricNames(category.metrics) {
			line, renderErr := r.renderMetricLine(name, category.metrics[name], info, indent)
			if renderErr != nil {
				return nil, renderErr
			}
			lines = append(lines, line)
		}
		lines = append(lines, "")
	}
	return lines, nil
}

func (r consoleReportRenderer) renderChecks(
	baseMetric *k6SummaryMetric,
	checks []k6SummaryCheck,
	durationMS float64,
	indent int,
) ([]string, error) {
	checkMetrics, err := consoleCheckMetrics(baseMetric, checks, durationMS)
	if err != nil {
		return nil, err
	}
	info, err := r.computeMetricRenderInfo(checkMetrics, indent, 0)
	if err != nil {
		return nil, err
	}

	lines := make([]string, 0, len(checkMetrics)+len(checks)*2+2)
	for _, name := range []string{"checks_total", "checks_succeeded", "checks_failed"} {
		line, renderErr := r.renderMetricLine(name, checkMetrics[name], info, indent)
		if renderErr != nil {
			return nil, renderErr
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	for _, check := range checks {
		if check.Fails == 0 {
			lines = append(lines, strings.Repeat(" ", indent)+r.formatter.decorate(
				consoleReportSuccessMark+" "+check.Name,
				consoleANSIGreen,
			))
			continue
		}
		total := check.Passes + check.Fails
		successPercentage := int64(0)
		if total > 0 {
			successPercentage = 100 * check.Passes / total
		}
		lines = append(lines,
			strings.Repeat(" ", indent)+r.formatter.decorate(
				consoleReportFailureMark+" "+check.Name,
				consoleANSIRed,
			),
			strings.Repeat(" ", indent+2)+r.formatter.decorate(fmt.Sprintf(
				"%s  %d%% — %s %d / %s %d",
				consoleReportSubtitlePrefix,
				successPercentage,
				consoleReportSuccessMark,
				check.Passes,
				consoleReportFailureMark,
				check.Fails,
			), consoleANSIRed),
		)
	}
	lines = append(lines, "")
	return lines, nil
}

func (r consoleReportRenderer) renderGroup(
	group k6SummaryGroup,
	durationMS float64,
	indent int,
) ([]string, error) {
	lines := make([]string, 0)
	if len(group.Checks) > 0 {
		checkLines, err := r.renderChecks(nil, group.Checks, durationMS, indent)
		if err != nil {
			return nil, err
		}
		lines = append(lines, checkLines...)
	}
	for _, child := range group.Groups {
		lines = append(lines,
			strings.Repeat(" ", indent)+consoleReportSubtitlePrefix+" "+r.formatter.bold("GROUP: "+child.Name)+" ",
			"",
		)
		childLines, err := r.renderGroup(child, durationMS, indent+2)
		if err != nil {
			return nil, err
		}
		lines = append(lines, childLines...)
	}
	return lines, nil
}

func (r consoleReportRenderer) renderMetricLine(
	name string,
	metric k6SummaryMetric,
	info consoleMetricRenderInfo,
	indent int,
) (string, error) {
	displayedName := consoleMetricDisplayName(name)
	dotsCount := max(3, info.maxNameWidth-(indent+consoleStringWidth(displayedName))+3)
	dottedName := displayedName + r.formatter.decorate(strings.Repeat(".", dotsCount)+":", consoleANSIWhite, consoleANSIFaint)

	var data string
	switch metric.Type {
	case metrics.Trend.String():
		values := info.trendValues[name]
		parts := make([]string, len(values))
		for index, value := range values {
			padding := strings.Repeat(" ", info.trendColumnWidths[index]-consoleStringWidth(value))
			parts[index] = r.trendStats[index] + "=" + r.formatter.decorate(value, consoleANSICyan) + padding
		}
		data = strings.Join(parts, " ")
	case metrics.Counter.String(), metrics.Gauge.String(), metrics.Rate.String():
		value := info.nonTrendValues[name]
		data = r.formatter.decorate(value, consoleANSICyan)
		data += strings.Repeat(" ", info.maxNonTrendValueLen-consoleStringWidth(value))
		extras := info.nonTrendExtras[name]
		if len(extras) == 1 {
			data += " " + r.formatter.decorate(extras[0], consoleANSICyan, consoleANSIFaint)
		} else if len(extras) > 1 {
			parts := make([]string, len(extras))
			for index, extra := range extras {
				padding := strings.Repeat(" ", info.nonTrendExtraWidths[index]-consoleStringWidth(extra))
				parts[index] = r.formatter.decorate(extra, consoleANSICyan, consoleANSIFaint) + padding
			}
			data += " " + strings.Join(parts, " ")
		}
	default:
		return "", fmt.Errorf("render console metric %q: unsupported type %q", name, metric.Type)
	}
	return strings.Repeat(" ", indent) + dottedName + " " + data, nil
}

func (r consoleReportRenderer) computeMetricRenderInfo(
	metricsData map[string]k6SummaryMetric,
	indent int,
	globalMaxNameWidth int,
) (consoleMetricRenderInfo, error) {
	info := consoleMetricRenderInfo{
		maxNameWidth:      globalMaxNameWidth,
		trendValues:       make(map[string][]string),
		trendKeys:         make(map[string]map[string]string),
		trendColumnWidths: make([]int, len(r.trendStats)),
		nonTrendValues:    make(map[string]string),
		nonTrendExtras:    make(map[string][]string),
	}
	for _, name := range sortedConsoleMetricNames(metricsData) {
		metric := metricsData[name]
		if globalMaxNameWidth == 0 {
			info.maxNameWidth = max(
				info.maxNameWidth,
				indent+consoleStringWidth(consoleMetricDisplayName(name)),
			)
		}

		if metric.Type == metrics.Trend.String() {
			values := make([]string, len(r.trendStats))
			for index, stat := range r.trendStats {
				value, exists := metric.Values[stat]
				if !exists {
					return consoleMetricRenderInfo{}, fmt.Errorf("render console metric %q: trend statistic %q is missing", name, stat)
				}
				values[index] = r.renderTrendValue(value, stat, metric)
				info.trendColumnWidths[index] = max(info.trendColumnWidths[index], consoleStringWidth(values[index]))
			}
			info.trendValues[name] = values
			keys := make(map[string]string)
			for stat, value := range metric.Values {
				if !containsString(r.trendStats, stat) {
					keys[stat] = r.renderTrendValue(value, stat, metric)
				}
			}
			info.trendKeys[name] = keys
			continue
		}

		values, err := r.renderNonTrendValues(name, metric)
		if err != nil {
			return consoleMetricRenderInfo{}, err
		}
		info.nonTrendValues[name] = values[0]
		info.maxNonTrendValueLen = max(info.maxNonTrendValueLen, consoleStringWidth(values[0]))
		extras := values[1:]
		info.nonTrendExtras[name] = extras
		for index, extra := range extras {
			for len(info.nonTrendExtraWidths) <= index {
				info.nonTrendExtraWidths = append(info.nonTrendExtraWidths, 0)
			}
			info.nonTrendExtraWidths[index] = max(info.nonTrendExtraWidths[index], consoleStringWidth(extra))
		}
	}
	return info, nil
}

func (r consoleReportRenderer) renderTrendValue(value float64, stat string, metric k6SummaryMetric) string {
	if stat == "count" {
		return consoleNumber(value)
	}
	return consoleHumanizeValue(value, metric, r.timeUnit)
}

func (r consoleReportRenderer) renderNonTrendValues(
	name string,
	metric k6SummaryMetric,
) ([]string, error) {
	required := func(key string) (float64, error) {
		value, exists := metric.Values[key]
		if !exists {
			return 0, fmt.Errorf("render console metric %q: value %q is missing", name, key)
		}
		return value, nil
	}
	switch metric.Type {
	case metrics.Counter.String():
		count, err := required("count")
		if err != nil {
			return nil, err
		}
		rate, err := required("rate")
		if err != nil {
			return nil, err
		}
		return []string{
			consoleHumanizeValue(count, metric, r.timeUnit),
			consoleHumanizeValue(rate, metric, r.timeUnit) + "/s",
		}, nil
	case metrics.Gauge.String():
		value, err := required("value")
		if err != nil {
			return nil, err
		}
		minimum, err := required("min")
		if err != nil {
			return nil, err
		}
		maximum, err := required("max")
		if err != nil {
			return nil, err
		}
		return []string{
			consoleHumanizeValue(value, metric, r.timeUnit),
			"min=" + consoleHumanizeValue(minimum, metric, r.timeUnit),
			"max=" + consoleHumanizeValue(maximum, metric, r.timeUnit),
		}, nil
	case metrics.Rate.String():
		rate, err := required("rate")
		if err != nil {
			return nil, err
		}
		passes, err := required("passes")
		if err != nil {
			return nil, err
		}
		fails, err := required("fails")
		if err != nil {
			return nil, err
		}
		return []string{
			consoleHumanizeValue(rate, metric, r.timeUnit),
			consoleNumber(passes) + " out of " + consoleNumber(passes+fails),
		}, nil
	default:
		return nil, fmt.Errorf("render console metric %q: unsupported type %q", name, metric.Type)
	}
}

func consoleMetricCategories(metricsData map[string]k6SummaryMetric) []consoleMetricCategory {
	categories := []consoleMetricCategory{
		{name: "CUSTOM", metrics: make(map[string]k6SummaryMetric)},
		{name: "HTTP", metrics: make(map[string]k6SummaryMetric)},
		{name: "EXECUTION", metrics: make(map[string]k6SummaryMetric)},
		{name: "NETWORK", metrics: make(map[string]k6SummaryMetric)},
		{name: "BROWSER", metrics: make(map[string]k6SummaryMetric)},
		{name: "WEBVITALS", metrics: make(map[string]k6SummaryMetric)},
		{name: "GRPC", metrics: make(map[string]k6SummaryMetric)},
		{name: "WEBSOCKET", metrics: make(map[string]k6SummaryMetric)},
	}
	hasTaggedChecks := false
	for name := range metricsData {
		if strings.HasPrefix(name, metrics.ChecksName+"{") {
			hasTaggedChecks = true
			break
		}
	}
	for name, metric := range metricsData {
		if name == metrics.ChecksName && !hasTaggedChecks {
			continue
		}
		categoryIndex := consoleMetricCategoryIndex(name)
		categories[categoryIndex].metrics[name] = metric
	}
	return categories
}

func consoleMetricCategoryIndex(name string) int {
	baseName := consoleMetricParentName(name)
	switch {
	case hasAnyPrefix(baseName,
		metrics.HTTPReqsName,
		metrics.HTTPReqFailedName,
		metrics.HTTPReqDurationName,
		metrics.HTTPReqBlockedName,
		metrics.HTTPReqConnectingName,
		metrics.HTTPReqTLSHandshakingName,
		metrics.HTTPReqSendingName,
		metrics.HTTPReqWaitingName,
		metrics.HTTPReqReceivingName,
	):
		return 1
	case hasAnyPrefix(baseName,
		metrics.VUsName,
		metrics.VUsMaxName,
		metrics.IterationsName,
		metrics.IterationDurationName,
		metrics.DroppedIterationsName,
	):
		return 2
	case hasAnyPrefix(baseName, metrics.DataSentName, metrics.DataReceivedName):
		return 3
	case strings.HasPrefix(baseName, "browser_web_vital_"):
		return 5
	case strings.HasPrefix(baseName, "browser_"):
		return 4
	case strings.HasPrefix(baseName, "grpc_"):
		return 6
	case strings.HasPrefix(baseName, "ws_"):
		return 7
	default:
		return 0
	}
}

func consoleCheckMetrics(
	baseMetric *k6SummaryMetric,
	checks []k6SummaryCheck,
	durationMS float64,
) (map[string]k6SummaryMetric, error) {
	var successes, failures float64
	if baseMetric != nil {
		var successExists, failureExists bool
		successes, successExists = baseMetric.Values["passes"]
		failures, failureExists = baseMetric.Values["fails"]
		if !successExists || !failureExists {
			return nil, fmt.Errorf("render console checks: pass/fail totals are missing")
		}
	} else {
		for _, check := range checks {
			successes += float64(check.Passes)
			failures += float64(check.Fails)
		}
	}
	total := successes + failures
	ratePerSecond := 0.0
	if durationMS > 0 {
		ratePerSecond = total / (durationMS / 1000)
	}
	successRate := 0.0
	failureRate := 0.0
	if total > 0 {
		successRate = successes / total
		failureRate = failures / total
	}
	return map[string]k6SummaryMetric{
		"checks_total": {
			Type: metrics.Counter.String(), Contains: metrics.Default.String(),
			Values: map[string]float64{"count": total, "rate": ratePerSecond},
		},
		"checks_succeeded": {
			Type: metrics.Rate.String(), Contains: metrics.Default.String(),
			Values: map[string]float64{"rate": successRate, "passes": successes, "fails": failures},
		},
		"checks_failed": {
			Type: metrics.Rate.String(), Contains: metrics.Default.String(),
			Values: map[string]float64{"rate": failureRate, "passes": failures, "fails": successes},
		},
	}, nil
}

func consoleHumanizeValue(value float64, metric k6SummaryMetric, timeUnit string) string {
	if metric.Type == metrics.Rate.String() {
		percentage := math.Trunc(value*10000) / 100
		return strconv.FormatFloat(percentage, 'f', 2, 64) + "%"
	}
	switch metric.Contains {
	case metrics.Data.String():
		return consoleHumanizeBytes(value)
	case metrics.Time.String():
		return consoleHumanizeDuration(value, timeUnit)
	default:
		return consoleNumber(value)
	}
}

func consoleHumanizeBytes(value float64) string {
	units := []string{"B", "kB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"}
	if value < 10 {
		return consoleNumber(value) + " B"
	}
	exponent := int(math.Floor(math.Log(value) / math.Log(1000)))
	exponent = min(max(exponent, 0), len(units)-1)
	scaled := math.Floor((value/math.Pow(1000, float64(exponent)))*10+0.5) / 10
	precision := 0
	if scaled < 10 {
		precision = 1
	}
	return strconv.FormatFloat(scaled, 'f', precision, 64) + " " + units[exponent]
}

func consoleHumanizeDuration(value float64, timeUnit string) string {
	switch timeUnit {
	case "s":
		return strconv.FormatFloat(value*0.001, 'f', 2, 64) + "s"
	case "ms":
		return strconv.FormatFloat(value, 'f', 2, 64) + "ms"
	case "us":
		return strconv.FormatFloat(value*1000, 'f', 2, 64) + "µs"
	}
	if value == 0 {
		return "0s"
	}
	if value < 0.001 {
		return consoleNumber(math.Trunc(value*1_000_000)) + "ns"
	}
	if value < 1 {
		return consoleTruncatedNumber(value*1000, 2) + "µs"
	}
	if value < 1000 {
		return consoleTruncatedNumber(value, 2) + "ms"
	}
	precision := 2
	if value > 60_000 {
		precision = 0
	}
	seconds := consoleTruncatedNumber(math.Mod(value, 60_000)/1000, precision) + "s"
	minutes := int64(value / 60_000)
	if minutes < 1 {
		return seconds
	}
	result := strconv.FormatInt(minutes%60, 10) + "m" + seconds
	hours := minutes / 60
	if hours < 1 {
		return result
	}
	return strconv.FormatInt(hours, 10) + "h" + result
}

func consoleTruncatedNumber(value float64, precision int) string {
	multiplier := math.Pow10(precision)
	return consoleNumber(math.Trunc(value*multiplier) / multiplier)
}

func consoleNumber(value float64) string {
	formatted := strconv.FormatFloat(value, 'f', 6, 64)
	formatted = strings.TrimRight(formatted, "0")
	formatted = strings.TrimRight(formatted, ".")
	if formatted == "-0" || formatted == "" {
		return "0"
	}
	return formatted
}

func sortedConsoleMetricNames(metricsData map[string]k6SummaryMetric) []string {
	names := make([]string, 0, len(metricsData))
	for name := range metricsData {
		names = append(names, name)
	}
	slices.SortFunc(names, func(left, right string) int {
		leftParent := consoleMetricParentName(left)
		rightParent := consoleMetricParentName(right)
		if parentOrder := cmp.Compare(leftParent, rightParent); parentOrder != 0 {
			return parentOrder
		}
		return cmp.Compare(strings.TrimPrefix(left, leftParent), strings.TrimPrefix(right, rightParent))
	})
	return names
}

func maxConsoleMetricNameWidth(metricsData map[string]k6SummaryMetric, indent int) int {
	width := 0
	for name := range metricsData {
		width = max(width, indent+consoleStringWidth(consoleMetricDisplayName(name)))
	}
	return width
}

func consoleMetricParentName(name string) string {
	if before, _, ok := strings.Cut(name, "{"); ok {
		return before
	}
	return name
}

func consoleMetricDisplayName(name string) string {
	if submetric := strings.IndexByte(name, '{'); submetric >= 0 && strings.HasSuffix(name, "}") {
		return "  { " + name[submetric+1:len(name)-1] + " }"
	}
	return name
}

func orderedThresholdSources(metric k6SummaryMetric) []string {
	if len(metric.ThresholdOrder) == len(metric.Thresholds) {
		return slices.Clone(metric.ThresholdOrder)
	}
	return slices.Sorted(maps.Keys(metric.Thresholds))
}

func consoleStringWidth(value string) int {
	return utf8.RuneCountInString(value)
}

func containsString(values []string, expected string) bool {
	return slices.Contains(values, expected)
}

func hasAnyPrefix(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

const (
	consoleANSIRed   = "31"
	consoleANSIGreen = "32"
	consoleANSICyan  = "36"
	consoleANSIWhite = "37"
	consoleANSIFaint = "2"
)

func (f consoleANSIFormatter) decorate(value string, color string, styles ...string) string {
	if !f.enabled {
		return value
	}
	codes := append(append(make([]string, 0, len(styles)+1), styles...), color)
	return "\x1b[" + strings.Join(codes, ";") + "m" + value + "\x1b[0m"
}

func (f consoleANSIFormatter) bold(value string) string {
	if !f.enabled {
		return value
	}
	return "\x1b[1m" + value + "\x1b[0m"
}
