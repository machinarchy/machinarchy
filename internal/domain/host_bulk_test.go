package domain

import "testing"

func TestNewBulkImportResponseCountsMatchResults(t *testing.T) {
	results := []HostOperationResult{
		{Index: 0, Status: StatusSuccess},
		{Index: 1, Status: StatusFailed},
		{Index: 2, Status: StatusDuplicate},
		{Index: 3, Status: StatusSuccess},
		{Index: 4, Status: StatusValid},
		{Index: 5, Status: ""}, // an unset status must never count as success
	}
	got := NewBulkImportResponse(false, results)
	if got.TotalProcessed != 6 || got.SuccessCount != 2 || got.FailureCount != 2 ||
		got.DuplicateCount != 1 || got.ValidCount != 1 {
		t.Fatalf("unexpected counters: %+v", got)
	}
	if got.SuccessCount+got.FailureCount+got.DuplicateCount+got.ValidCount != got.TotalProcessed {
		t.Fatalf("counters do not add up to the total: %+v", got)
	}
}
