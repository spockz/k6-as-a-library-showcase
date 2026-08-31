// artifact_validation.go isolates structural checks and atomic publication required for trustworthy reports.
package artifact

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"go.k6.io/k6/lib/fsext"
	"go.k6.io/k6/metrics"
)

func ValidateK6JSON(filename string) (err error) {
	file, err := OpenForValidation(filename)
	if err != nil {
		return fmt.Errorf("validate k6 JSON artifact %q: %w", filename, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close k6 JSON artifact %q: %w", filename, closeErr))
		}
	}()

	reader := bufio.NewReader(file)
	declaredMetrics := make(map[string]struct{})
	metricRecords := 0
	pointRecords := 0
	lineNumber := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNumber++
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				return fmt.Errorf("validate k6 JSON artifact %q line %d: empty record", filename, lineNumber)
			}

			recordType, metricName, recordErr := validateK6JSONRecord(trimmed, declaredMetrics)
			if recordErr != nil {
				return fmt.Errorf("validate k6 JSON artifact %q line %d: %w", filename, lineNumber, recordErr)
			}
			switch recordType {
			case "Metric":
				metricRecords++
				declaredMetrics[metricName] = struct{}{}
			case "Point":
				pointRecords++
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fmt.Errorf("read k6 JSON artifact %q: %w", filename, readErr)
		}
	}

	if metricRecords == 0 {
		return fmt.Errorf("validate k6 JSON artifact %q: no metric records found", filename)
	}
	if pointRecords == 0 {
		return fmt.Errorf("validate k6 JSON artifact %q: no point records found", filename)
	}
	return nil
}

func validateK6JSONRecord(line []byte, declaredMetrics map[string]struct{}) (string, string, error) {
	fields, err := decodeJSONObject(line, "record")
	if err != nil {
		return "", "", err
	}
	metricName, err := requiredJSONString(fields, "metric")
	if err != nil {
		return "", "", err
	}
	recordType, err := requiredJSONString(fields, "type")
	if err != nil {
		return "", "", err
	}
	dataRaw, err := requiredJSONField(fields, "data")
	if err != nil {
		return "", "", err
	}
	data, err := decodeJSONObject(dataRaw, "data")
	if err != nil {
		return "", "", err
	}

	switch recordType {
	case "Metric":
		if err := validateK6MetricRecord(metricName, data); err != nil {
			return "", "", err
		}
	case "Point":
		if _, declared := declaredMetrics[metricName]; !declared {
			return "", "", fmt.Errorf("point references undeclared metric %q", metricName)
		}
		if err := validateK6PointRecord(data); err != nil {
			return "", "", err
		}
	default:
		return "", "", fmt.Errorf("unsupported record type %q", recordType)
	}
	return recordType, metricName, nil
}

func validateK6MetricRecord(metricName string, data map[string]json.RawMessage) error {
	dataName, err := requiredJSONString(data, "name")
	if err != nil {
		return err
	}
	if dataName != metricName {
		return fmt.Errorf("metric name %q does not match data.name %q", metricName, dataName)
	}
	metricType, err := requiredJSONString(data, "type")
	if err != nil {
		return err
	}
	if !validK6MetricType(metricType) {
		return fmt.Errorf("unsupported metric type %q", metricType)
	}
	valueType, err := requiredJSONString(data, "contains")
	if err != nil {
		return err
	}
	if !validK6ValueType(valueType) {
		return fmt.Errorf("unsupported metric value kind %q", valueType)
	}
	thresholds, err := requiredJSONField(data, "thresholds")
	if err != nil {
		return err
	}
	thresholdValues, err := decodeJSONArray(thresholds, "thresholds")
	if err != nil {
		return err
	}
	for index, threshold := range thresholdValues {
		if err := validateK6Threshold(threshold); err != nil {
			return fmt.Errorf("threshold %d: %w", index, err)
		}
	}
	submetrics, present := data["submetrics"]
	if !present {
		return errors.New(`required field "submetrics" is missing`)
	}
	if bytes.Equal(bytes.TrimSpace(submetrics), []byte("null")) {
		return nil
	}
	submetricValues, err := decodeJSONArray(submetrics, "submetrics")
	if err != nil {
		return err
	}
	for index, submetric := range submetricValues {
		if err := validateK6Submetric(submetric); err != nil {
			return fmt.Errorf("submetric %d: %w", index, err)
		}
	}
	return nil
}

