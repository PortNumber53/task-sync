package cmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	helpPkg "github.com/PortNumber53/task-sync/help"
	"github.com/PortNumber53/task-sync/internal"
)

func HandleRunSteps() {
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
}

func HandleServe() {
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
}

func HandleStep() {
	if len(os.Args) < 3 {
		helpPkg.PrintStepHelp()
		os.Exit(1)
	}

	subcommand := os.Args[2]

	showHelp := false
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--help" || os.Args[i] == "-h" {
			showHelp = true
			break
		}
	}

	if showHelp || subcommand == "--help" || subcommand == "-h" {
		switch subcommand {
		case "create":
			helpPkg.PrintStepCreateHelp()
		case "copy":
			helpPkg.PrintStepCopyHelp()
		case "list":
			helpPkg.PrintStepsListHelp()
		case "edit":
			helpPkg.PrintStepEditHelp()
		case "info":
			helpPkg.PrintStepInfoHelp()
		default:
			helpPkg.PrintStepHelp()
		}
		os.Exit(0)
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

	switch subcommand {
	case "tree":
		if err := internal.TreeSteps(db); err != nil {
			fmt.Printf("Error displaying step tree: %v\n", err)
			os.Exit(1)
		}
	case "delete":
		HandleStepDelete(db)
	case "list":
		HandleStepList(db)
	case "create":
		HandleStepCreate(db)
	case "edit":
		HandleStepEdit(db)
	case "info":
		HandleStepInfo(db)
	case "copy":
		HandleStepCopy(db)
	default:
		fmt.Printf("Unknown step subcommand: %s\n", subcommand)
		helpPkg.PrintStepHelp()
		os.Exit(1)
	}
}

func HandleStepDelete(db *sql.DB) {
	if len(os.Args) < 4 {
		fmt.Println("Error: delete subcommand requires a step ID.")
		helpPkg.PrintStepHelp()
		os.Exit(1)
	}
	stepID, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Printf("Error: invalid step ID '%s'. Must be an integer.\n", os.Args[3])
		os.Exit(1)
	}

	if err := internal.DeleteStep(db, stepID); err != nil {
		fmt.Printf("Error deleting step: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Step %d deleted successfully.\n", stepID)
}

func HandleStepList(db *sql.DB) {
	full := false
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--full" {
			full = true
		}
	}
	if err := internal.ListSteps(db, full); err != nil {
		fmt.Printf("List steps error: %v\n", err)
		os.Exit(1)
	}
}

func HandleStepCreate(db *sql.DB) {
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

	if taskRef == "" || title == "" || settings == "" {
		fmt.Println("Error: --task, --title, and --settings are required")
		helpPkg.PrintStepCreateHelp()
		os.Exit(1)
	}

	newStepID, err := internal.CreateStep(db, taskRef, title, settings)
	if err != nil {
		fmt.Printf("Failed to create step: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Step created successfully with ID: %d\n", newStepID)
}

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

	if removeKey != "" {
		if err := internal.RemoveStepSettingKey(db, stepID, removeKey); err != nil {
			fmt.Printf("Error removing key '%s' for step %d: %v\n", removeKey, stepID, err)
			os.Exit(1)
		}
		fmt.Printf("Successfully removed key '%s' from step %d\n", removeKey, stepID)
		return
	}

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

	if len(sets) > 0 {
		fmt.Printf("Step %d updated successfully.\n", stepID)
	}
}

func HandleStepInfo(db *sql.DB) {
	if len(os.Args) < 4 {
		helpPkg.PrintStepInfoHelp()
		os.Exit(1)
	}

	stepID, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Printf("Error: invalid step ID '%s'\n", os.Args[3])
		helpPkg.PrintStepInfoHelp()
		os.Exit(1)
	}

	info, err := internal.GetStepInfo(db, stepID)
	if err != nil {
		fmt.Printf("Error getting step info: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Step #%d: %s\n", info.ID, info.Title)
	fmt.Printf("Task ID: %d\n", info.TaskID)
	fmt.Printf("Created: %s\n", info.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated: %s\n", info.UpdatedAt.Format(time.RFC3339))

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
}

func HandleStepCopy(db *sql.DB) {
	var stepID, toTaskID int
	var err error

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

	if stepID <= 0 || toTaskID <= 0 {
		fmt.Println("Error: --id and --to-task-id are required and must be positive integers")
		helpPkg.PrintStepCopyHelp()
		os.Exit(1)
	}

	newStepID, err := internal.CopyStep(db, stepID, toTaskID)
	if err != nil {
		fmt.Printf("Error copying step: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Step %d copied to task %d as new step %d.\n", stepID, toTaskID, newStepID)
}

func HandleTask() {
	if len(os.Args) < 3 {
		helpPkg.PrintTaskHelp()
		os.Exit(1)
	}

	subcommand := os.Args[2]

	showHelp := false
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--help" || os.Args[i] == "-h" {
			showHelp = true
			break
		}
	}

	if showHelp || subcommand == "--help" || subcommand == "-h" {
		// Specific help for task subcommands can be added here
		helpPkg.PrintTaskHelp()
		os.Exit(0)
	}

	switch subcommand {
	case "delete":
		HandleTaskDelete()
	case "info":
		HandleTaskInfo()
	case "create":
		HandleTaskCreate()
	case "edit":
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
		HandleTaskEdit(db)
	case "list":
		HandleTaskList()
	default:
		fmt.Printf("Unknown task subcommand: %s\n", subcommand)
		helpPkg.PrintTaskHelp()
		os.Exit(1)
	}
}

