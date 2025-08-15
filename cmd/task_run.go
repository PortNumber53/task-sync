package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"

	helpPkg "github.com/PortNumber53/task-sync/help"
	"github.com/PortNumber53/task-sync/internal"
)

// HandleTaskRunID runs all steps for a specific task, similar to run-steps but filtered by task ID.
func HandleTaskRunID(db *sql.DB) {
	if len(os.Args) < 4 {
		fmt.Println("Error: run-id subcommand requires a TASK_ID.")
		helpPkg.PrintTaskRunIDHelp()
		os.Exit(1)
	}
	taskIDStr := os.Args[3]
	taskID, err := strconv.Atoi(taskIDStr)
	if err != nil || taskID <= 0 {
		fmt.Printf("Error: invalid TASK_ID '%s'. Must be a positive integer.\n", taskIDStr)
		os.Exit(1)
	}
    // Parse flags after task ID
    golden := false
    for i := 4; i < len(os.Args); i++ {
        if os.Args[i] == "--golden" {
            golden = true
        }
    }

    fmt.Printf("Running all steps for task ID %d...\n", taskID)
    if err := internal.ProcessStepsForTask(db, taskID, golden); err != nil {
        fmt.Printf("Error processing steps for task: %v\n", err)
        os.Exit(1)
    }
	fmt.Println("All steps for task processed successfully.")
}
