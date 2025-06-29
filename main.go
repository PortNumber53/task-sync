package main

import (
	"fmt"
	"os"

	cmd "github.com/PortNumber53/task-sync/cmd"
	help "github.com/PortNumber53/task-sync/help"
	"github.com/PortNumber53/task-sync/internal"
)

func main() {
	// Check for --help or -h as the first argument
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		help.PrintMainHelp()
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
		cmd.HandleRunSteps()
	case "serve":
		cmd.HandleServe()
	case "step":
		cmd.HandleStep()
	case "task":
		cmd.HandleTask()
	case "migrate":
		cmd.HandleMigrate()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		help.PrintMainHelp()
		os.Exit(1)
	}
}