func validateK6Threshold(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("threshold must be a string or object")
	}
	if trimmed[0] == '"' {
		_, err := decodeJSONString(trimmed, "threshold")
		if err != nil {
			return err
		}
		return nil
	}
	fields, err := decodeJSONObject(trimmed, "threshold")
	if err != nil {
		return err
	}
	if _, err := requiredJSONString(fields, "threshold"); err != nil {
		return err
	}
	return nil
}

func validateK6Submetric(raw json.RawMessage) error {
	fields, err := decodeJSONObject(raw, "submetric")
	if err != nil {
		return err
	}
	if _, err := requiredJSONString(fields, "name"); err != nil {
		return err
	}
	if _, err := requiredJSONString(fields, "suffix"); err != nil {
		return err
	}
	tags, present := fields["tags"]
	if !present {
		return errors.New(`required field "tags" is missing`)
	}
	return validateJSONStringObject(tags, "submetric tags", true)
}

func validateK6PointRecord(data map[string]json.RawMessage) error {
	timestamp, err := requiredJSONField(data, "time")
	if err != nil {
		return err
	}
	var at time.Time
	if err := json.Unmarshal(timestamp, &at); err != nil {
		return fmt.Errorf("decode time: %w", err)
	}
	value, err := requiredJSONField(data, "value")
	if err != nil {
		return err
	}
	var number float64
	if err := json.Unmarshal(value, &number); err != nil {
		return fmt.Errorf("decode value: %w", err)
	}
	tags, present := data["tags"]
	if !present {
		return errors.New(`required field "tags" is missing`)
	}
	if err := validateJSONStringObject(tags, "tags", true); err != nil {
		return err
	}
	metadata, present := data["metadata"]
	if present {
		if err := validateJSONStringObject(metadata, "metadata", true); err != nil {
			return err
		}
	}
	return nil
}

func ValidateHTML(filename string) (err error) {
	file, err := OpenForValidation(filename)
	if err != nil {
		return fmt.Errorf("validate generated HTML artifact %q: %w", filename, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close generated HTML artifact %q: %w", filename, closeErr))
		}
	}()

	var reader io.Reader = file
	var compressedReader *gzip.Reader
	if filepath.Ext(filename) == ".gz" {
		compressedReader, err = gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("open compressed HTML document: %w", err)
		}
		defer func() {
			if closeErr := compressedReader.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close compressed HTML document: %w", closeErr))
			}
		}()
		reader = compressedReader
	}
	contents, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read HTML document: %w", err)
	}
	return ValidateHTMLContents(contents)
}

func ValidateHTMLContents(contents []byte) error {
	tags, err := scanHTMLTags(contents)
	if err != nil {
		return fmt.Errorf("scan HTML document: %w", err)
	}
	if !tags.doctype {
		return errors.New("HTML document is missing a doctype")
	}
	for _, element := range []string{"html", "head", "body"} {
		if !tags.starts[element] {
			return fmt.Errorf("HTML document is missing a %s element", element)
		}
		if !tags.ends[element] {
			return fmt.Errorf("HTML document is missing a closing %s tag", element)
		}
	}
	return nil
}

type htmlTagSet struct {
	doctype bool
	starts  map[string]bool
	ends    map[string]bool
}

