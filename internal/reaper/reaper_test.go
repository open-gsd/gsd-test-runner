package reaper

import (
	"reflect"
	"testing"
)

func TestOverdue_PastDeadlineSelectedFutureSkipped(t *testing.T) {
	now := int64(1_000_000)
	got := Overdue([]Container{
		{ID: "past", RunID: "a", DeadlineMs: 999_000},
		{ID: "future", RunID: "b", DeadlineMs: 1_001_000},
	}, now)
	if len(got) != 1 || got[0].ID != "past" {
		t.Fatalf("Overdue = %+v, want [past]", got)
	}
}

func TestOverdue_Cases(t *testing.T) {
	now := int64(1_000_000)
	tests := []struct {
		name       string
		containers []Container
		want       []string // container IDs expected
	}{
		{"exactly at deadline is overdue", []Container{{ID: "x", DeadlineMs: now}}, []string{"x"}},
		{"missing deadline is never reaped", []Container{{ID: "x", DeadlineMs: 0}}, nil},
		{"empty input", nil, nil},
		{"multiple overdue preserved in order", []Container{
			{ID: "a", DeadlineMs: 1},
			{ID: "fut", DeadlineMs: now + 1},
			{ID: "b", DeadlineMs: 2},
		}, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotIDs []string
			for _, c := range Overdue(tt.containers, now) {
				gotIDs = append(gotIDs, c.ID)
			}
			if !reflect.DeepEqual(gotIDs, tt.want) {
				t.Errorf("Overdue IDs = %v, want %v", gotIDs, tt.want)
			}
		})
	}
}

func TestLabelConstants(t *testing.T) {
	if LabelRunID != "sh.gsd-test.run-id" {
		t.Errorf("LabelRunID = %q", LabelRunID)
	}
	if LabelDeadline != "sh.gsd-test.deadline" {
		t.Errorf("LabelDeadline = %q", LabelDeadline)
	}
}
