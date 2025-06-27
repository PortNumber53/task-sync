package internal

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// sqlOpen is a package-level variable to allow mocking of sql.Open in tests.
var sqlOpen = sql.Open

// --- Comprehensive coverage stubs ---
func TestCreateStep(t *testing.T) {
	// Setup sqlmock
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	t.Run("valid taskRef as ID", func(t *testing.T) {
		// Expect a query for task by ID
		mock.ExpectQuery("SELECT id FROM tasks WHERE id = \\$1").
			WithArgs(42).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))
		// Expect insert
		mock.ExpectExec("INSERT INTO steps").
			WithArgs(42, "Test Step", `{"foo":"bar"}`).
			WillReturnResult(sqlmock.NewResult(1, 1))

		settings := `{"foo":"bar"}`
		err := CreateStep(db, "42", "Test Step", settings)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("valid taskRef as name", func(t *testing.T) {
		mock.ExpectQuery("SELECT id FROM tasks WHERE name = \\$1").
			WithArgs("mytask").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
		mock.ExpectExec("INSERT INTO steps").
			WithArgs(7, "Step by Name", `{"foo":123}`).
			WillReturnResult(sqlmock.NewResult(1, 1))

		settings := `{"foo":123}`
		err := CreateStep(db, "mytask", "Step by Name", settings)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("invalid settings JSON", func(t *testing.T) {
		err := CreateStep(db, "42", "Bad JSON", "not-json")
		if err == nil || err.Error() == "" {
			t.Error("expected error for invalid JSON, got nil")
		}
	})

	t.Run("taskRef not found", func(t *testing.T) {
		mock.ExpectQuery("SELECT id FROM tasks WHERE name = \\$1").
			WithArgs("notask").
			WillReturnError(sql.ErrNoRows)

		settings := `{"foo":1}`
		err := CreateStep(db, "notask", "No Task", settings)
		if err == nil || err.Error() == "" {
			t.Error("expected error for missing task, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}
func TestActivateStep(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	t.Run("success", func(t *testing.T) {
		mock.ExpectExec("UPDATE steps SET status = 'active', updated_at = NOW\\(\\) WHERE id = \\$1").
			WithArgs(123).
			WillReturnResult(sqlmock.NewResult(0, 1)) // 1 row affected

		err := ActivateStep(db, 123)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("no step found", func(t *testing.T) {
		mock.ExpectExec("UPDATE steps SET status = 'active', updated_at = NOW\\(\\) WHERE id = \\$1").
			WithArgs(999).
			WillReturnResult(sqlmock.NewResult(0, 0)) // 0 rows affected

		err := ActivateStep(db, 999)
		if err == nil || err.Error() == "" {
			t.Error("expected error for no step found, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("db error", func(t *testing.T) {
		mock.ExpectExec("UPDATE steps SET status = 'active', updated_at = NOW\\(\\) WHERE id = \\$1").
			WithArgs(1).
			WillReturnError(sql.ErrConnDone)

		err := ActivateStep(db, 1)
		if err == nil {
			t.Error("expected error for DB failure, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

func TestListSteps(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	t.Run("full=false, prints basic columns", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "task_id", "title", "status", "created_at", "updated_at"}).
			AddRow(1, 10, "Step1", "active", "2024-01-01", "2024-01-02").
			AddRow(2, 20, "Step2", "new", "2024-02-01", "2024-02-02")
		mock.ExpectQuery("SELECT id, task_id, title, status, created_at, updated_at FROM steps ORDER BY id").
			WillReturnRows(rows)

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := ListSteps(db, false)
		w.Close()
		os.Stdout = oldStdout

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out, _ := io.ReadAll(r)
		output := string(out)
		if !strings.Contains(output, "Step1") || !strings.Contains(output, "Step2") {
			t.Errorf("output missing expected step names: %q", output)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("full=true, prints settings", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "task_id", "title", "status", "settings", "created_at", "updated_at"}).
			AddRow(1, 10, "Step1", "active", "{\"foo\":1}", "2024-01-01", "2024-01-02")
		mock.ExpectQuery("SELECT id, task_id, title, status, settings, created_at, updated_at FROM steps ORDER BY id").
			WillReturnRows(rows)

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := ListSteps(db, true)
		w.Close()
		os.Stdout = oldStdout

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out, _ := io.ReadAll(r)
		output := string(out)
		if !strings.Contains(output, "{\"foo\":1}") {
			t.Errorf("output missing expected settings: %q", output)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("db error", func(t *testing.T) {
		mock.ExpectQuery("SELECT id, task_id, title, status, created_at, updated_at FROM steps ORDER BY id").
			WillReturnError(sql.ErrConnDone)
		err := ListSteps(db, false)
		if err == nil {
			t.Error("expected error from db, got nil")
		}
	})

	t.Run("no rows", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "task_id", "title", "status", "created_at", "updated_at"})
		mock.ExpectQuery("SELECT id, task_id, title, status, created_at, updated_at FROM steps ORDER BY id").
			WillReturnRows(rows)

		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w

		err := ListSteps(db, false)
		w.Close()
		os.Stdout = oldStdout

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out, _ := io.ReadAll(r)
		output := string(out)
		if !strings.Contains(output, "ID") || strings.Contains(output, "Step1") {
			t.Errorf("output missing expected header or contains unexpected data: %q", output)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

func TestCalculateFileHash(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Create a temporary file with known content
		content := []byte("hello world")
		tmpfile, err := os.CreateTemp("", "testfile-*.txt")
		if err != nil {
			t.Fatalf("Failed to create temp file: %v", err)
		}
		defer os.Remove(tmpfile.Name()) // clean up

		if _, err := tmpfile.Write(content); err != nil {
			t.Fatalf("Failed to write to temp file: %v", err)
		}
		if err := tmpfile.Close(); err != nil {
			t.Fatalf("Failed to close temp file: %v", err)
		}

		// Expected hash for "hello world"
		// echo -n "hello world" | sha256sum
		expectedHash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

		hash, err := calculateFileHash(tmpfile.Name())
		if err != nil {
			t.Errorf("expected no error, but got: %v", err)
		}

		if hash != expectedHash {
			t.Errorf("expected hash '%s', but got '%s'", expectedHash, hash)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := calculateFileHash("non-existent-file.txt")
		if err == nil {
			t.Error("expected an error for a non-existent file, but got nil")
		}
	})
}
func TestCheckDependencies(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	// Mock the logger to discard output during tests
	originalLogger := stepLogger
	stepLogger = log.New(io.Discard, "", 0)
	defer func() { stepLogger = originalLogger }()

	stepID := 100

	t.Run("no dependencies", func(t *testing.T) {
		ok, err := checkDependencies(db, stepID, []struct {
			ID int `json:"id"`
		}{})
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if !ok {
			t.Error("expected true when no dependencies, got false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	deps := []struct {
		ID int `json:"id"`
	}{{ID: 1}, {ID: 2}, {ID: 3}}

	t.Run("all dependencies met", func(t *testing.T) {
		// Mock individual status checks (for logging part)
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(1).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(2).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("active", sql.NullString{String: `{"result":"success"}`, Valid: true}))
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(3).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{String: `{"result":"failure"}`, Valid: true})) // Status 'success' overrides result

		// Mock main dependency check query
		query := `
		SELECT NOT EXISTS (
			SELECT 1 FROM steps
			WHERE id IN ($2,$3,$4)
			AND id != $1
			AND status != 'success'
			AND (results IS NULL OR results->>'result' IS NULL OR results->>'result' != 'success')
		)`
		mock.ExpectQuery(query).WithArgs(stepID, 1, 2, 3).WillReturnRows(sqlmock.NewRows([]string{"not_exists"}).AddRow(true))

		ok, err := checkDependencies(db, stepID, deps)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if !ok {
			t.Error("expected true when all dependencies met, got false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("one dependency pending", func(t *testing.T) {
		// Mock individual status checks
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(1).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(2).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("pending", sql.NullString{}))
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(3).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))

		// Mock main dependency check query
		query := `
		SELECT NOT EXISTS (
			SELECT 1 FROM steps
			WHERE id IN ($2,$3,$4)
			AND id != $1
			AND status != 'success'
			AND (results IS NULL OR results->>'result' IS NULL OR results->>'result' != 'success')
		)`
		mock.ExpectQuery(query).WithArgs(stepID, 1, 2, 3).WillReturnRows(sqlmock.NewRows([]string{"not_exists"}).AddRow(false))

		ok, err := checkDependencies(db, stepID, deps)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if ok {
			t.Error("expected false when one dependency pending, got true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("one dependency failed (by result)", func(t *testing.T) {
		// Mock individual status checks
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(1).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(2).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("active", sql.NullString{String: `{"result":"failure"}`, Valid: true}))
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(3).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))

		// Mock main dependency check query
		query := `
		SELECT NOT EXISTS (
			SELECT 1 FROM steps
			WHERE id IN ($2,$3,$4)
			AND id != $1
			AND status != 'success'
			AND (results IS NULL OR results->>'result' IS NULL OR results->>'result' != 'success')
		)`
		mock.ExpectQuery(query).WithArgs(stepID, 1, 2, 3).WillReturnRows(sqlmock.NewRows([]string{"not_exists"}).AddRow(false))

		ok, err := checkDependencies(db, stepID, deps)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if ok {
			t.Error("expected false when one dependency failed, got true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("dependency check query error", func(t *testing.T) {
		// Mock individual status checks
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(1).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(2).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(3).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))

		dbErr := fmt.Errorf("db error on check")
		// Mock main dependency check query to return an error
		query := `
		SELECT NOT EXISTS (
			SELECT 1 FROM steps
			WHERE id IN ($2,$3,$4)
			AND id != $1
			AND status != 'success'
			AND (results IS NULL OR results->>'result' IS NULL OR results->>'result' != 'success')
		)`
		mock.ExpectQuery(query).WithArgs(stepID, 1, 2, 3).WillReturnError(dbErr)

		ok, err := checkDependencies(db, stepID, deps)
		if err == nil {
			t.Error("expected an error, got nil")
		} else if err != dbErr {
			t.Errorf("expected error '%v', got '%v'", dbErr, err)
		}
		if ok {
			t.Error("expected false on db error, got true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("dependency status query error", func(t *testing.T) {
		dbErr := fmt.Errorf("db error on status check")
		// Mock first individual status check to return an error
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(1).WillReturnError(dbErr)
		// Other status checks might not be called if the first one errors and logging continues,
		// but the main query will still run.
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(2).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))
		mock.ExpectQuery("SELECT status, results FROM steps WHERE id = $1").WithArgs(3).WillReturnRows(sqlmock.NewRows([]string{"status", "results"}).AddRow("success", sql.NullString{}))

		// Mock main dependency check query, assuming it still runs and finds all (mocked) deps complete
		query := `
		SELECT NOT EXISTS (
			SELECT 1 FROM steps
			WHERE id IN ($2,$3,$4)
			AND id != $1
			AND status != 'success'
			AND (results IS NULL OR results->>'result' IS NULL OR results->>'result' != 'success')
		)`
		mock.ExpectQuery(query).WithArgs(stepID, 1, 2, 3).WillReturnRows(sqlmock.NewRows([]string{"not_exists"}).AddRow(true))

		ok, err := checkDependencies(db, stepID, deps)
		if err != nil { // The error from individual status check is logged but not returned by checkDependencies itself
			t.Errorf("expected no error from checkDependencies for individual status query error, got %v", err)
		}
		if !ok { // Expecting true because the main query will succeed based on its mocks
			t.Error("expected true as main query should succeed, got false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})
}
func TestExecutePendingSteps(t *testing.T) {
	// Use a channel to record the order of function calls
	callOrder := make(chan string, 4)

	// Replace the real functions with mocks for the duration of the test
	originalFileExists := processFileExistsStepsFunc
	originalDockerBuild := processDockerBuildStepsFunc
	originalDockerRun := processDockerRunStepsFunc
	originalDockerRubrics := processDockerRubricsStepsFunc

	processFileExistsStepsFunc = func(db *sql.DB) {
		callOrder <- "file_exists"
	}
	processDockerBuildStepsFunc = func(db *sql.DB) {
		callOrder <- "docker_build"
	}
	processDockerRunStepsFunc = func(db *sql.DB) {
		callOrder <- "docker_run"
	}
	processDockerRubricsStepsFunc = func(db *sql.DB) {
		callOrder <- "docker_rubrics"
	}

	// Restore original functions after the test
	defer func() {
		processFileExistsStepsFunc = originalFileExists
		processDockerBuildStepsFunc = originalDockerBuild
		processDockerRunStepsFunc = originalDockerRun
		processDockerRubricsStepsFunc = originalDockerRubrics
	}()

	// Setup a mock DB. Even though the downstream funcs are mocked, the top-level func might use it.
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	// Call the function to be tested
	err = executePendingSteps(db)
	if err != nil {
		t.Fatalf("executePendingSteps returned an unexpected error: %v", err)
	}

	close(callOrder)

	// Verify the call order
	expectedOrder := []string{"file_exists", "docker_build", "docker_run", "docker_rubrics"}
	actualOrder := []string{}
	for call := range callOrder {
		actualOrder = append(actualOrder, call)
	}

	if !reflect.DeepEqual(expectedOrder, actualOrder) {
		t.Errorf("Expected call order %v, but got %v", expectedOrder, actualOrder)
	}
}
func TestGetStepInfo(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	t.Run("success", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "task_id", "title", "status", "settings", "results", "created_at", "updated_at"}).
			AddRow(1, 10, "Step1", "active", `{"foo":1}`, `{"score":5}`,
				time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC))
		mock.ExpectQuery(`SELECT\s+s.id,\s+s.task_id,\s+s.title,\s+s.status,\s+s.settings::text,\s+s.results::text,\s+s.created_at,\s+s.updated_at\s+FROM\s+steps\s+s\s+WHERE\s+s.id\s*=\s*\$1`).
			WithArgs(1).
			WillReturnRows(rows)
		info, err := GetStepInfo(db, 1)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if info == nil || info.ID != 1 || info.Title != "Step1" {
			t.Errorf("unexpected info: %+v", info)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		mock.ExpectQuery(`SELECT\s+s.id,\s+s.task_id,\s+s.title,\s+s.status,\s+s.settings::text,\s+s.results::text,\s+s.created_at,\s+s.updated_at\s+FROM\s+steps\s+s\s+WHERE\s+s.id\s*=\s*\$1`).
			WithArgs(999).
			WillReturnError(sql.ErrNoRows)
		info, err := GetStepInfo(db, 999)
		if err == nil {
			t.Error("expected error for not found, got nil")
		}
		if info != nil {
			t.Errorf("expected nil info, got %+v", info)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("db error", func(t *testing.T) {
		mock.ExpectQuery(`SELECT\s+s.id,\s+s.task_id,\s+s.title,\s+s.status,\s+s.settings::text,\s+s.results::text,\s+s.created_at,\s+s.updated_at\s+FROM\s+steps\s+s\s+WHERE\s+s.id\s*=\s*\$1`).
			WithArgs(2).
			WillReturnError(sql.ErrConnDone)
		info, err := GetStepInfo(db, 2)
		if err == nil {
			t.Error("expected error for DB failure, got nil")
		}
		if info != nil {
			t.Errorf("expected nil info, got %+v", info)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

func TestCopyStep(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	t.Run("successful copy", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)").
			WithArgs(2).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery("SELECT title, status, settings FROM steps WHERE id = $1").
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows([]string{"title", "status", "settings"}).AddRow("Source Step", "active", "{\"key\":\"value\"}"))
		mock.ExpectExec("INSERT INTO steps (task_id, title, settings, status, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now())").
			WithArgs(2, "Source Step", "{\"key\":\"value\"}", "active").
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()

		err := CopyStep(db, 1, 2)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("source step does not exist", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)").
			WithArgs(2).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery("SELECT title, status, settings FROM steps WHERE id = $1").
			WithArgs(99).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()

		err := CopyStep(db, 99, 2)
		if err == nil {
			t.Error("expected an error but got nil")
		} else if !strings.Contains(err.Error(), "source step with ID 99 does not exist") {
			t.Errorf("expected error 'source step with ID 99 does not exist', got '%v'", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("target task does not exist", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)").
			WithArgs(99).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectRollback()

		err := CopyStep(db, 1, 99)
		if err == nil {
			t.Error("expected an error but got nil")
		} else if !strings.Contains(err.Error(), "target task with ID 99 does not exist") {
			t.Errorf("expected error 'target task with ID 99 does not exist', got '%v'", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("commit fails", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT EXISTS(SELECT 1 FROM tasks WHERE id = $1)").
			WithArgs(2).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		mock.ExpectQuery("SELECT title, status, settings FROM steps WHERE id = $1").
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows([]string{"title", "status", "settings"}).AddRow("Source Step", "active", "{\"key\":\"value\"}"))
		mock.ExpectExec("INSERT INTO steps (task_id, title, settings, status, created_at, updated_at) VALUES ($1, $2, $3, $4, now(), now())").
			WithArgs(2, "Source Step", "{\"key\":\"value\"}", "active").
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit().WillReturnError(fmt.Errorf("commit failed"))
		// The deferred tx.Rollback() will be called in actual execution,
		// but sqlmock might not track it after a Commit() error.

		err := CopyStep(db, 1, 2)
		if err == nil {
			t.Error("expected an error but got nil")
		} else if !strings.Contains(err.Error(), "error committing transaction: commit failed") {
			t.Errorf("expected error 'error committing transaction: commit failed', got '%v'", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})
}
func TestClearStepResults(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	t.Run("success", func(t *testing.T) {
		mock.ExpectExec("UPDATE steps SET results = NULL, updated_at = NOW\\(\\) WHERE id = \\$1").
			WithArgs(42).
			WillReturnResult(sqlmock.NewResult(0, 1))
		err := ClearStepResults(db, 42)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("no step found", func(t *testing.T) {
		mock.ExpectExec("UPDATE steps SET results = NULL, updated_at = NOW\\(\\) WHERE id = \\$1").
			WithArgs(999).
			WillReturnResult(sqlmock.NewResult(0, 0))
		err := ClearStepResults(db, 999)
		if err == nil || err.Error() == "" {
			t.Error("expected error for no step found, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("db error", func(t *testing.T) {
		mock.ExpectExec("UPDATE steps SET results = NULL, updated_at = NOW\\(\\) WHERE id = \\$1").
			WithArgs(1).
			WillReturnError(sql.ErrConnDone)
		err := ClearStepResults(db, 1)
		if err == nil {
			t.Error("expected error for DB failure, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}
