package internal

import (
	"context"
	"github.com/gin-contrib/cors"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

// godotenvLoad allows mocking of godotenv.Load in tests.
var godotenvLoad = godotenv.Load

// InitStepLogger initializes the package-level step logger.
func InitStepLogger(writer io.Writer) {
	stepLogger = log.New(writer, "[StepExecutor] ", log.LstdFlags)
}

// RunAPIServer starts the Gin server and prints environment/setup info
// (Task and step logic is now in tasks.go and steps.go)
func RunAPIServer(listenAddr string) error {
	// Load config and set up logging
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Println("[Config] Error loading config:", err)
	}

	pgURL, err := GetPgURLFromEnv()
	if err != nil {
		stepLogger.Fatalf("DB config error: %v", err)
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		stepLogger.Fatalf("DB open error: %v", err)
	}
	defer db.Close()

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	// Initialize the models package logger
	models.InitStepLogger(os.Stdout)

	// Start step executor in a goroutine
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
				c.JSON(500, gin.H{"error": "Failed to scan task row"})
				return
			}
			tasks = append(tasks, map[string]interface{}{
				"id": id,
				"name": name,
				"status": status,
				"local_path": func() string { if localPath.Valid { return localPath.String } else { return "" } }(),
				"created_at": createdAt,
				"updated_at": updatedAt,
			})
		}
		c.JSON(200, gin.H{"tasks": tasks})
	})
	r.POST("/steps", func(c *gin.Context) {
		c.JSON(501, gin.H{"message": "Not implemented"})
	})
	r.GET("/steps", func(c *gin.Context) {
		c.JSON(501, gin.H{"message": "Not implemented"})
	})

	// Create HTTP server with timeouts
	srv := &http.Server{
		Addr:    listenAddr,
		Handler: r,
	}

	// Start server in a goroutine
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	// Wait for interrupt signal to gracefully shut down the server
	<-quit
	log.Println("Shutting down server...")

	// Cancel step executor context
	cancel()

	// Give a short grace period for the step executor to exit
	time.Sleep(1 * time.Second)

	// Create a deadline to wait for
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	// Doesn't block if no connections, but will otherwise wait until the timeout deadline
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	log.Println("Server exiting")
	return nil
}

// (Step execution logic moved to steps.go)

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
