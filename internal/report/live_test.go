package report

import (
	"slices"
	"testing"
)

func TestDashboardTagsUsesGroupByAttributes(t *testing.T) {
	groupBy := []string{"consumer", "interaction"}
	tags := DashboardTags(groupBy)
	groupBy[0] = "changed"
	if !slices.Equal(tags, []string{"consumer", "interaction"}) {
		t.Fatalf("dashboard tags = %v", tags)
	}
}

func TestDashboardTagsFallsBackForAggregateReports(t *testing.T) {
	if tags := DashboardTags(nil); !slices.Equal(tags, []string{DashboardDefaultTag}) {
		t.Fatalf("dashboard tags = %v", tags)
	}
}
