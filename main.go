package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yourusername/task-sync/internal"
)

func printStepsListHelp() {
	helpText := `List all steps in the task system.

Usage:
  task-sync steps list [flags]

Flags:
  --full    Show additional details including step settings
  -h, --help  Show this help message and exit

Examples:
  # List all steps
  task-sync steps list

  # Show all steps with full details
  task-sync steps list --full`
	fmt.Println(helpText)
}

func printTaskDeleteHelp() {
	helpText := `Delete a task and all its associated steps.

Usage:
  task-sync task delete --id TASK_ID

Required Flags:
  --id int  ID of the task to delete
  -h, --help  Show this help message and exit

Examples:
  # Delete task with ID 1
  task-sync task delete --id 1

  # Show this help message
  task-sync task delete --help`
	fmt.Println(helpText)
}

func printTaskCreateHelp() {
	helpText := `Create a new task in the system.

Usage:
  task-sync task create --name NAME --status STATUS [--local-path PATH]

Required Flags:
  --name string    Name of the task (must be unique)
  --status string  Status of the task (must be one of: active, inactive, disabled, running)

Optional Flags:
  --local-path string  Local filesystem path associated with the task
  -h, --help          Show this help message and exit

Examples:
  # Create a new active task
  task-sync task create --name "deploy-api" --status active

  # Create a task with a local path
  task-sync task create --name "frontend" --status inactive --local-path "/projects/frontend"

  # Show this help message
  task-sync task create --help`
	fmt.Println(helpText)
}

func printStepCopyHelp() {
	helpText := `Copy a step to a different task.

Usage:
  task-sync step copy --id STEP_ID --to-task-id TASK_ID

Required Flags:
  --id int         ID of the step to copy
  --to-task-id int ID of the target task

Options:
  -h, --help    Show this help message and exit

Examples:
  # Copy step with ID 5 to task with ID 3
  task-sync step copy --id 5 --to-task-id 3

  # Show this help message
  task-sync step copy --help`
	fmt.Println(helpText)
}

func printStepCreateHelp() {
	helpText := `Create a new step for a task.

Usage:
  task-sync step create --task TASK_REF --title TITLE --settings JSON

Required Flags:
  --task string    Task ID or name to attach the step to
  --title string   Title of the step
  --settings JSON  JSON string containing step settings

Options:
  -h, --help  Show this help message and exit

Examples:
  # Create a step for task with ID 1
  task-sync step create --task 1 --title "Build" --settings '{"command":"npm build"}'

  # Create a step for task by name
  task-sync step create --task "My Task" --title "Test" --settings '{"command":"npm test"}'

  # Show this help message
  task-sync step create --help`
	fmt.Println(helpText)
}

func printTasksListHelp() {
	helpText := `List all tasks in the system.

Usage:
  task-sync tasks list
  task-sync task list

Options:
  -h, --help  Show this help message and exit

Examples:
  # List all tasks
  task-sync tasks list

  # Alternative syntax
  task-sync task list

  # Show this help message
  task-sync tasks list --help`
	fmt.Println(helpText)
}

func printServeHelp() {
	helpText := `Start the task-sync API server.

Usage:
  task-sync serve [--remote]

Options:
  --remote    Listen on all network interfaces (default: localhost only)
  -h, --help  Show this help message and exit

Examples:
  # Start the server on localhost (default)
  task-sync serve

  # Start the server accessible from other machines
  task-sync serve --remote

  # Show this help message
  task-sync serve --help`
	fmt.Println(helpText)
}

func printMigrateHelp() {
	helpText := `Manage database migrations.

Usage:
  task-sync migrate COMMAND [options]

Commands:
  up        Apply all pending migrations
  down      Roll back the most recent migration
  status    Show current migration status
  reset     Reset the database by dropping all tables and re-running all migrations

Options:
  -h, --help    Show this help message and exit

Examples:
  # Apply all pending migrations
  task-sync migrate up

  # Roll back the most recent migration
  task-sync migrate down

  # Show current migration status
  task-sync migrate status

  # Reset the database (drop all tables and re-run migrations)
  task-sync migrate reset`
	fmt.Println(helpText)
}