func scanHTMLTags(contents []byte) (htmlTagSet, error) {
	tags := htmlTagSet{
		starts: make(map[string]bool),
		ends:   make(map[string]bool),
	}
	for position := 0; position < len(contents); {
		relativeStart := bytes.IndexByte(contents[position:], '<')
		if relativeStart < 0 {
			break
		}
		position += relativeStart
		if position+1 >= len(contents) {
			return htmlTagSet{}, errors.New("unterminated tag")
		}

		switch {
		case hasHTMLPrefix(contents[position:], "<!--"):
			commentEnd := bytes.Index(contents[position+4:], []byte("-->"))
			if commentEnd < 0 {
				return htmlTagSet{}, errors.New("unterminated comment")
			}
			position += 4 + commentEnd + 3
		case hasHTMLPrefix(contents[position:], "<!doctype"):
			tagEnd := htmlTagEnd(contents, position+2)
			if tagEnd < 0 {
				return htmlTagSet{}, errors.New("unterminated doctype")
			}
			fields := strings.Fields(string(contents[position+2 : tagEnd]))
			if len(fields) >= 2 && strings.EqualFold(fields[0], "doctype") && strings.EqualFold(fields[1], "html") {
				tags.doctype = true
			}
			position = tagEnd + 1
		case contents[position+1] == '!' || contents[position+1] == '?':
			tagEnd := htmlTagEnd(contents, position+2)
			if tagEnd < 0 {
				return htmlTagSet{}, errors.New("unterminated declaration")
			}
			position = tagEnd + 1
		case contents[position+1] == '/':
			name, nameEnd, ok := htmlTagName(contents, position+2)
			if !ok {
				position++
				continue
			}
			tagEnd := htmlTagEnd(contents, nameEnd)
			if tagEnd < 0 {
				return htmlTagSet{}, fmt.Errorf("unterminated closing %s tag", name)
			}
			tags.ends[name] = true
			position = tagEnd + 1
		default:
			name, nameEnd, ok := htmlTagName(contents, position+1)
			if !ok {
				position++
				continue
			}
			tagEnd := htmlTagEnd(contents, nameEnd)
			if tagEnd < 0 {
				return htmlTagSet{}, fmt.Errorf("unterminated %s tag", name)
			}
			tags.starts[name] = true
			position = tagEnd + 1
			if name == "script" || name == "style" {
				closingTag := htmlClosingTagIndex(contents, position, name)
				if closingTag < 0 {
					return htmlTagSet{}, fmt.Errorf("unterminated %s element", name)
				}
				position = closingTag
			}
		}
	}
	return tags, nil
}

func hasHTMLPrefix(contents []byte, prefix string) bool {
	if len(contents) < len(prefix) {
		return false
	}
	return strings.EqualFold(string(contents[:len(prefix)]), prefix)
}

func htmlTagName(contents []byte, start int) (string, int, bool) {
	position := start
	for position < len(contents) && isHTMLTagNameByte(contents[position]) {
		position++
	}
	if position == start {
		return "", start, false
	}
	return strings.ToLower(string(contents[start:position])), position, true
}

func isHTMLTagNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == ':' || value == '-'
}

func htmlTagEnd(contents []byte, start int) int {
	var quote byte
	for position := start; position < len(contents); position++ {
		value := contents[position]
		if quote != 0 {
			if value == quote {
				quote = 0
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		if value == '>' {
			return position
		}
	}
	return -1
}

func htmlClosingTagIndex(contents []byte, start int, name string) int {
	needle := []byte("</" + name)
	for position := start; position+len(needle) <= len(contents); position++ {
		if !equalHTMLBytes(contents[position:position+len(needle)], needle) {
			continue
		}
		if position+len(needle) == len(contents) || !isHTMLTagNameByte(contents[position+len(needle)]) {
			return position
		}
	}
	return -1
}

func equalHTMLBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] == right[index] {
			continue
		}
		if left[index] >= 'A' && left[index] <= 'Z' {
			if left[index]+('a'-'A') == right[index] {
				continue
			}
		}
		if right[index] >= 'A' && right[index] <= 'Z' && right[index]+('a'-'A') == left[index] {
			continue
		}
		return false
	}
	return true
}