func HandleTaskDelete() {
	if len(os.Args) < 4 {
		fmt.Println("Error: delete subcommand requires a task ID.")
		helpPkg.PrintTaskHelp()
		os.Exit(1)
	}
	taskID, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Printf("Error: invalid task ID '%s'. Must be an integer.\n", os.Args[3])
		os.Exit(1)
	}

	if err := internal.DeleteTask(taskID); err != nil {
		fmt.Printf("Error deleting task: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Task %d deleted successfully.\n", taskID)
}

func HandleTaskInfo() {
	if len(os.Args) < 4 {
		fmt.Println("Error: info subcommand requires a task ID.")
		helpPkg.PrintTaskHelp()
		os.Exit(1)
	}
	taskID, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Printf("Error: invalid task ID '%s'. Must be an integer.\n", os.Args[3])
		os.Exit(1)
	}

	info, err := internal.GetTaskInfo(taskID)
	if err != nil {
		fmt.Printf("Error getting task info: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ID: %d\nName: %s\nStatus: %s\n", info.ID, info.Name, info.Status)
	if info.LocalPath != nil {
		fmt.Printf("Local Path: %s\n", *info.LocalPath)
	}
	fmt.Printf("Created At: %s\nUpdated At: %s\n", info.CreatedAt, info.UpdatedAt)
}

func HandleTaskCreate() {
	var name, status, localPath string
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--name":
			if i+1 < len(os.Args) {
				name = os.Args[i+1]
				i++
			} else {
				fmt.Println("Error: --name requires a value")
				helpPkg.PrintTaskCreateHelp()
				os.Exit(1)
			}
		case "--status":
			if i+1 < len(os.Args) {
				status = os.Args[i+1]
				i++
			} else {
				fmt.Println("Error: --status requires a value")
				helpPkg.PrintTaskCreateHelp()
				os.Exit(1)
			}
		case "--local-path":
			if i+1 < len(os.Args) {
				localPath = os.Args[i+1]
				i++
			} else {
				fmt.Println("Error: --local-path requires a value")
				helpPkg.PrintTaskCreateHelp()
				os.Exit(1)
			}
		}
	}

	if name == "" {
		fmt.Println("Error: --name is required.")
		helpPkg.PrintTaskCreateHelp()
		os.Exit(1)
	}
	if status == "" {
		status = "pending" // Default status
	}

	if err := internal.CreateTask(name, status, localPath); err != nil {
		fmt.Printf("Error creating task: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Task created successfully.\n")
}

func HandleTaskEdit(db *sql.DB) {
	if len(os.Args) < 4 {
		helpPkg.PrintTaskEditHelp()
		os.Exit(1)
	}
	taskID, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Printf("Error: invalid task ID '%s'.\n", os.Args[3])
		helpPkg.PrintTaskEditHelp()
		os.Exit(1)
	}

	updates := make(map[string]string)
	for i := 4; i < len(os.Args); i++ {
		if os.Args[i] == "--set" {
			if i+1 >= len(os.Args) {
				fmt.Println("Error: --set requires a key=value argument")
				helpPkg.PrintTaskEditHelp()
				os.Exit(1)
			}
			kv := strings.SplitN(os.Args[i+1], "=", 2)
			if len(kv) != 2 {
				fmt.Printf("Error: invalid format for --set, expected key=value, got '%s'\n", os.Args[i+1])
				helpPkg.PrintTaskEditHelp()
				os.Exit(1)
			}
			updates[kv[0]] = kv[1]
			i++
		}
	}

	if len(updates) == 0 {
		fmt.Println("Error: at least one --set flag is required.")
		helpPkg.PrintTaskEditHelp()
		os.Exit(1)
	}

	if err := internal.EditTask(db, taskID, updates); err != nil {
		fmt.Printf("Error editing task: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Task %d updated successfully.\n", taskID)
}

func HandleTaskList() {
	if err := internal.ListTasks(); err != nil {
		fmt.Printf("Error listing tasks: %v\n", err)
		os.Exit(1)
	}
}

func HandleMigrate() {
	if len(os.Args) < 3 {
		helpPkg.PrintMigrateHelp()
		os.Exit(1)
	}

	subcommand := os.Args[2]

	var err error
	switch subcommand {
	case "up":
		err = internal.RunMigrate("up")
	case "down":
		err = internal.RunMigrate("down")
	case "reset":
		err = internal.RunMigrateReset()
	case "status":
		err = internal.RunMigrateStatus()
	default:
		fmt.Printf("Unknown migrate subcommand: %s\n", subcommand)
		helpPkg.PrintMigrateHelp()
		os.Exit(1)
	}

	if err != nil {
		fmt.Printf("Error running migration command '%s': %v\n", subcommand, err)
		os.Exit(1)
	}
}
