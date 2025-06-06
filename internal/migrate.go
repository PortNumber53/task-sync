package internal

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

const StepExecutorInterval = 5 * time.Second

var stepLogger *log.Logger

// RunAPIServer starts the Gin server and prints environment/setup info
// (Task and step logic is now in tasks.go and steps.go)
func RunAPIServer(listenAddr string) error {
	// Load config and set up logging
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Println("[Config] Error loading config:", err)
	}
	if cfg != nil && cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Println("[Config] Failed to open log file:", err)
			stepLogger = log.New(os.Stdout, "[StepExecutor] ", log.LstdFlags)
		} else {
			stepLogger = log.New(f, "[StepExecutor] ", log.LstdFlags)
			fmt.Println("[Config] Logging step execution to:", cfg.LogFile)
		}
	} else {
		stepLogger = log.New(os.Stdout, "[StepExecutor] ", log.LstdFlags)
	}

	// Start background step executor
	go func() {
		for {
			executePendingSteps()
			time.Sleep(StepExecutorInterval)
		}
	}()

	mode := "LOCAL"
	if listenAddr == "0.0.0.0:8080" {
		mode = "REMOTE"
	}
	fmt.Println("==============================")
	fmt.Println("Task Sync API Server Starting...")
	fmt.Printf("Listening on:   http://%s\n", listenAddr)
	fmt.Printf("Mode:           %s\n", mode)
	fmt.Println("\nSupported API endpoints:")
	fmt.Println("  GET    /status       - API status/health check")
	fmt.Println("  POST   /tasks        - Create a new task")
	fmt.Println("  GET    /tasks        - List all tasks")
	fmt.Println("  POST   /steps        - Create a new step")
	fmt.Println("  GET    /steps        - List all steps (use ?full=1 for settings)")
	fmt.Println("==============================")
	fmt.Println()

	r := gin.Default()

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{"message": "Welcome to Task Sync"})
	})

	r.GET("/status", func(c *gin.Context) {
		c.JSON(200, gin.H{})
	})

	r.POST("/tasks", func(c *gin.Context) {
		c.JSON(501, gin.H{"message": "Not implemented"})
	})
	r.GET("/tasks", func(c *gin.Context) {
		c.JSON(501, gin.H{"message": "Not implemented"})
	})
	r.POST("/steps", func(c *gin.Context) {
		c.JSON(501, gin.H{"message": "Not implemented"})
	})
	r.GET("/steps", func(c *gin.Context) {
		c.JSON(501, gin.H{"message": "Not implemented"})
	})

	return r.Run(listenAddr)
}

// (Step execution logic moved to steps.go)


// RunMigrate runs DB migrations up or down
func getPgURLFromEnv() (string, error) {
	_ = godotenv.Load()
	host := os.Getenv("DB_HOST")
	port := os.Getenv("DB_PORT")
	user := os.Getenv("DB_USER")
	password := os.Getenv("DB_PASSWORD")
	dbname := os.Getenv("DB_NAME")
	ssl := os.Getenv("DB_SSL")
	if host == "" || port == "" || user == "" || password == "" || dbname == "" {
		return "", fmt.Errorf("missing one or more required DB environment variables")
	}
	sslmode := "disable"
	if ssl == "true" || ssl == "1" {
		sslmode = "require"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s", user, password, host, port, dbname, sslmode), nil
}

func RunMigrate(direction string) error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	m, err := migrate.New(
		"file://./migrations",
		pgURL,
	)
	if err != nil {
		return fmt.Errorf("failed to init migrate: %w", err)
	}
	defer m.Close()

	switch direction {
	case "up":
		err = m.Up()
	case "down":
		err = m.Down()
	default:
		return fmt.Errorf("unknown migration direction: %s", direction)
	}
	if err != nil && err != migrate.ErrNoChange {
		return err
	}
	fmt.Println("Migration", direction, "completed.")
	return nil
}

func RunMigrateReset() error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	m, err := migrate.New(
		"file://./migrations",
		pgURL,
	)
	if err != nil {
		return fmt.Errorf("failed to init migrate: %w", err)
	}
	defer m.Close()

	if err := m.Drop(); err != nil {
		return fmt.Errorf("failed to drop database objects: %w", err)
	}
	fmt.Println("All tables dropped. Re-applying migrations...")
	err = m.Up()
	if err != nil && err.Error() != "no change" {
		if err.Error() == "first .: file does not exist" || err.Error() == "no migration files found" {
			fmt.Println("No migration files found in ./migrations. Please create at least one migration file (e.g., 0001_init.up.sql) before running 'migrate reset'.")
			return nil
		}
		return fmt.Errorf("failed to re-apply migrations: %w", err)
	}
	fmt.Println("Database reset and all migrations reapplied.")
	return nil
}

// RunMigrateStatus prints the status of migrations using golang-migrate's schema_migrations table
func RunMigrateStatus() error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return fmt.Errorf("failed to connect to db: %w", err)
	}
	defer db.Close()

	var version int64
	var dirty bool
	err = db.QueryRow("SELECT version, dirty FROM schema_migrations").Scan(&version, &dirty)
	if err != nil {
		return fmt.Errorf("failed to query schema_migrations: %w", err)
	}
	fmt.Printf("Current migration version: %d\n", version)
	fmt.Printf("Dirty state: %v\n", dirty)
	return nil
}
