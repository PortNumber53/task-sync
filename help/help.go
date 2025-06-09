package help

import "fmt"

// PrintMainHelp prints the main help message for the task-sync CLI
func PrintMainHelp() {
	helpText := `task-sync - A CLI tool for managing tasks and steps

Usage:
  task-sync [command]

Available Commands:
  migrate    Manage database migrations
  task       Manage tasks
  step       Manage steps
  serve      Start the API server
  help       Show this help message

Use "task-sync [command] --help" for more information about a command.
`
	fmt.Println(helpText)
}

// PrintMigrateHelp prints help for the migrate command
func PrintMigrateHelp() {
	helpText := `Manage database migrations.

Usage:
  task-sync migrate [command]

Available Commands:
  up        Apply all up migrations
  down      Apply all down migrations (warning: will drop all tables!)
  status    Show current migration status
  reset     Reset database by applying all down then all up migrations

Examples:
  # Apply all pending migrations
  task-sync migrate up
  
  # Show migration status
  task-sync migrate status
  
  # Reset the database
  task-sync migrate reset`
	fmt.Println(helpText)
}

// PrintTaskCreateHelp prints help for the task create command
func PrintTaskCreateHelp() {
	helpText := `Create a new task.

Usage:
  task-sync task create --name NAME [--status STATUS] [--local-path PATH]

Required Flags:
  --name string    Name of the task

Options:
  --status string     Status of the task (default: "pending")
  --local-path string Local filesystem path for the task
  -h, --help          Show this help message and exit

Examples:
  # Create a new task with default status
  task-sync task create --name "My Task"
  
  # Create a task with a specific status and local path
  task-sync task create --name "Build Project" --status active --local-path "/path/to/project"`
	fmt.Println(helpText)
}

// PrintStepCreateHelp prints help for the step create command
func PrintStepCreateHelp() {
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

// PrintStepCopyHelp prints help for the step copy command
func PrintStepCopyHelp() {
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

// PrintStepHelp prints help for the root step command
func PrintStepHelp() {
	helpText := `Manage steps in the task system.

Usage:
  task-sync step <command> [flags]

Available Commands:
  create    Create a new step
  copy      Copy a step to another task
  list      List all steps
  
Use "task-sync step <command> --help" for more information about a command.
`
	fmt.Println(helpText)
}

// PrintStepsListHelp prints help for the steps list command
func PrintStepsListHelp() {
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

// PrintTaskDeleteHelp prints help for the task delete command
func PrintTaskDeleteHelp() {
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

// PrintTasksListHelp prints help for the tasks list command
func PrintTasksListHelp() {
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

// PrintServeHelp prints help for the serve command
func PrintServeHelp() {
	helpText := `Start the task-sync API server.

Usage:
  task-sync serve [--remote]

Options:
  --remote    Listen on all network interfaces (default: localhost only)
  -h, --help  Show this help message and exit

Examples:
  # Start server on localhost only (default)
  task-sync serve
  
  # Start server on all network interfaces
  task-sync serve --remote`
	fmt.Println(helpText)
}
