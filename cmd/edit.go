package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"

	helpPkg "github.com/PortNumber53/task-sync/help"
	"github.com/PortNumber53/task-sync/internal"
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
	var removeKeys []string
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
			removeKeys = append(removeKeys, os.Args[i+1])
			i++
		}
	}

	if len(sets) > 0 && len(removeKeys) > 0 {
		fmt.Println("Error: --set and --remove-key are mutually exclusive")
		helpPkg.PrintStepEditHelp()
		os.Exit(1)
	}

	if len(sets) == 0 && len(removeKeys) == 0 {
		fmt.Println("Error: either --set or --remove-key must be provided")
		helpPkg.PrintStepEditHelp()
		os.Exit(1)
	}

	if len(removeKeys) > 0 {
		var removeErrors []string
		for _, key := range removeKeys {
			if err := internal.RemoveStepSettingKey(db, stepID, key); err != nil {
				removeErrors = append(removeErrors, fmt.Sprintf("failed to remove key '%s': %v", key, err))
			}
		}

		if len(removeErrors) > 0 {
			fmt.Printf("Error removing keys for step %d:\n", stepID)
			for _, errMsg := range removeErrors {
				fmt.Printf("  - %s\n", errMsg)
			}
			os.Exit(1)
		}
		fmt.Printf("Successfully removed keys from step %d\n", stepID)
		return
	}

	if len(sets) > 0 {
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
		fmt.Printf("Step %d updated successfully.\n", stepID)
	}
}
