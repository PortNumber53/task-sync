package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	helpPkg "github.com/yourusername/task-sync/help"
	"github.com/yourusername/task-sync/internal"
)

func main() {
	// Check for --help or -h as the first argument
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		helpPkg.PrintMainHelp()
		os.Exit(0)
	}

	if len(os.Args) < 2 {
		// Default to running the API server in remote mode if no arguments are provided
		if err := internal.RunAPIServer("127.0.0.1:8064"); err != nil {
			fmt.Printf("API server error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	switch os.Args[1] {
	case "run-steps":
		// Initialize the logger for step execution
		internal.InitStepLogger(os.Stdout)

		pgURL, err := internal.GetPgURLFromEnv()
		if err != nil {
			fmt.Printf("Database configuration error: %v\n", err)
			os.Exit(1)
		}
		db, err := sql.Open("postgres", pgURL)
		if err != nil {
			fmt.Printf("Database connection error: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		fmt.Println("Starting step processing...")
		if err := internal.ProcessSteps(db); err != nil {
			fmt.Printf("Error processing steps: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Step processing finished.")
		return

	case "serve":
		// Check for help flag
		help := false
		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--help" || os.Args[i] == "-h" {
				help = true
				break
			}
		}

		if help {
			helpPkg.PrintServeHelp()
			os.Exit(0)
		}

		listenAddr := "127.0.0.1:8064"
		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--remote" {
				listenAddr = "0.0.0.0:8064"
			}
		}

		if err := internal.RunAPIServer(listenAddr); err != nil {
			fmt.Printf("API server error: %v\n", err)
			os.Exit(1)
		}
		return
	case "step":
		// Check for help flag
		if len(os.Args) >= 3 && (os.Args[2] == "--help" || os.Args[2] == "-h") {
			helpPkg.PrintStepHelp()
			os.Exit(0)
		}

		// Check if we have a subcommand
		if len(os.Args) < 3 {
			helpPkg.PrintStepHelp()
			os.Exit(1)
		}

		subcommand := os.Args[2]

		// Open DB connection for all step subcommands that need it
		pgURL, err := internal.GetPgURLFromEnv()
		if err != nil {
			fmt.Printf("Database configuration error: %v\n", err)
			os.Exit(1)
		}
		db, err := sql.Open("postgres", pgURL)
		if err != nil {
			fmt.Printf("Database connection error: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		// step activate <id>
		if subcommand == "activate" {
			if len(os.Args) < 4 || os.Args[3] == "--help" || os.Args[3] == "-h" {
				helpPkg.PrintStepActivateHelp()
				os.Exit(0)
			}
			stepID, err := strconv.Atoi(os.Args[3])
			if err != nil {
				fmt.Printf("Invalid step ID: %v\n", os.Args[3])
				os.Exit(1)
			}
			err = internal.ActivateStep(db, stepID)
			if err != nil {
				fmt.Printf("Failed to activate step: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Step %d activated.\n", stepID)
			os.Exit(0)
		}

		// Handle help flag for subcommands
		showHelp := false
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--help" || os.Args[i] == "-h" {
				showHelp = true
				break
			}
		}

		if showHelp {
			switch subcommand {
			case "create":
				helpPkg.PrintStepCreateHelp()
			case "copy":
				helpPkg.PrintStepCopyHelp()
			case "list":
				helpPkg.PrintStepsListHelp()
			case "edit":
				helpPkg.PrintStepEditHelp()
			default:
				fmt.Printf("Unknown subcommand: %s\n", subcommand)
			}
			os.Exit(0)
		}

		switch os.Args[2] {
		case "tree":
			if err := internal.TreeSteps(db); err != nil {
				fmt.Printf("Error displaying step tree: %v\n", err)
				os.Exit(1)
			}
			return
		case "list":
			full := false
			help := false
			for i := 3; i < len(os.Args); i++ {
				switch os.Args[i] {
				case "--full":
					full = true
				case "--help", "-h":
					help = true
				}
			}
			if help {
				helpPkg.PrintStepsListHelp()
				os.Exit(0)
			}
			if err := internal.ListSteps(db, full); err != nil {
				fmt.Printf("List steps error: %v\n", err)
				os.Exit(1)
			}
			return

		case "create":
			// Handle step create command
			var taskRef, title, settings string
			for i := 3; i < len(os.Args); i++ {
				switch os.Args[i] {
				case "--task":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --task requires a value")
						helpPkg.PrintStepCreateHelp()
						os.Exit(1)
					}
					taskRef = os.Args[i+1]
					i++
				case "--title":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --title requires a value")
						helpPkg.PrintStepCreateHelp()
						os.Exit(1)
					}
					title = os.Args[i+1]
					i++
				case "--settings":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --settings requires a value")
						helpPkg.PrintStepCreateHelp()
						os.Exit(1)
					}
					settings = os.Args[i+1]
					i++
				}
			}

			// Validate required arguments
			if taskRef == "" || title == "" || settings == "" {
				fmt.Println("Error: --task, --title, and --settings are required")
				helpPkg.PrintStepCreateHelp()
				os.Exit(1)
			}

			// Create the step
			newStepID, err := internal.CreateStep(db, taskRef, title, settings)
			if err != nil {
				fmt.Printf("Failed to create step: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Step created successfully with ID: %d\n", newStepID)
			return

		case "edit":
			// Handle step edit command
			if len(os.Args) < 4 {
				helpPkg.PrintStepEditHelp()
				os.Exit(1)
			}

			// Parse the step ID
			stepID, err := strconv.Atoi(os.Args[3])
			if err != nil {
				fmt.Printf("Error: invalid step ID '%s'\n", os.Args[3])
				helpPkg.PrintStepEditHelp()
				os.Exit(1)
			}

			// Check if step exists before attempting to edit
			_, getStepErr := internal.GetStepInfo(db, stepID) // db is from the outer scope for step subcommands
			if getStepErr != nil {
				if strings.Contains(getStepErr.Error(), fmt.Sprintf("no step found with ID %d", stepID)) {
					fmt.Printf("Error: no step found with ID %d\n", stepID)
				} else {
					// For other errors from GetStepInfo, print a generic message
					fmt.Printf("Error preparing to edit step %d: %v\n", stepID, getStepErr)
				}
				os.Exit(1)
			}

			// Parse --set flags and --remove-key flag
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

			// Connect to the database
			pgURL, err := internal.GetPgURLFromEnv()
			if err != nil {
				fmt.Printf("Database configuration error: %v\n", err)
				os.Exit(1)
			}
			db, err := sql.Open("postgres", pgURL)
			if err != nil {
				fmt.Printf("Database connection error: %v\n", err)
				os.Exit(1)
			}
			defer db.Close()

			// Handle --remove-key if provided
			if removeKey != "" {
				if err := internal.RemoveStepSettingKey(db, stepID, removeKey); err != nil {
					fmt.Printf("Error removing key '%s' for step %d: %v\n", removeKey, stepID, err)
					os.Exit(1)
				}
				fmt.Printf("Successfully removed key '%s' from step %d\n", removeKey, stepID)
				os.Exit(0)
			}

			// Apply each set operation using UpdateStepFieldOrSetting
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

			if len(sets) > 0 { // Only print success if --set operations were performed
				fmt.Printf("Step %d updated successfully.\n", stepID)
			}
			return

		case "info":
			// Handle step info command
			if len(os.Args) < 4 {
				helpPkg.PrintStepInfoHelp()
				os.Exit(1)
			}

			// Parse the step ID
			stepID, err := strconv.Atoi(os.Args[3])
			if err != nil {
				fmt.Printf("Error: invalid step ID '%s'\n", os.Args[3])
				helpPkg.PrintStepInfoHelp()
				os.Exit(1)
			}

			// Get step info
			info, err := internal.GetStepInfo(db, stepID)
			if err != nil {
				fmt.Printf("Error getting step info: %v\n", err)
				os.Exit(1)
			}

			// Format and print the step info
			fmt.Printf("Step #%d: %s\n", info.ID, info.Title)
			fmt.Printf("Task ID: %d\n", info.TaskID)
			fmt.Printf("Status: %s\n", info.Status)
			fmt.Printf("Created: %s\n", info.CreatedAt.Format(time.RFC3339))
			fmt.Printf("Updated: %s\n", info.UpdatedAt.Format(time.RFC3339))

			// Print settings
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

			// Print results if available
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

			return

		case "copy":
			// Handle step copy command
			var stepID, toTaskID int
			var err error

			// Parse command line arguments
			for i := 3; i < len(os.Args); i++ {
				switch os.Args[i] {
				case "--id":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --id requires a value")
						helpPkg.PrintStepCopyHelp()
						os.Exit(1)
					}
					stepID, err = strconv.Atoi(os.Args[i+1])
					if err != nil {
						fmt.Printf("Error: invalid step ID '%s'\n", os.Args[i+1])
						helpPkg.PrintStepCopyHelp()
						os.Exit(1)
					}
					i++
				case "--to-task-id":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --to-task-id requires a value")
						helpPkg.PrintStepCopyHelp()
						os.Exit(1)
					}
					toTaskID, err = strconv.Atoi(os.Args[i+1])
					if err != nil {
						fmt.Printf("Error: invalid task ID '%s'\n", os.Args[i+1])
						helpPkg.PrintStepCopyHelp()
						os.Exit(1)
					}
					i++
				}
			}

			// Validate required arguments
			if stepID <= 0 || toTaskID <= 0 {
				fmt.Println("Error: --id and --to-task-id are required and must be positive integers")
				helpPkg.PrintStepCopyHelp()
				os.Exit(1)
			}

			pgURL, err := internal.GetPgURLFromEnv()
			if err != nil {
				fmt.Printf("Error getting DB config: %v\n", err)
				os.Exit(1)
			}
			db, err := sql.Open("postgres", pgURL)
			if err != nil {
				fmt.Printf("Error opening DB: %v\n", err)
				os.Exit(1)
			}
			defer db.Close()

			// Copy the step
			if err := internal.CopyStep(db, stepID, toTaskID); err != nil {
				fmt.Printf("Error copying step: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("Step with ID %d has been copied to task %d\n", stepID, toTaskID)
			return
		}

	case "task":
		if len(os.Args) < 3 {
			fmt.Println("Available subcommands:")
			fmt.Println("  create - Create a new task")
			fmt.Println("  delete - Delete a task and its steps")
			fmt.Println("  edit   - Edit an existing task's details")
			fmt.Println("  list   - List all tasks")
			os.Exit(1)
		}

		// Check for help flag for task subcommands that don't have their own specific flag parsing yet
		if len(os.Args) == 4 && (os.Args[3] == "--help" || os.Args[3] == "-h") {
			switch os.Args[2] {
			case "edit":
				helpPkg.PrintTaskEditHelp()
				os.Exit(0)
				// Add other task subcommands here if they need generic help flag handling
			}
		}

		switch os.Args[2] {
		case "delete":
			var taskID int
			help := false

			// Parse command line arguments
			for i := 3; i < len(os.Args); i++ {
				switch os.Args[i] {
				case "--id":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --id requires a value")
						helpPkg.PrintTaskDeleteHelp()
						os.Exit(1)
					}
					var err error
					taskID, err = strconv.Atoi(os.Args[i+1])
					if err != nil {
						fmt.Printf("Error: invalid task ID '%s'\n", os.Args[i+1])
						helpPkg.PrintTaskDeleteHelp()
						os.Exit(1)
					}
					i++
				case "-h", "--help":
					help = true
				}
			}

			// Show help if requested
			if help {
				helpPkg.PrintTaskDeleteHelp()
				os.Exit(0)
			}

			// Validate required arguments
			if taskID <= 0 {
				fmt.Println("Error: --id is required and must be a positive integer")
				helpPkg.PrintTaskDeleteHelp()
				os.Exit(1)
			}

			// Delete the task
			if err := internal.DeleteTask(taskID); err != nil {
				fmt.Printf("Error deleting task: %v\n", err)
				os.Exit(1)
			}

			fmt.Printf("Task with ID %d and all its steps have been deleted.\n", taskID)
			return

		case "info":
			var taskID int
			help := false

			// Parse command line arguments
			for i := 3; i < len(os.Args); i++ {
				switch os.Args[i] {
				case "--id":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --id requires a value")
						os.Exit(1)
					}
					var err error
					taskID, err = strconv.Atoi(os.Args[i+1])
					if err != nil {
						fmt.Printf("Error: invalid task ID '%s'\n", os.Args[i+1])
						os.Exit(1)
					}
					i++
				case "--help", "-h":
					help = true
				}
			}

			if help {
				fmt.Println("Usage: task info --id <id>")
				os.Exit(0)
			}

			if taskID <= 0 {
				fmt.Println("Error: --id is required and must be a positive integer")
				os.Exit(1)
			}

			task, err := internal.GetTaskInfo(taskID)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Task Information:")
			fmt.Printf("  ID:        %d\n", task.ID)
			fmt.Printf("  Name:      %s\n", task.Name)
			fmt.Printf("  Status:    %s\n", task.Status)
			if task.LocalPath != nil {
				fmt.Printf("  LocalPath: %s\n", *task.LocalPath)
			} else {
				fmt.Printf("  LocalPath: <none>\n")
			}
			fmt.Printf("  Created:   %s\n", task.CreatedAt)
			fmt.Printf("  Updated:   %s\n", task.UpdatedAt)
			return

		case "create":
			var name, status, localPath string
			help := false

			// Parse command line arguments
			for i := 3; i < len(os.Args); i++ {
				switch os.Args[i] {
				case "--name":
					if i+1 < len(os.Args) {
						name = os.Args[i+1]
						i++
					}
				case "--status":
					if i+1 < len(os.Args) {
						status = strings.ToLower(os.Args[i+1])
						i++
					}
				case "--local-path":
					if i+1 < len(os.Args) {
						localPath = os.Args[i+1]
						i++
					}
				case "--help", "-h":
					help = true
				}
			}

			// Show help if requested
			if help {
				helpPkg.PrintTaskCreateHelp()
				os.Exit(0)
			}

			// Validate required arguments
			if name == "" || status == "" {
				fmt.Println("Error: --name and --status are required")
				helpPkg.PrintTaskCreateHelp()
				os.Exit(1)
			}

			// Create the task with the local path
			if err := internal.CreateTask(name, status, localPath); err != nil {
				fmt.Printf("Error creating task: %v\n", err)
				helpPkg.PrintTaskCreateHelp()
				os.Exit(1)
			}

			// Show success message with local path if provided
			if localPath != "" {
				absPath, _ := filepath.Abs(localPath)
				fmt.Printf("Task '%s' created successfully with status '%s' and local path '%s'.\n", name, status, absPath)
			} else {
				fmt.Printf("Task '%s' created successfully with status '%s'.\n", name, status)
			}
			return

		case "list":
			// Check for help flag
			help := false
			for i := 3; i < len(os.Args); i++ {
				if os.Args[i] == "--help" || os.Args[i] == "-h" {
					help = true
					break
				}
			}

			if help {
				helpPkg.PrintTasksListHelp()
				os.Exit(0)
			}

			if err := internal.ListTasks(); err != nil {
				fmt.Printf("Error listing tasks: %v\n", err)
				os.Exit(1)
			}
			return
		case "edit":
			var taskID int
			updates := make(map[string]string)
			help := false

			// Parse command line arguments
			for i := 3; i < len(os.Args); i++ {
				switch os.Args[i] {
				case "--id":
					if i+1 < len(os.Args) {
						var err error
						taskID, err = strconv.Atoi(os.Args[i+1])
						if err != nil {
							fmt.Printf("Error: invalid task ID '%s'\n", os.Args[i+1])
							helpPkg.PrintTaskEditHelp()
							os.Exit(1)
						}
						i++
					} else {
						fmt.Println("Error: --id requires a value")
						helpPkg.PrintTaskEditHelp()
						os.Exit(1)
					}
				case "--set":
					if i+1 < len(os.Args) {
						parts := strings.SplitN(os.Args[i+1], "=", 2)
						if len(parts) != 2 {
							fmt.Printf("Error: invalid format for --set '%s'. Expected KEY=\"value\"\n", os.Args[i+1])
							helpPkg.PrintTaskEditHelp()
							os.Exit(1)
						}
						key := strings.ToLower(strings.TrimSpace(parts[0]))
						value := strings.TrimSpace(parts[1])
						// Remove surrounding quotes from value if present
						if (strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) || (strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
							value = value[1 : len(value)-1]
						}
						updates[key] = value
						i++
					} else {
						fmt.Println("Error: --set requires a value in KEY=\"value\" format")
						helpPkg.PrintTaskEditHelp()
						os.Exit(1)
					}
				case "--help", "-h":
					help = true
				}
			}

			if help {
				helpPkg.PrintTaskEditHelp()
				os.Exit(0)
			}

			if taskID <= 0 {
				fmt.Println("Error: --id is required and must be a positive integer")
				helpPkg.PrintTaskEditHelp()
				os.Exit(1)
			}
			if len(updates) == 0 {
				fmt.Println("Error: at least one --set KEY=\"value\" is required")
				helpPkg.PrintTaskEditHelp()
				os.Exit(1)
			}

			pgURL, err := internal.GetPgURLFromEnv()
			if err != nil {
				fmt.Printf("Database configuration error: %v\n", err)
				os.Exit(1)
			}
			db, err := sql.Open("postgres", pgURL)
			if err != nil {
				fmt.Printf("Database connection error: %v\n", err)
				os.Exit(1)
			}
			defer db.Close()

			if err := internal.EditTask(db, taskID, updates); err != nil {
				fmt.Printf("Error editing task: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Task %d updated successfully.\n", taskID)
			return

		default:
			fmt.Println("Unknown subcommand:", os.Args[2])
			fmt.Println("Available subcommands:")
			fmt.Println("  create - Create a new task")
			fmt.Println("  delete - Delete a task and its steps")
			fmt.Println("  list   - List all tasks")
			os.Exit(1)
		}

	case "migrate":
		// Check for help flag
		if len(os.Args) >= 3 && (os.Args[2] == "--help" || os.Args[2] == "-h") {
			helpPkg.PrintMigrateHelp()
			os.Exit(0)
		}

		if len(os.Args) < 3 {
			helpPkg.PrintMigrateHelp()
			os.Exit(1)
		}
		if os.Args[2] == "status" {
			if err := internal.RunMigrateStatus(); err != nil {
				fmt.Printf("Status error: %v\n", err)
				os.Exit(1)
			}
		} else if os.Args[2] == "reset" {
			if err := internal.RunMigrateReset(); err != nil {
				fmt.Printf("Reset error: %v\n", err)
				os.Exit(1)
			}
		} else {
			if err := internal.RunMigrate(os.Args[2]); err != nil {
				fmt.Printf("Migration error: %v\n", err)
				os.Exit(1)
			}
		}
	default:
		valid := map[string]bool{"migrate": true, "task": true, "step": true, "serve": true}
		if !valid[os.Args[1]] {
			fmt.Println("Unknown command:", os.Args[1])
			fmt.Println("Commands:")
			fmt.Println("  task-sync migrate [up|down|status|reset]")
			fmt.Println("  task-sync task create --name <name> --status <status>")
			fmt.Println("  task-sync task list")
			fmt.Println("  task-sync step create --task <id|name> --title <title> --settings <json>")
			fmt.Println("  task-sync step list [--full]")
			fmt.Println("  task-sync serve [--remote]")
			fmt.Println()
			fmt.Println("  --remote: Listen on all interfaces (not just localhost) for API server")
			os.Exit(1)
		}
	}
}
