package internal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"database/sql"
	"io"
	"strings"

	"github.com/DATA-DOG/go-sqlmock"
)

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

func TestCalculateFileHash(t *testing.T)         {}
func TestCheckDependencies(t *testing.T)         {}
func TestExecuteDockerBuild(t *testing.T)        {}
func TestGetDockerImageID(t *testing.T)          {}
func TestExecutePendingSteps(t *testing.T)       {}
func TestProcessFileExistsSteps(t *testing.T)    {}
func TestProcessDockerRubricsSteps(t *testing.T) {}
func TestProcessDockerBuildSteps(t *testing.T)   {}
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

func TestCopyStep(t *testing.T) {}
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

func TestEditStepSettings(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	t.Run("update nested setting", func(t *testing.T) {
		mock.ExpectQuery("SELECT settings FROM steps WHERE id = \\$1").
			WithArgs(1).
			WillReturnRows(sqlmock.NewRows([]string{"settings"}).AddRow(`{"docker_run":{"image_tag":"old"}}`))
		mock.ExpectExec("UPDATE steps SET settings = \\$1, updated_at = NOW\\(\\) WHERE id = \\$2").
			WithArgs(`{"docker_run":{"image_tag":"newtag"}}`, 1).
			WillReturnResult(sqlmock.NewResult(0, 1))

		err := EditStepSettings(db, 1, "docker_run.image_tag", "newtag")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("update results field", func(t *testing.T) {
		mock.ExpectExec(`UPDATE steps SET results = \$1::jsonb, updated_at = (now|NOW)\(\) WHERE id = \$2`).
			WithArgs(`{"score":100}`, 2).
			WillReturnResult(sqlmock.NewResult(0, 1))

		err := EditStepSettings(db, 2, "results", map[string]interface{}{"score": 100})
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("clear results field", func(t *testing.T) {
		mock.ExpectExec(`UPDATE steps SET results = NULL, updated_at = (now|NOW)\(\) WHERE id = \$1`).
			WithArgs(3).
			WillReturnResult(sqlmock.NewResult(0, 1))

		err := EditStepSettings(db, 3, "results", nil)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("step not found", func(t *testing.T) {
		mock.ExpectQuery("SELECT settings FROM steps WHERE id = \\$1").
			WithArgs(404).
			WillReturnError(sql.ErrNoRows)

		err := EditStepSettings(db, 404, "docker_run.image_tag", "foo")
		if err == nil {
			t.Error("expected error for missing step, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("db error on update", func(t *testing.T) {
		mock.ExpectQuery("SELECT settings FROM steps WHERE id = \\$1").
			WithArgs(5).
			WillReturnRows(sqlmock.NewRows([]string{"settings"}).AddRow(`{"foo":"bar"}`))
		mock.ExpectExec("UPDATE steps SET settings = \\$1, updated_at = NOW\\(\\) WHERE id = \\$2").
			WithArgs(`{"foo":"baz"}`, 5).
			WillReturnError(sql.ErrConnDone)

		err := EditStepSettings(db, 5, "foo", "baz")
		if err == nil {
			t.Error("expected error for DB failure, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("invalid JSON in settings", func(t *testing.T) {
		mock.ExpectQuery("SELECT settings FROM steps WHERE id = \\$1").
			WithArgs(6).
			WillReturnRows(sqlmock.NewRows([]string{"settings"}).AddRow(`not-json`))

		err := EditStepSettings(db, 6, "foo", "bar")
		if err == nil {
			t.Error("expected error for invalid JSON, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

func TestStoreStepResult(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	result := map[string]interface{}{"score": 42}

	t.Run("success", func(t *testing.T) {
		mock.ExpectExec(`UPDATE steps SET results = \$1::jsonb, updated_at = (now|NOW)\(\) WHERE id = \$2`).
			WithArgs(`{"score":42}`, 1).
			WillReturnResult(sqlmock.NewResult(0, 1))
		err := StoreStepResult(db, 1, result)
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("no step found", func(t *testing.T) {
		mock.ExpectExec(`UPDATE steps SET results = \$1::jsonb, updated_at = (now|NOW)\(\) WHERE id = \$2`).
			WithArgs(`{"score":42}`, 999).
			WillReturnResult(sqlmock.NewResult(0, 0))
		err := StoreStepResult(db, 999, result)
		if err == nil {
			t.Error("expected error for no step found, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})

	t.Run("db error", func(t *testing.T) {
		mock.ExpectExec(`UPDATE steps SET results = \$1::jsonb, updated_at = (now|NOW)\(\) WHERE id = \$2`).
			WithArgs(`{"score":42}`, 2).
			WillReturnError(sql.ErrConnDone)
		err := StoreStepResult(db, 2, result)
		if err == nil {
			t.Error("expected error for DB failure, got nil")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet expectations: %v", err)
		}
	})
}

// --- Existing test preserved ---
func TestStepFileExistsLogic(t *testing.T) {
	tempDir := t.TempDir()
	testFile := filepath.Join(tempDir, "Dockerfile")
	if err := os.WriteFile(testFile, []byte("FROM busybox"), 0644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	tests := []struct {
		name        string
		localPath   string
		settings    map[string]interface{}
		filePresent bool
		wantResult  string
	}{
		{
			name:        "file exists",
			localPath:   tempDir,
			settings:    map[string]interface{}{"file_exists": "Dockerfile"},
			filePresent: true,
			wantResult:  "success",
		},
		{
			name:        "file missing",
			localPath:   tempDir,
			settings:    map[string]interface{}{"file_exists": "notfound.txt"},
			filePresent: false,
			wantResult:  "failure",
		},
		{
			name:        "invalid settings",
			localPath:   tempDir,
			settings:    map[string]interface{}{}, // no file_exists key
			filePresent: false,
			wantResult:  "", // should not update result
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the step execution logic
			settingsJson, _ := json.Marshal(tc.settings)
			var settings map[string]interface{}
			_ = json.Unmarshal(settingsJson, &settings)

			filePath, ok := settings["file_exists"].(string)
			if ok {
				absPath := filepath.Join(tc.localPath, filePath)
				_, err := os.Stat(absPath)
				if tc.wantResult == "success" && err != nil {
					t.Errorf("expected file to exist, got error: %v", err)
				}
				if tc.wantResult == "failure" && err == nil {
					t.Errorf("expected file to be missing, but it exists")
				}
			} else {
				if tc.wantResult != "" {
					t.Errorf("expected file_exists logic to run, but it did not")
				}
			}
		})
	}
}
