package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

	_ "github.com/lib/pq"
	"github.com/PortNumber53/task-sync/internal"
	"github.com/PortNumber53/task-sync/pkg/models"
)

// basePath should be set from task settings or CLI argument in a real system
// TODO: Replace with dynamic lookup from task.settings.local_path or CLI argument
var basePath string // Set this via flag, config, or task context

func main() {
	pgURL, err := internal.GetPgURLFromEnv()
	if err != nil {
		log.Fatalf("Failed to get DB URL: %v", err)
	}

	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT id, settings FROM steps")
	if err != nil {
		log.Fatalf("Failed to query steps: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var stepID int
		var settingsStr string
		if err := rows.Scan(&stepID, &settingsStr); err != nil {
			log.Printf("Failed to scan step: %v", err)
			continue
		}

		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(settingsStr), &settings); err != nil {
			log.Printf("Failed to unmarshal settings for step %d: %v", stepID, err)
			continue
		}

		for stepType, stepSettings := range settings {
			var config struct {
				Triggers struct {
					Files map[string]string `json:"files"`
				} `json:"triggers"`
			}

			if err := json.Unmarshal(stepSettings, &config); err != nil {
				continue // Not all step types have triggers.files
			}

			if len(config.Triggers.Files) > 0 {
				log.Printf("Updating hashes for step %d, type %s", stepID, stepType)
				newHashes := make(map[string]string)
				for fileName := range config.Triggers.Files {
					filePath := filepath.Join(basePath, fileName)
					newHash, err := models.GetSHA256(filePath)
					if err != nil {
						log.Printf("Error getting hash for %s: %v", filePath, err)
						continue
					}
					newHashes[fileName] = newHash
				}

				newHashesJSON, err := json.Marshal(newHashes)
				if err != nil {
					log.Printf("Failed to marshal new hashes for step %d: %v", stepID, err)
					continue
				}

				path := fmt.Sprintf("{%s,triggers,files}", stepType)
				_, err = db.Exec("UPDATE steps SET settings = jsonb_set(settings, $1, $2::jsonb) WHERE id = $3", path, newHashesJSON, stepID)
				if err != nil {
					log.Printf("Failed to update hashes for step %d: %v", stepID, err)
				}
			}
		}
	}

	if err := rows.Err(); err != nil {
		log.Fatalf("Error iterating through steps: %v", err)
	}

	log.Println("Finished updating all file hashes.")
}
