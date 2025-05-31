package main

import (
	"fmt"
	"os"

	"github.com/yourusername/task-sync/internal"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: task-sync migrate [up|down]")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "migrate":
		if len(os.Args) < 3 {
			fmt.Println("Usage: task-sync migrate [up|down|status|reset]")
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
		fmt.Println("Unknown command")
		os.Exit(1)
	}
}
