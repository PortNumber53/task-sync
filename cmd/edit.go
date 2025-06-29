package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/PortNumber53/task-sync/internal"
	helpPkg "github.com/PortNumber53/task-sync/help"
)

func HandleStepEdit(db *sql.DB) {
	if len(os.Args) < 4 {
		helpPkg.PrintStepEditHelp()
		os.Exit(1)
	}

	stepID, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Printf("Error: invalid step ID '%s'\n", os.Args[3])
		helpPkg.PrintStepEditHelp()
		os.Exit(1)
	}

	_, getStepErr := internal.GetStepInfo(db, stepID)
	if getStepErr != nil {
		fmt.Printf("Error preparing to edit step %d: %v\n", stepID, getStepErr)
		os.Exit(1)
	}

	sets := make(map[string]string)
	var removeKey string
	for i := 4; i < len(os.Args); i++ {
		if os.Args[i] == "--set" {
			if i+1 >= len(os.Args) {
				fmt.Println("Error: --set requires a key=value argument")
				helpPkg.PrintStepEditHelp()
				os.Exit(1)
			}
			kv := strings.SplitN(os.Args[i+1], "=", 2)
			if len(kv) != 2 {
				fmt.Printf("Error: invalid format for --set, expected key=value, got '%s'\n", os.Args[i+1])
				helpPkg.PrintStepEditHelp()
				os.Exit(1)
			}
			sets[kv[0]] = kv[1]
			i++
		} else if os.Args[i] == "--remove-key" {
			if i+1 >= len(os.Args) {
				fmt.Println("Error: --remove-key requires a key argument")
				helpPkg.PrintStepEditHelp()
				os.Exit(1)
			}
			removeKey = os.Args[i+1]
			i++
		}
	}

	if len(sets) > 0 && removeKey != "" {
		fmt.Println("Error: --set and --remove-key are mutually exclusive")
		helpPkg.PrintStepEditHelp()
		os.Exit(1)
	}

	if len(sets) == 0 && removeKey == "" {
		fmt.Println("Error: either --set or --remove-key must be provided")
		helpPkg.PrintStepEditHelp()
		os.Exit(1)
	}

	if removeKey != "" {
		if err := internal.RemoveStepSettingKey(db, stepID, removeKey); err != nil {
			fmt.Printf("Error removing key '%s' for step %d: %v\n", removeKey, stepID, err)
			os.Exit(1)
		}
		fmt.Printf("Successfully removed key '%s' from step %d\n", removeKey, stepID)
		return
	}

	var updateErrors []string
	for key, value := range sets {
		err := internal.UpdateStepFieldOrSetting(db, stepID, key, value)
		if err != nil {
			updateErrors = append(updateErrors, fmt.Sprintf("failed to set '%s': %v", key, err))
		}
	}

	if len(updateErrors) > 0 {
		fmt.Printf("Error updating step %d:\n", stepID)
		for _, errMsg := range updateErrors {
			fmt.Printf("  - %s\n", errMsg)
		}
		os.Exit(1)
	}

	if len(sets) > 0 {
		fmt.Printf("Step %d updated successfully.\n", stepID)
	}
}
