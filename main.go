package main

import (
	"fmt"
	"os"

	"github.com/yourusername/task-sync/internal"
)

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
		if len(os.Args) < 3 || os.Args[2] != "list" {
			fmt.Println("Usage: task-sync tasks list")
			os.Exit(1)
		}
		if err := internal.ListTasks(); err != nil {
			fmt.Printf("List tasks error: %v\n", err)
			os.Exit(1)
		}
		return
	case "steps":
		if len(os.Args) < 3 || os.Args[2] != "list" {
			fmt.Println("Usage: task-sync steps list [--full]")
			os.Exit(1)
		}
		full := false
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--full" {
				full = true
			}
		}
		if err := internal.ListSteps(full); err != nil {
			fmt.Printf("List steps error: %v\n", err)
			os.Exit(1)
		}
		return

	case "step":
		if len(os.Args) < 3 || os.Args[2] != "create" {
			fmt.Println("Usage: task-sync step create --task <id|name> --title <title> --settings <json>")
			os.Exit(1)
		}
		var taskRef, title, settings string
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--task" && i+1 < len(os.Args) {
				taskRef = os.Args[i+1]
				i++
			} else if os.Args[i] == "--title" && i+1 < len(os.Args) {
				title = os.Args[i+1]
				i++
			} else if os.Args[i] == "--settings" && i+1 < len(os.Args) {
				settings = os.Args[i+1]
				i++
			}
		}
		if taskRef == "" || title == "" || settings == "" {
			fmt.Println("--task, --title, and --settings are required.")
			os.Exit(1)
		}
		if err := internal.CreateStep(taskRef, title, settings); err != nil {
			fmt.Printf("Step creation error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Step created successfully.")
		return

	case "task":
		if len(os.Args) < 3 || os.Args[2] != "create" {
			fmt.Println("Usage: task-sync task create --name <name> --status <status>")
			os.Exit(1)
		}
		var name, status string
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "--name" && i+1 < len(os.Args) {
				name = os.Args[i+1]
				i++
			} else if os.Args[i] == "--status" && i+1 < len(os.Args) {
				status = os.Args[i+1]
				i++
			}
		}
		if name == "" || status == "" {
			fmt.Println("Both --name and --status are required.")
			os.Exit(1)
		}
		if err := internal.CreateTask(name, status); err != nil {
			fmt.Printf("Task creation error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Task created successfully.")
		return

	case "migrate":
		if len(os.Args) < 3 {
			fmt.Println("Usage:")
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
