package cmd

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/PortNumber53/task-sync/internal"
)

func HandleStepList(db *sql.DB) {
	full := false
	for _, arg := range os.Args {
		if arg == "--full" {
			full = true
			break
		}
	}

	if err := internal.ListSteps(db, full); err != nil {
		fmt.Printf("Error listing steps: %v\n", err)
		os.Exit(1)
	}
}
