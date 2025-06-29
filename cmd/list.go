package cmd

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/PortNumber53/task-sync/internal"
)

func HandleStepList(db *sql.DB) {
	full := false
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--full" {
			full = true
		}
	}
	if err := internal.ListSteps(db, full); err != nil {
		fmt.Printf("List steps error: %v\n", err)
		os.Exit(1)
	}
}
