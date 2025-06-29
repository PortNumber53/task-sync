package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"

	helpPkg "github.com/PortNumber53/task-sync/help"
	"github.com/PortNumber53/task-sync/internal"
)

func HandleStepDelete(db *sql.DB) {
	if len(os.Args) < 4 {
		fmt.Println("Error: delete subcommand requires a step ID.")
		helpPkg.PrintStepHelp()
		os.Exit(1)
	}
	stepID, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Printf("Error: invalid step ID '%s'. Must be an integer.\n", os.Args[3])
		os.Exit(1)
	}

	if err := internal.DeleteStep(db, stepID); err != nil {
		fmt.Printf("Error deleting step: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Step %d deleted successfully.\n", stepID)
}
