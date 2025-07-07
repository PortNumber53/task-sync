package main

import (
	"fmt"
	"log"
	"os"
	"net/http"
	"context"
	"time"

	cmd "github.com/PortNumber53/task-sync/cmd"
	help "github.com/PortNumber53/task-sync/help"
	"github.com/PortNumber53/task-sync/internal"
	"github.com/PortNumber53/task-sync/pkg/models"
)

func main() {
    // Initialize StepLogger at the start to avoid nil pointer dereference
    models.StepLogger = log.New(os.Stdout, "STEP: ", log.Ldate|log.Ltime|log.Lshortfile)

	// Check for --help or -h as the first argument
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		help.PrintMainHelp()
		os.Exit(0)
	}

	if len(os.Args) < 2 {
		// Default to running the API server in local mode if no arguments are provided
		listenAddr := "127.0.0.1:8064"
		pgURL, err := internal.GetPgURLFromEnv()
		if err != nil {
			fmt.Printf("Database configuration error: %v\n", err)
			os.Exit(1)
		}
		db, err := models.OpenDB(pgURL)
		if err != nil {
			fmt.Printf("Database connection error: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		// Start API server and step executor in parallel
		srv, quit := internal.NewAPIServer(listenAddr, db)
		_, cancel := internal.RunStepExecutor(db)

		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("listen: %s\n", err)
			}
		}()

		// Wait for interrupt signal to gracefully shut down the server
		<-quit
		log.Println("Shutting down server...")
		cancel()
		time.Sleep(1 * time.Second)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatal("Server forced to shutdown:", err)
		}
		log.Println("Server exiting")
		return
	}

	switch os.Args[1] {
	case "run-steps":
		cmd.HandleRunSteps()
	case "step":
		cmd.HandleStep()
	case "task":
		if len(os.Args) > 2 && os.Args[2] == "report" {
			pgURL, err := internal.GetPgURLFromEnv()
			if err != nil {
				fmt.Printf("Database configuration error: %v\n", err)
				os.Exit(1)
			}
			db, err := models.OpenDB(pgURL)
			if err != nil {
				fmt.Printf("Database connection error: %v\n", err)
				os.Exit(1)
			}
			defer db.Close()
			cmd.HandleReport(db)
			return
		}
		cmd.HandleTask()
	case "migrate":
		cmd.HandleMigrate()
	default:
		fmt.Printf("Unknown command: %s\n", os.Args[1])
		help.PrintMainHelp()
		os.Exit(1)
	}
}
