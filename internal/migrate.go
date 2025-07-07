package internal

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"path/filepath"

	"github.com/gin-contrib/cors"
	"github.com/PortNumber53/task-sync/pkg/models"
	"github.com/gin-gonic/gin"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)


const StepExecutorInterval = 5 * time.Second

var stepLogger *log.Logger

// apiErrorLogger is a logger that writes API errors to a file.
var apiErrorLogger *log.Logger

func initAPIErrorLogger() {
	logFilePath := filepath.Join("tmp", "api-errors.log")
	f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Fallback to stdout if file can't be opened
		apiErrorLogger = log.New(os.Stdout, "[API_ERROR] ", log.LstdFlags)
		fmt.Fprintf(os.Stderr, "[API_ERROR] Failed to open log file: %v\n", err)
		return
	}
	apiErrorLogger = log.New(f, "[API_ERROR] ", log.LstdFlags)
}

// godotenvLoad allows mocking of godotenv.Load in tests.
var godotenvLoad = godotenv.Load

// InitStepLogger initializes the package-level step logger.
func InitStepLogger(writer io.Writer) {
	stepLogger = log.New(writer, "[StepExecutor] ", log.LstdFlags)
}

// RunAPIServer starts the Gin server and prints environment/setup info
// (Task and step logic is now in tasks.go and steps.go)
// NewAPIServer creates a Gin HTTP server and returns the http.Server and quit channel for signal handling
func NewAPIServer(listenAddr string, db *sql.DB) (*http.Server, chan os.Signal) {
	initAPIErrorLogger()
	// Load config and set up logging
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Println("[Config] Error loading config:", err)
	}

	if db == nil {
		pgURL, err := GetPgURLFromEnv()
		if err != nil {
			stepLogger.Fatalf("DB config error: %v", err)
		}
		db, err = sql.Open("postgres", pgURL)
		if err != nil {
			stepLogger.Fatalf("DB open error: %v", err)
		}
		// Do not defer db.Close(); keep it open for server lifetime
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	// Initialize the models package logger
	models.InitStepLogger(os.Stdout)

	// Print environment info
	fmt.Println("Starting task-sync API server...")
	fmt.Println("Environment:")
	fmt.Printf("  - Listen address: %s\n", listenAddr)
	fmt.Printf("  - Step check interval: %v\n", StepExecutorInterval)
	fmt.Println("  - Database: PostgreSQL")
	if cfg != nil && cfg.LogFile != "" {
		fmt.Printf("  - Log file: %s\n", cfg.LogFile)
	}
	fmt.Println("==============================")
	fmt.Println("Task Sync API Server Starting...")
	fmt.Printf("Listening on:   http://%s\n", listenAddr)
	mode := "LOCAL"
	if listenAddr == "0.0.0.0:8080" {
		mode = "REMOTE"
	}
	fmt.Printf("Mode:           %s\n", mode)
	fmt.Println("\nSupported API endpoints:")
	fmt.Println("  GET    /status       - API status/health check")
	fmt.Println("  POST   /tasks        - Create a new task")
	fmt.Println("  GET    /tasks        - List all tasks")
	fmt.Println("  POST   /steps        - Create a new step")
	fmt.Println("  GET    /steps        - List all steps (use ?full=1 for settings)")
	fmt.Println("==============================")
	fmt.Println()

	r := gin.New()

	// Register WebSocket API endpoint
	RegisterWebsocketRoutes(r, db)

	// Add CORS middleware for Vite dev server
	r.Use(cors.New(cors.Config{
		AllowOrigins:     []string{"http://localhost:5173"},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Accept"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
	}))

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
		rows, err := db.Query(`SELECT id, name, status, local_path, created_at, updated_at FROM tasks ORDER BY id`)
		if err != nil {
			apiErrorLogger.Printf("/tasks DB query error: %v", err)
			c.JSON(500, gin.H{"error": "Failed to fetch tasks"})
			return
		}
		defer rows.Close()
		tasks := make([]map[string]interface{}, 0)
		for rows.Next() {
			var id int
			var name, status string
			var localPath sql.NullString
			var createdAt, updatedAt string
			if err := rows.Scan(&id, &name, &status, &localPath, &createdAt, &updatedAt); err != nil {
				apiErrorLogger.Printf("/tasks row scan error: %v", err)
				c.JSON(500, gin.H{"error": "Failed to scan task row"})
				return
			}
			tasks = append(tasks, map[string]interface{}{
				"id":     id,
				"name":   name,
				"status": status,
				"local_path": func() string {
					if localPath.Valid {
						return localPath.String
					} else {
						return ""
					}
				}(),
				"created_at": createdAt,
				"updated_at": updatedAt,
			})
		}
		c.JSON(200, gin.H{"tasks": tasks})
	})
	r.POST("/steps", func(c *gin.Context) {
		// Log any errors in the handler
		defer func() {
			if rec := recover(); rec != nil {
				errMsg := fmt.Sprintf("/steps POST panic: %v", rec)
				apiErrorLogger.Println(errMsg)
			}
		}()
		c.JSON(501, gin.H{"message": "Not implemented"})
	})
	r.GET("/steps", func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				errMsg := fmt.Sprintf("/steps GET panic: %v", rec)
				apiErrorLogger.Println(errMsg)
			}
		}()
		c.JSON(501, gin.H{"message": "Not implemented"})
	})

	// Create HTTP server with timeouts
	srv := &http.Server{
		Addr:    listenAddr,
		Handler: r,
	}

	return srv, quit
}

// (Step execution logic moved to steps.go)

// RunStepExecutor starts the step executor in a Goroutine and returns the context.CancelFunc
func RunStepExecutor(db *sql.DB) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func(ctx context.Context, db *sql.DB) {
		ticker := time.NewTicker(StepExecutorInterval)
		defer ticker.Stop()

		// Initial execution
		if err := ProcessSteps(db); err != nil {
			stepLogger.Printf("Error during initial step execution: %v", err)
		}

		for {
			select {
			case <-ticker.C:
				if err := ProcessSteps(db); err != nil {
					stepLogger.Printf("Error during periodic step execution: %v", err)
				}
			case <-ctx.Done():
				stepLogger.Println("Step executor shutting down...")
				return
			}
		}
	}(ctx, db)
	return ctx, cancel
}

// GetPgURLFromEnv loads the database connection URL from environment variables
// It checks for DATABASE_URL first, then falls back to individual DB_* variables
func GetPgURLFromEnv() (string, error) {
	err := godotenvLoad()
	if err != nil {
		return "", fmt.Errorf("error loading .env file: %w", err)
	}

	pgURL := os.Getenv("DATABASE_URL")
	if pgURL == "" {
		// Construct from individual components if DATABASE_URL not set
		pgURL = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
			os.Getenv("DB_USER"),
			os.Getenv("DB_PASSWORD"),
			os.Getenv("DB_HOST"),
			os.Getenv("DB_PORT"),
			os.Getenv("DB_NAME"),
		)
	}
	return pgURL, nil
}

func RunMigrateForce(version int) error {
	pgURL, err := GetPgURLFromEnv()
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
	if err := m.Force(version); err != nil {
		return fmt.Errorf("failed to force migration version: %w", err)
	}
	fmt.Printf("Forced migration version to %d and cleared dirty flag.\n", version)
	return nil
}

func RunMigrate(direction string) error {
	// Get database URL from environment
	pgURL, err := GetPgURLFromEnv()
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
	// Get database URL from environment
	pgURL, err := GetPgURLFromEnv()
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
	// Get database URL from environment
	pgURL, err := GetPgURLFromEnv()
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