func main() {
	if len(os.Args) < 2 {
		// Default to running the API server in remote mode if no arguments are provided
		if err := internal.RunAPIServer("0.0.0.0:8064"); err != nil {
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
			printServeHelp()
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
	case "tasks":
		// Check for help flag or invalid subcommand
		if len(os.Args) < 3 || (os.Args[2] != "list" && os.Args[2] != "--help" && os.Args[2] != "-h") {
			printTasksListHelp()
			os.Exit(1)
		}

		// Check for help flag
		help := false
		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "--help" || os.Args[i] == "-h" {
				help = true
				break
			}
		}

		if help {
			printTasksListHelp()
			os.Exit(0)
		}

		if err := internal.ListTasks(); err != nil {
			fmt.Printf("Error listing tasks: %v\n", err)
			os.Exit(1)
		}
		return
	case "steps":
		if len(os.Args) < 3 || os.Args[2] != "list" {
			printStepsListHelp()
			os.Exit(1)
		}
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
			printStepsListHelp()
			os.Exit(0)
		}
		if err := internal.ListSteps(full); err != nil {
			fmt.Printf("List steps error: %v\n", err)
			os.Exit(1)
		}
		return

	case "step":
		// Check if we have a subcommand
		if len(os.Args) < 3 {
			fmt.Println("Available subcommands:")
			fmt.Println("  create - Create a new step")
			fmt.Println("  copy   - Copy a step to another task")
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
				printStepCreateHelp()
			case "copy":
				printStepCopyHelp()
			default:
				fmt.Printf("Unknown subcommand: %s\n", subcommand)
			}
			os.Exit(0)
		}

		switch os.Args[2] {
		case "create":
			// Handle step create command
			var taskRef, title, settings string
			for i := 3; i < len(os.Args); i++ {
				switch os.Args[i] {
				case "--task":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --task requires a value")
						printStepCreateHelp()
						os.Exit(1)
					}
					taskRef = os.Args[i+1]
					i++
				case "--title":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --title requires a value")
						printStepCreateHelp()
						os.Exit(1)
					}
					title = os.Args[i+1]
					i++
				case "--settings":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --settings requires a value")
						printStepCreateHelp()
						os.Exit(1)
					}
					settings = os.Args[i+1]
					i++
				}
			}

			// Validate required arguments
			if taskRef == "" || title == "" || settings == "" {
				fmt.Println("Error: --task, --title, and --settings are required\n")
				printStepCreateHelp()
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
						printStepCopyHelp()
						os.Exit(1)
					}
					stepID, err = strconv.Atoi(os.Args[i+1])
					if err != nil {
						fmt.Printf("Error: invalid step ID '%s'\n", os.Args[i+1])
						printStepCopyHelp()
						os.Exit(1)
					}
					i++
				case "--to-task-id":
					if i+1 >= len(os.Args) {
						fmt.Println("Error: --to-task-id requires a value")
						printStepCopyHelp()
						os.Exit(1)
					}
					toTaskID, err = strconv.Atoi(os.Args[i+1])
					if err != nil {
						fmt.Printf("Error: invalid task ID '%s'\n", os.Args[i+1])
						printStepCopyHelp()
						os.Exit(1)
					}
					i++
				}
			}

			// Validate required arguments
			if stepID <= 0 || toTaskID <= 0 {
				fmt.Println("Error: --id and --to-task-id are required and must be positive integers\n")
				printStepCopyHelp()
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
						printTaskDeleteHelp()
						os.Exit(1)
					}
					var err error
					taskID, err = strconv.Atoi(os.Args[i+1])
					if err != nil {
						fmt.Printf("Error: invalid task ID '%s'\n", os.Args[i+1])
						printTaskDeleteHelp()
						os.Exit(1)
					}
					i++
				case "-h", "--help":
					help = true
				}
			}

			// Show help if requested
			if help {
				printTaskDeleteHelp()
				os.Exit(0)
			}

			// Validate required arguments
			if taskID <= 0 {
				fmt.Println("Error: --id is required and must be a positive integer\n")
				printTaskDeleteHelp()
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
				printTaskCreateHelp()
				os.Exit(0)
			}

			// Validate required arguments
			if name == "" || status == "" {
				fmt.Println("Error: --name and --status are required\n")
				printTaskCreateHelp()
				os.Exit(1)
			}

			// Create the task with the local path
			if err := internal.CreateTask(name, status, localPath); err != nil {
				fmt.Printf("Error creating task: %v\n\n", err)
				printTaskCreateHelp()
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
				printTasksListHelp()
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
			printMigrateHelp()
			os.Exit(0)
		}

		if len(os.Args) < 3 {
			printMigrateHelp()
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
		valid := map[string]bool{"migrate": true, "task": true, "tasks": true, "step": true, "steps": true, "serve": true}
		if !valid[os.Args[1]] {
			fmt.Println("Unknown command:", os.Args[1])
			fmt.Println("Commands:")
			fmt.Println("  task-sync migrate [up|down|status|reset]")
			fmt.Println("  task-sync task create --name <name> --status <status>")
			fmt.Println("  task-sync tasks list")
			fmt.Println("  task-sync step create --task <id|name> --title <title> --settings <json>")
			fmt.Println("  task-sync steps list [--full]")
			fmt.Println("  task-sync serve [--remote]")
			fmt.Println()
			fmt.Println("  --remote: Listen on all interfaces (not just localhost) for API server")
			os.Exit(1)
		}
	}
}
