package internal

import (
	"database/sql"
	"fmt"
)

// CreateTask inserts a new task with name and status. Only allows status: active, inactive, disabled, running.
func isValidTaskStatus(status string) bool {
	valid := map[string]bool{"active": true, "inactive": true, "disabled": true, "running": true}
	return valid[status]
}

func CreateTask(name, status string) error {
	if !isValidTaskStatus(status) {
		return fmt.Errorf("invalid status: %s (must be one of active|inactive|disabled|running)", status)
	}
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO tasks (name, status, created_at, updated_at) VALUES ($1, $2, now(), now())`, name, status)
	return err
}

// ListTasks prints all tasks in the DB
func ListTasks() error {
	pgURL, err := getPgURLFromEnv()
	if err != nil {
		return err
	}
	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, name, status, local_path, created_at, updated_at FROM tasks ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Printf("%-4s %-20s %-10s %-30s %-25s %-25s\n", "ID", "Name", "Status", "Local Path", "Created At", "Updated At")
	for rows.Next() {
		var id int
		var name, status string
		var localPath sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&id, &name, &status, &localPath, &createdAt, &updatedAt); err != nil {
			return err
		}
		lp := ""
		if localPath.Valid {
			lp = localPath.String
		}
		fmt.Printf("%-4d %-20s %-10s %-30s %-25s %-25s\n", id, name, status, lp, createdAt, updatedAt)
	}
	return nil
}