func OpenForValidation(filename string) (*os.File, error) {
	if filename == "" {
		return nil, errors.New("filename is empty")
	}
	info, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("artifact is not a regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func PublishAtomically(
	filename string,
	validate func(string) error,
	generate func(io.Writer) error,
) (returnErr error) {
	if validate == nil {
		return errors.New("publish artifact: validation function is nil")
	}
	return PublishAtomicallyWithFS(fsext.NewOsFs(), filename, validate, generate)
}

func PublishAtomicallyWithFS(
	fs fsext.Fs,
	filename string,
	validate func(string) error,
	generate func(io.Writer) error,
) (returnErr error) {
	if filename == "" {
		return errors.New("publish artifact: filename is empty")
	}
	if fs == nil {
		return errors.New("publish artifact: filesystem is nil")
	}
	if generate == nil {
		return errors.New("publish artifact: generation function is nil")
	}

	directory := filepath.Dir(filename)
	pattern := "." + filepath.Base(filename) + ".tmp-*"
	temporary, err := afero.TempFile(fs, directory, pattern)
	if err != nil {
		return fmt.Errorf("create temporary artifact for %q: %w", filename, err)
	}
	temporaryName := temporary.Name()
	published := false
	defer func() {
		if published {
			return
		}
		if removeErr := fs.Remove(temporaryName); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("remove temporary artifact %q: %w", temporaryName, removeErr),
			)
		}
	}()

	writer := bufio.NewWriter(temporary)
	generationErr := generate(writer)
	flushErr := writer.Flush()
	closeErr := temporary.Close()
	if generationErr != nil || flushErr != nil || closeErr != nil {
		return fmt.Errorf(
			"generate artifact %q: %w",
			filename,
			errors.Join(generationErr, flushErr, closeErr),
		)
	}
	if validate != nil {
		if err := validate(temporaryName); err != nil {
			return fmt.Errorf("validate temporary artifact %q: %w", filename, err)
		}
	}
	if err := fs.Rename(temporaryName, filename); err != nil {
		return fmt.Errorf("publish artifact %q: %w", filename, err)
	}
	published = true
	return nil
}

func decodeJSONObject(raw []byte, fieldName string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("decode %s object: %w", fieldName, err)
	}
	if fields == nil {
		return nil, fmt.Errorf("%s must be an object", fieldName)
	}
	return fields, nil
}

func requiredJSONField(fields map[string]json.RawMessage, name string) (json.RawMessage, error) {
	value, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, fmt.Errorf("required field %q is missing or null", name)
	}
	return value, nil
}

func requiredJSONString(fields map[string]json.RawMessage, name string) (string, error) {
	value, err := requiredJSONField(fields, name)
	if err != nil {
		return "", err
	}
	return decodeJSONString(value, name)
}

func decodeJSONString(raw []byte, fieldName string) (string, error) {
	value, err := decodeJSONAnyString(raw, fieldName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s string is empty", fieldName)
	}
	return value, nil
}

func decodeJSONAnyString(raw []byte, fieldName string) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("decode %s string: %w", fieldName, err)
	}
	return value, nil
}

func decodeJSONArray(raw json.RawMessage, fieldName string) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("%s must be an array", fieldName)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil {
		return nil, fmt.Errorf("decode %s array: %w", fieldName, err)
	}
	return values, nil
}

func validateJSONStringObject(raw json.RawMessage, fieldName string, allowNull bool) error {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		if allowNull {
			return nil
		}
		return fmt.Errorf("%s must be an object", fieldName)
	}
	fields, err := decodeJSONObject(trimmed, fieldName)
	if err != nil {
		return err
	}
	for name, value := range fields {
		if _, err := decodeJSONAnyString(value, fmt.Sprintf("%s key %q", fieldName, name)); err != nil {
			return err
		}
	}
	return nil
}

func validK6MetricType(value string) bool {
	switch value {
	case metrics.Counter.String(), metrics.Gauge.String(), metrics.Trend.String(), metrics.Rate.String():
		return true
	default:
		return false
	}
}

func validK6ValueType(value string) bool {
	switch value {
	case metrics.Default.String(), metrics.Time.String(), metrics.Data.String():
		return true
	default:
		return false
	}
}
