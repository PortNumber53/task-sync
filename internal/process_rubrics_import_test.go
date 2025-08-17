package internal

import (
	"database/sql"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3" // Import for SQLite driver
	"github.com/PortNumber53/task-sync/pkg/models"
)

func TestProcessRubricsImportSteps(t *testing.T) {
	t.Skip("Skipping MHTML-based test: MHTML import support removed. Use JSON/Markdown based import tests instead.")
	models.InitStepLogger(os.Stdout)
	_ = models.Criterion{} // Dummy usage to avoid 'imported and not used' error

	// Create a temporary directory for test files
	tmpDir, err := ioutil.TempDir("", "rubrics_import_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a dummy SQLite database
	db, err := sql.Open("sqlite3", filepath.Join(tmpDir, "test.db"))
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Initialize database schema
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		local_path TEXT NOT NULL,
		status TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS steps (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL,
		settings JSON NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (task_id) REFERENCES tasks(id)
	);
	`
	_, err = db.Exec(schema)
	if err != nil {
		t.Fatalf("Failed to initialize database schema: %v", err)
	}

	// Insert a dummy task
	_, err = db.Exec("INSERT INTO tasks (local_path, status) VALUES (?, ?)", tmpDir, "active")
	if err != nil {
		t.Fatalf("Failed to insert task: %v", err)
	}

	// Get the task ID
	var taskID int
	err = db.QueryRow("SELECT id FROM tasks WHERE local_path = ?", tmpDir).Scan(&taskID)
	if err != nil {
		t.Fatalf("Failed to get task ID: %v", err)
	}

	// Insert a rubrics_import step
	stepSettings := `{"rubrics_import":{"md_file":"TASK_DATA.md"}}`
	_, err = db.Exec("INSERT INTO steps (task_id, settings, status) VALUES (?, ?, ?)", taskID, stepSettings, "pending")
	if err != nil {
		t.Fatalf("Failed to insert step: %v", err)
	}

	// Process the step
	err = processRubricsImportSteps(db, 0)
	if err != nil {
		t.Fatalf("processRubricsImportSteps failed: %v", err)
	}

	// Verify TASK_DATA.md content
	mdFilePath := filepath.Join(tmpDir, "TASK_DATA.md")
	mdContent, err := ioutil.ReadFile(mdFilePath)
	if err != nil {
		t.Fatalf("Failed to read TASK_DATA.md: %v", err)
	}

	expectedMDContent := "# TASK DATA\n\n" +
		"### crit-CRIT001: CRIT001\n\n" +
		"**Score**: 10\n" +
		"**Required**: true\n\n" +
		"This is the rubric text for criterion 1.\n\n" +
		"**Held-out tests**:\n" +
		"```bash\n" +
		"echo \"test 1\"\n" +
		"```\n\n" +
		"### crit-CRIT002: CRIT002\n\n" +
		"**Score**: 5\n" +
		"**Required**: false\n\n" +
		"This is the rubric text for criterion 2.\n\n" +
		"**Held-out tests**:\n" +
		"```bash\n" +
		"echo \"test 2\"\n" +
		"```\n\n"

	if string(mdContent) != expectedMDContent {
		t.Errorf("Generated TASK_DATA.md content mismatch.\nExpected:\n%s\nGot:\n%s", expectedMDContent, string(mdContent))
	}
}
