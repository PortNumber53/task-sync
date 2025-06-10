package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
		if err := internal.RunAPIServer("0.0.0.1:8064"); err != nil {
			fmt.Printf("API server error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	switch os.Args[1] {
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
			default:
				fmt.Printf("Unknown subcommand: %s\n", subcommand)
			}
			os.Exit(0)
		}

		switch os.Args[2] {
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
			if err := internal.ListSteps(full); err != nil {
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
			if err := internal.CreateStep(taskRef, title, settings); err != nil {
				fmt.Printf("Error creating step: %v\n", err)
				os.Exit(1)
			}

			fmt.Println("Step created successfully.")
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

			// Copy the step
			if err := internal.CopyStep(stepID, toTaskID); err != nil {
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
			fmt.Println("  list   - List all tasks")
			os.Exit(1)
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
