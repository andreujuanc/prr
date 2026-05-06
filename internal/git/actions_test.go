package git

import "testing"

func TestAggregateActionStatus(t *testing.T) {
	tests := []struct {
		name string
		runs []WorkflowRun
		want ActionStatus
	}{
		{"empty", nil, ActionStatusNone},
		{"all passed", []WorkflowRun{
			{Status: "completed", Conclusion: "success"},
			{Status: "completed", Conclusion: "success"},
		}, ActionStatusPassed},
		{"one failed", []WorkflowRun{
			{Status: "completed", Conclusion: "success"},
			{Status: "completed", Conclusion: "failure"},
		}, ActionStatusFailed},
		{"timed out counts as failed", []WorkflowRun{
			{Status: "completed", Conclusion: "timed_out"},
		}, ActionStatusFailed},
		{"in progress", []WorkflowRun{
			{Status: "completed", Conclusion: "success"},
			{Status: "in_progress"},
		}, ActionStatusInProgress},
		{"queued", []WorkflowRun{
			{Status: "queued"},
		}, ActionStatusInProgress},
		{"in_progress takes priority over failed", []WorkflowRun{
			{Status: "completed", Conclusion: "failure"},
			{Status: "in_progress"},
		}, ActionStatusInProgress},
		{"skipped counts as passed", []WorkflowRun{
			{Status: "completed", Conclusion: "skipped"},
		}, ActionStatusPassed},
		{"cancelled counts as passed", []WorkflowRun{
			{Status: "completed", Conclusion: "cancelled"},
		}, ActionStatusPassed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AggregateActionStatus(tt.runs); got != tt.want {
				t.Errorf("AggregateActionStatus() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHasActiveRuns(t *testing.T) {
	tests := []struct {
		name string
		runs []WorkflowRun
		want bool
	}{
		{"empty", nil, false},
		{"all completed", []WorkflowRun{
			{Status: "completed", Conclusion: "success"},
		}, false},
		{"one queued", []WorkflowRun{
			{Status: "completed"}, {Status: "queued"},
		}, true},
		{"one in_progress", []WorkflowRun{
			{Status: "in_progress"},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasActiveRuns(tt.runs); got != tt.want {
				t.Errorf("HasActiveRuns() = %v, want %v", got, tt.want)
			}
		})
	}
}
