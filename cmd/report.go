package cmd

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/PortNumber53/task-sync/internal"
)

// HandleReport parses CLI args for `task report` and runs the report logic.
func HandleReport(db *sql.DB) {
	if len(os.Args) < 4 || os.Args[2] != "report" {
		fmt.Println("Usage: task report <TASK_ID>")
		os.Exit(1)
	}



taskID := 0
	_, err := fmt.Sscanf(os.Args[3], "%d", &taskID)
	if err != nil || taskID == 0 {
		fmt.Println("Invalid TASK_ID")
		os.Exit(1)
	}

	if err := internal.ReportTask(db, taskID); err != nil {
		fmt.Printf("Report error: %v\n", err)
		os.Exit(1)
	}
}
