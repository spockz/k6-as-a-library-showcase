// These bounds keep native workload tracing metadata safe at the external API boundary.
package otel

import "strings"

const (
	MaxAttributeValueLength = 256
	MaxSpanNameLength       = 128
	MaxErrorTypeLength      = 128
	maxConfigTextLength     = MaxAttributeValueLength
	maxMismatchCount        = 1000
)

func boundedString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}
