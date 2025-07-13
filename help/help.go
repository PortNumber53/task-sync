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
  down      Downgrade (revert) migrations. Use --step for partial, confirmation required.
  status    Show current migration status
  reset     Reset database by applying all down then all up migrations

Examples:
  # Apply all pending migrations
  task-sync migrate up

  # Downgrade the last migration (safe)
  task-sync migrate down --step 1

  # Downgrade all migrations (dangerous!)
  task-sync migrate down

  # Show migration status
  task-sync migrate status

  # Reset the database
  task-sync migrate reset

For details on 'down', run: task-sync migrate down --help`
	fmt.Println(helpText)
}

// PrintTaskCreateHelp prints help for the task create command
func PrintTaskCreateHelp() {
	helpText := `Create a new task.

Usage:
  task-sync task create --name NAME [--status STATUS] [--local_path PATH]

Required Flags:
  --name string    Name of the task

Options:
  --status string     Status of the task (default: "pending")
  --local_path string Local filesystem path for the task
  -h, --help          Show this help message and exit

Examples:
  # Create a new task with default status
  task-sync task create --name "My Task"

  # Create a task with a specific status and local path
  task-sync task create --name "Build Project" --status active --local_path "/path/to/project"`
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

// PrintStepInfoHelp prints help for the step info command
func PrintStepInfoHelp() {
	helpText := `Show detailed information about a specific step.

Usage:
  task-sync step info STEP_ID

Arguments:
  STEP_ID    ID of the step to show information about

Examples:
  # Show information about step with ID 5
  task-sync step info 5

  # Show this help message
  task-sync step info --help`
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

// PrintStepEditHelp prints help for the step edit command
func PrintStepEditHelp() {
	helpText := `Edit a step's settings.

Usage:
  task-sync step edit STEP_ID (--set KEY=VALUE [--set KEY2=VALUE2 ...] | --remove-key KEY)

Arguments:
  STEP_ID    ID of the step to edit

Options:
  --set KEY=VALUE         Set a value in the step's settings using dot notation.
                          This can be used multiple times.
  --remove-key KEY        Remove a top-level key from the step's settings.
                          Cannot be used with --set.
  -h, --help              Show this help message and exit

Examples:
  # Update the image tag for a docker_run step
  task-sync step edit 9 --set docker_run.image_tag='ansible-inventory'

  # Set multiple values at once
  task-sync step edit 9 --set docker_run.image_tag='new-image' --set docker_run.container_name='my-container'

  # Set a nested value
  task-sync step edit 9 --set 'docker_run.environment.DEBUG=true'

  # Set a JSON value (use single quotes around the value)
  task-sync step edit 9 --set docker_run.ports='["8080:80"]'

  # Remove the 'docker_build' key from step 9's settings
  task-sync step edit 9 --remove-key docker_build

  # Show this help message
  task-sync step edit --help`
	fmt.Println(helpText)
}

// PrintStepHelp prints help for the root step command
func PrintStepHelp() {
	helpText := `Manage steps in the task system.

Usage:
  task-sync step <command> [flags]

Available Commands:
  copy       Copy a step to another task
  create     Create a new step
  delete     Delete a step by ID
  edit       Edit a step's settings
  info       Show detailed information about a step
  list       List all steps
  run        Run a specific step by ID

Use "task-sync step <command> --help" for more information about a command.
`
	fmt.Println(helpText)
}

// PrintStepsListHelp prints help for the step list command
func PrintStepsListHelp() {
	helpText := `List all steps in the task system.

Usage:
  task-sync step list [flags]

Flags:
  --full    Show additional details including step settings
  -h, --help  Show this help message and exit

Examples:
  # List all steps
  task-sync step list

  # Show all steps with full details
  task-sync step list --full`
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

// PrintTaskEditHelp prints help for the task edit command
func PrintTaskEditHelp() {
	helpText := `Edit a task's details.

Usage:
  task-sync task edit TASK_ID --set KEY=VALUE [--set KEY2=VALUE2 ...]

Arguments:
  TASK_ID    ID of the task to edit

Options:
  --set KEY=VALUE    Set a field on the task. This can be used multiple times.
  -h, --help         Show this help message and exit

Editable Fields:
  - name:        Task name
  - description: Task description
  - image_tag:   Docker image tag (e.g., 'my_image:latest')
  - image_hash:  Docker image hash (e.g., 'sha256:abc123')
  - status:      Task status (e.g., 'pending', 'active', 'completed')
  - local_path:  Local filesystem path for the task

Examples:
  # Edit the name of task with ID 123
  task-sync task edit 123 --set name="My New Task Name"

  # Update the status and local path for task 45
  task-sync task edit 45 --set status=active --set local_path=/path/to/new/location

  # Show this help message
  task-sync task edit --help`
	fmt.Println(helpText)
}

// PrintTaskHelp prints help for the root task command
func PrintTaskHelp() {
	helpText := `Manage tasks in the task system.

Usage:
  task-sync task <command> [flags]

Available Commands:
  create     Create a new task
  delete     Delete a task
  edit       Edit a task's details
  info       Show detailed information about a task
  list       List all tasks
  run        Run all steps for a specific task

Use "task-sync task <command> --help" for more information about a command.
`
	fmt.Println(helpText)
}

// PrintTaskRunIDHelp prints help for the task run command
func PrintTaskRunIDHelp() {
	fmt.Println("task run command help:")
	fmt.Println("  Usage: task-sync task run <task_id>")
	fmt.Println("  Description: Run all steps for a specific task by providing its ID.")
}

// PrintTasksListHelp prints help for the task list command
func PrintTasksListHelp() {
	helpText := `List all tasks in the system.

Usage:
  task-sync task list

Options:
  -h, --help  Show this help message and exit

Examples:
  # List all tasks
  task-sync task list

  # Show this help message
  task-sync task list --help`
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

// PrintStepRunIDHelp prints help for the step run command
func PrintStepRunIDHelp() {
	fmt.Println("step run command help:")
	fmt.Println("  Usage: task-sync step run <step_id>")
	fmt.Println("  Description: Run a specific step by providing its ID.")
}

// PrintMigrateDownHelp prints help for the migrate down command
func PrintMigrateDownHelp() {
	helpText := `Downgrade (revert) database migrations. By default, this will revert all migrations (dangerous!).

Usage:
  task-sync migrate down [--step COUNT] [--yes]

Options:
  --step COUNT   Revert the specified number of migrations (partial downgrade)
  --yes          Skip the confirmation prompt (for automation)
  -h, --help     Show this help message and exit

Examples:
  # Downgrade the last migration (safe)
  task-sync migrate down --step 1

  # Downgrade the last 3 migrations
  task-sync migrate down --step 3

  # Downgrade all migrations (dangerous!)
  task-sync migrate down

  # Downgrade without confirmation prompt
  task-sync migrate down --step 1 --yes

WARNING:
  Running 'migrate down' without --step will revert ALL migrations and may result in DATA LOSS.
  Always use --step unless you are sure you want to reset the entire database.`
	fmt.Println(helpText)
}
