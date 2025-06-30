package internal

import (
	"database/sql"
	"fmt"
	"io"

	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"

	"github.com/PortNumber53/task-sync/pkg/models"
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
		// Expect a query for task by ref
		mock.ExpectQuery("SELECT id FROM tasks WHERE ref = \\$1").
			WithArgs("42").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))
		// Expect insert and return of new ID
		mock.ExpectQuery("INSERT INTO steps").
			WithArgs(42, "Test Step", `{"foo":"bar"}`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))

		settings := `{"foo":"bar"}`
		_, err := CreateStep(db, "42", "Test Step", settings)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("valid taskRef as name", func(t *testing.T) {
		mock.ExpectQuery("SELECT id FROM tasks WHERE ref = \\$1").
			WithArgs("mytask").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
		// Expect insert and return of new ID
		mock.ExpectQuery("INSERT INTO steps").
			WithArgs(7, "Step by Name", `{"foo":123}`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))

		settings := `{"foo":123}`
		_, err := CreateStep(db, "mytask", "Step by Name", settings)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("invalid settings JSON", func(t *testing.T) {
		_, err := CreateStep(db, "42", "Bad JSON", "not-json")
		if err == nil || err.Error() == "" {
			t.Error("expected error for invalid JSON, got nil")
		}
	})

	t.Run("taskRef not found", func(t *testing.T) {
		mock.ExpectQuery("SELECT id FROM tasks WHERE ref = \\$1").
			WithArgs("notask").
			WillReturnError(sql.ErrNoRows)

		settings := `{"foo":1}`
		_, err := CreateStep(db, "notask", "No Task", settings)
		if err == nil || err.Error() == "" {
			t.Error("expected error for missing task, got nil")
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
		rows := sqlmock.NewRows([]string{"id", "task_id", "title"}).
			AddRow(1, 10, "Step1").
			AddRow(2, 20, "Step2")
		mock.ExpectQuery("SELECT id, task_id, title FROM steps ORDER BY id").
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
		rows := sqlmock.NewRows([]string{"id", "task_id", "title", "settings"}).
			AddRow(1, 10, "Step1", "{\"foo\":1}")
		mock.ExpectQuery("SELECT id, task_id, title, settings FROM steps ORDER BY id").
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
		mock.ExpectQuery("SELECT id, task_id, title FROM steps ORDER BY id").
			WillReturnError(sql.ErrConnDone)
		err := ListSteps(db, false)
		if err == nil {
			t.Error("expected error from db, got nil")
		}
	})

	t.Run("no rows", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "task_id", "title"})
		mock.ExpectQuery("SELECT id, task_id, title FROM steps ORDER BY id").
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
		if output != "Steps:\n" {
			t.Errorf("expected output 'Steps:\n', got %q", output)
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

		hash, err := models.GetSHA256(tmpfile.Name())
		if err != nil {
			t.Errorf("expected no error, but got: %v", err)
		}

		if hash != expectedHash {
			t.Errorf("expected hash '%s', but got '%s'", expectedHash, hash)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		_, err := models.GetSHA256("non-existent-file.txt")
		if err == nil {
			t.Error("expected an error for a non-existent file, but got nil")
		}
	})
}

