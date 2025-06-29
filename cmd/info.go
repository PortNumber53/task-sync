package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	helpPkg "github.com/PortNumber53/task-sync/help"
	"github.com/PortNumber53/task-sync/internal"
)

func HandleStepInfo(db *sql.DB) {
	if len(os.Args) < 4 {
		helpPkg.PrintStepInfoHelp()
		os.Exit(1)
	}

	stepID, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Printf("Error: invalid step ID '%s'\n", os.Args[3])
		helpPkg.PrintStepInfoHelp()
		os.Exit(1)
	}

	info, err := internal.GetStepInfo(db, stepID)
	if err != nil {
		fmt.Printf("Error getting step info: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Step #%d: %s\n", info.ID, info.Title)
	fmt.Printf("Task ID: %d\n", info.TaskID)
	fmt.Printf("Created: %s\n", info.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated: %s\n", info.UpdatedAt.Format(time.RFC3339))

	fmt.Println("\nSettings:")
	var settingsBuf bytes.Buffer
	encoder := json.NewEncoder(&settingsBuf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("  ", "  ")
	if err := encoder.Encode(info.Settings); err != nil {
		fmt.Printf("Error formatting settings: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(settingsBuf.String())

	if len(info.Results) > 0 {
		fmt.Println("\nResults:")
		var resultsBuf bytes.Buffer
		encoder := json.NewEncoder(&resultsBuf)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("  ", "  ")
		if err := encoder.Encode(info.Results); err != nil {
			fmt.Printf("Error formatting results: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(resultsBuf.String())
	}
}
