package engine

import (
	"strings"
	"testing"
)

// Списки не читают снимок фактов: замена колонки не должна тихо перестать срабатывать.
func TestTaskListSkipsMaterialized(t *testing.T) {
	if strings.Contains(taskListCols, "materialized") || !strings.Contains(taskListCols, "NULL::jsonb") {
		t.Fatalf("taskListCols still reads the snapshot: %s", taskListCols)
	}
	if strings.Count(taskListCols, ",") != strings.Count(taskCols, ",") {
		t.Fatal("column count changed: scanTask would break")
	}
}