func TestExecutePendingSteps(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	// Use a channel to record the order of function calls
	callOrder := make(chan string, 10) // Increased buffer size



	// Create a map of mock step processors
	mockStepProcessors := map[string]func(*sql.DB) error{
		"dynamic_lab": func(db *sql.DB) error {
			callOrder <- "dynamic_lab"
			return nil
		},
		"docker_pull": func(db *sql.DB) error {
			callOrder <- "docker_pull"
			return nil
		},
		"docker_build": func(db *sql.DB) error {
			callOrder <- "docker_build"
			return nil
		},
		"docker_run": func(db *sql.DB) error {
			callOrder <- "docker_run"
			return nil
		},
		"docker_shell": func(db *sql.DB) error {
			callOrder <- "docker_shell"
			return nil
		},
		"docker_rubrics": func(db *sql.DB) error {
			callOrder <- "docker_rubrics"
			return nil
		},
		"file_exists": func(db *sql.DB) error {
			callOrder <- "file_exists"
			return nil
		},
	}

	// Call the function to be tested
	err = executePendingSteps(db, mockStepProcessors)
	if err != nil {
		t.Fatalf("executePendingSteps returned an unexpected error: %v", err)
	}

	close(callOrder)

	// Verify the call order
	// The order is determined by the map iteration, which is not guaranteed in Go.
	// So, we check if all expected functions were called, regardless of order.
	expectedCalls := map[string]bool{
		"dynamic_lab":    true,
		"docker_pull":    true,
		"docker_build":   true,
		"docker_run":     true,
		"docker_shell":   true,
		"docker_rubrics": true,
		"file_exists":    true,
	}

	actualCalls := make(map[string]bool)
	for call := range callOrder {
		actualCalls[call] = true
	}

	if len(actualCalls) != len(expectedCalls) {
		t.Errorf("Expected %d function calls, but got %d", len(expectedCalls), len(actualCalls))
	}

	for expected := range expectedCalls {
		if !actualCalls[expected] {
			t.Errorf("Expected function '%s' to be called, but it was not", expected)
		}
	}

	// Check if all expectations were met
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("there were unfulfilled expectations: %s", err)
	}
}
func TestGetStepInfo(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	t.Run("success", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "task_id", "title", "settings", "results", "created_at", "updated_at"}).
			AddRow(1, 101, "Step 1", `{"key":"val"}`, `{"res":"ok"}`, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC))
		mock.ExpectQuery(`SELECT\s+s\.id,\s+s\.task_id,\s+s\.title,\s+s\.settings::text,\s+s\.results::text,\s+s\.created_at,\s+s\.updated_at\s+FROM\s+steps\s+s\s+WHERE\s+s\.id\s*=\s*\$1`).
			WithArgs(1).
			WillReturnRows(rows)
		info, err := GetStepInfo(db, 1)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if info == nil || info.ID != 1 || info.Title != "Step 1" {
			t.Errorf("unexpected info: %+v", info)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("not found", func(t *testing.T) {
		mock.ExpectQuery(`SELECT\s+s\.id,\s+s\.task_id,\s+s\.title,\s+s\.settings::text,\s+s\.results::text,\s+s\.created_at,\s+s\.updated_at\s+FROM\s+steps\s+s\s+WHERE\s+s\.id\s*=\s*\$1`).
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
		mock.ExpectQuery(`SELECT\s+s\.id,\s+s\.task_id,\s+s\.title,\s+s\.settings::text,\s+s\.results::text,\s+s\.created_at,\s+s\.updated_at\s+FROM\s+steps\s+s\s+WHERE\s+s\.id\s*=\s*\$1`).
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
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	t.Run("successful copy", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+tasks\s+WHERE\s+id\s*=\s*\$1\s*\)`).
			WithArgs(101).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

		rows := sqlmock.NewRows([]string{"title", "settings"}).
			AddRow("test_step", "{}")
		mock.ExpectQuery(`SELECT\s+title,\s+settings\s+FROM\s+steps\s+WHERE\s+id\s*=\s*\$1`).
			WithArgs(1).
			WillReturnRows(rows)

		mock.ExpectQuery("INSERT INTO steps").
			WithArgs(101, "test_step", "{}").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(2))
		mock.ExpectCommit()

		newID, err := CopyStep(db, 1, 101)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if newID != 2 {
			t.Errorf("expected new ID to be 2, got %d", newID)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("target task not found", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+tasks\s+WHERE\s+id\s*=\s*\$1\s*\)`).
			WithArgs(999).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectRollback()

		newID, err := CopyStep(db, 1, 999)
		if err == nil {
			t.Error("expected error for non-existent target task, got nil")
		}
		if newID != 0 {
			t.Errorf("expected new ID to be 0, got %d", newID)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("source step not found", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+tasks\s+WHERE\s+id\s*=\s*\$1\s*\)`).
			WithArgs(101).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

		mock.ExpectQuery(`SELECT\s+title,\s+settings\s+FROM\s+steps\s+WHERE\s+id\s*=\s*\$1`).
			WithArgs(99).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()

		newID, err := CopyStep(db, 99, 101)
		if err == nil {
			t.Error("expected error for non-existent source step, got nil")
		}
		if newID != 0 {
			t.Errorf("expected new ID to be 0, got %d", newID)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("insert fails", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+tasks\s+WHERE\s+id\s*=\s*\$1\s*\)`).
			WithArgs(101).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

		rows := sqlmock.NewRows([]string{"title", "settings"}).
			AddRow("test_step", "{}")
		mock.ExpectQuery(`SELECT\s+title,\s+settings\s+FROM\s+steps\s+WHERE\s+id\s*=\s*\$1`).
			WithArgs(1).
			WillReturnRows(rows)

		mock.ExpectQuery("INSERT INTO steps").
			WithArgs(101, "test_step", "{}").
			WillReturnError(fmt.Errorf("insert failed"))
		mock.ExpectRollback()

		newID, err := CopyStep(db, 1, 101)
		if err == nil {
			t.Error("expected error on insert, got nil")
		}
		if newID != 0 {
			t.Errorf("expected new ID to be 0 on failure, got %d", newID)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("query fails", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+tasks\s+WHERE\s+id\s*=\s*\$1\s*\)`).
			WithArgs(101).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

		mock.ExpectQuery(`SELECT\s+title,\s+settings\s+FROM\s+steps\s+WHERE\s+id\s*=\s*\$1`).
			WithArgs(1).
			WillReturnError(fmt.Errorf("query failed"))
		mock.ExpectRollback()

		newID, err := CopyStep(db, 1, 101)
		if err == nil {
			t.Error("expected error on query, got nil")
		}
		if newID != 0 {
			t.Errorf("expected new ID to be 0 on failure, got %d", newID)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

func TestCheckDependencies(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("an error '%s' was not expected when opening a stub database connection", err)
	}
	defer db.Close()

	t.Run("no dependencies", func(t *testing.T) {
		mock.ExpectQuery(`SELECT COALESCE\s*\(\s*\(SELECT value FROM jsonb_each\s*\(settings\)\s*WHERE key = 'depends_on'\)\s*,\s*'\[\]'::jsonb\s*\)\s*::text\s*FROM steps WHERE id = \$1`).
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow("[]"))

		stepExec := &models.StepExec{StepID: 1}
		ok, err := models.CheckDependencies(db, stepExec)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !ok {
			t.Errorf("expected true, got false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("dependencies met", func(t *testing.T) {
		dependsOnJSON := `[{"id": 2}, {"id": 3}]`
		mock.ExpectQuery(`SELECT\s*COALESCE\s*\(\s*\(\s*SELECT\s+value\s+FROM\s+jsonb_each\s*\(\s*settings\s*\)\s*WHERE\s+key\s*=\s*'depends_on'\s*\)\s*,\s*'\[\]'::jsonb\s*\)\s*::text\s*FROM\s+steps\s+WHERE\s+id\s*=\s*\$1`).
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(dependsOnJSON))

		query := `SELECT\s+NOT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+steps\s+s\s+WHERE\s+s\.id\s*=\s*ANY\(\$1::int\[\]\)\s+AND\s+\(s\.results-\>\>'result'\s+IS\s+NULL\s+OR\s+s\.results-\>\>'result'\s+!=\s+'success'\)\s*\)`
		mock.ExpectQuery(query).
			WithArgs(pq.Array([]int{2, 3})).
			WillReturnRows(sqlmock.NewRows([]string{"not_exists"}).AddRow(true))

		stepExec := &models.StepExec{StepID: 1}
		ok, err := models.CheckDependencies(db, stepExec)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !ok {
			t.Errorf("expected true, got false")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("dependencies not met (one failed)", func(t *testing.T) {
		dependsOnJSON := `[{"id": 2}, {"id": 3}]`
		mock.ExpectQuery(`SELECT\s*COALESCE\s*\(\s*\(\s*SELECT\s+value\s+FROM\s+jsonb_each\s*\(\s*settings\s*\)\s*WHERE\s+key\s*=\s*'depends_on'\s*\)\s*,\s*'\[\]'::jsonb\s*\)\s*::text\s*FROM\s+steps\s+WHERE\s+id\s*=\s*\$1`).
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(dependsOnJSON))

		query := `SELECT\s+NOT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+steps\s+s\s+WHERE\s+s\.id\s*=\s*ANY\(\$1::int\[\]\)\s+AND\s+\(s\.results-\>\>'result'\s+IS\s+NULL\s+OR\s+s\.results-\>\>'result'\s+!=\s+'success'\)\s*\)`
		mock.ExpectQuery(query).
			WithArgs(pq.Array([]int{2, 3})).
			WillReturnRows(sqlmock.NewRows([]string{"not_exists"}).AddRow(false))

		stepExec := &models.StepExec{StepID: 1}
		ok, err := models.CheckDependencies(db, stepExec)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if ok {
			t.Errorf("expected false, got true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("db error on dependency query", func(t *testing.T) {
		dependsOnJSON := `[{"id": 2}]`
		mock.ExpectQuery(`SELECT\s*COALESCE\s*\(\s*\(\s*SELECT\s+value\s+FROM\s+jsonb_each\s*\(\s*settings\s*\)\s*WHERE\s+key\s*=\s*'depends_on'\s*\)\s*,\s*'\[\]'::jsonb\s*\)\s*::text\s*FROM\s+steps\s+WHERE\s+id\s*=\s*\$1`).
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(dependsOnJSON))

		query := `SELECT\s+NOT\s+EXISTS\s*\(\s*SELECT\s+1\s+FROM\s+steps\s+s\s+WHERE\s+s\.id\s*=\s*ANY\(\$1::int\[\]\)\s+AND\s+\(s\.results-\>\>'result'\s+IS\s+NULL\s+OR\s+s\.results-\>\>'result'\s+!=\s+'success'\)\s*\)`
		mock.ExpectQuery(query).
			WithArgs(pq.Array([]int{2})).
			WillReturnError(fmt.Errorf("db error"))

		stepExec := &models.StepExec{StepID: 1}
		ok, err := models.CheckDependencies(db, stepExec)
		if err == nil {
			t.Errorf("expected error, got nil")
		}
		if ok {
			t.Errorf("expected false, got true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("db error on initial query", func(t *testing.T) {
		mock.ExpectQuery(`SELECT COALESCE\s*\(\s*\(SELECT value FROM jsonb_each\s*\(settings\)\s*WHERE key = 'depends_on'\)\s*,\s*'\[\]'::jsonb\s*\)\s*::text\s*FROM steps WHERE id = \$1`).
			WithArgs(1).
			WillReturnError(fmt.Errorf("db error"))

		stepExec := &models.StepExec{StepID: 1}
		ok, err := models.CheckDependencies(db, stepExec)
		if err == nil {
			t.Errorf("expected error, got nil")
		}
		if ok {
			t.Errorf("expected false, got true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("there were unfulfilled expectations: %s", err)
		}
	})

	t.Run("invalid json for depends_on", func(t *testing.T) {
		dependsOnJSON := `not-a-json`
		mock.ExpectQuery(`SELECT COALESCE\s*\(\s*\(SELECT value FROM jsonb_each\s*\(settings\)\s*WHERE key = 'depends_on'\)\s*,\s*'\[\]'::jsonb\s*\)\s*::text\s*FROM steps WHERE id = \$1`).
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows([]string{"coalesce"}).AddRow(dependsOnJSON))

		stepExec := &models.StepExec{StepID: 1}
		ok, err := models.CheckDependencies(db, stepExec)
		if err == nil {
			t.Errorf("expected error, got nil")
		}
		if ok {
			t.Errorf("expected false, got true")
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
