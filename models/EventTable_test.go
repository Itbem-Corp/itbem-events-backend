package models

import "testing"

func TestEventTablePreservesProductionPhysicalTableName(t *testing.T) {
	if got := (EventTable{}).TableName(); got != "tables" {
		t.Fatalf("EventTable.TableName() = %q, want tables", got)
	}
}
