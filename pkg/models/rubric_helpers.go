package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Criterion defines a single rubric criterion.
type Criterion struct {
	Title       string
	Score       int
	Required    bool
	Rubric      string
	HeldOutTest string
	Counter     string
}

// ParseRubric extracts rubric criteria from a markdown or JSON file.
// Each criterion is expected to be in a section that starts with a header like:
// ### #1: d0aba505-cc93-489c-bc8b-da566a1f0af5
func ParseRubric(filePath string) ([]Criterion, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	if strings.HasSuffix(filePath, ".json") {
		var jsonCriteria []struct {
			RubricItemId string `json:"rubricItemId"`
			Score        int    `json:"score"`
			Criterion    string `json:"criterion"`
			Required     bool   `json:"required"`
			Forms        map[string]struct {
				CriterionTestCommand string `json:"criterion_test_command"`
			} `json:"forms"`
		}
		if err := json.Unmarshal(content, &jsonCriteria); err != nil {
			return nil, fmt.Errorf("failed to unmarshal JSON rubric: %w", err)
		}
		var criteria []Criterion
		for i, critJSON := range jsonCriteria {
			var crit Criterion
			crit.Counter = strconv.Itoa(i + 1)
			crit.Title = critJSON.RubricItemId
			crit.Score = critJSON.Score
			crit.Rubric = critJSON.Criterion
			crit.Required = critJSON.Required
			// Extract HeldOutTest from forms, assuming the first key if multiple exist
			if len(critJSON.Forms) > 0 {
				for _, formValue := range critJSON.Forms {
					if formValue.CriterionTestCommand != "" {
						crit.HeldOutTest = formValue.CriterionTestCommand
						break // Use the first non-empty command found
					}
				}
			} else {
				crit.HeldOutTest = ""
			}
			if crit.Title != "" && crit.HeldOutTest != "" {
				criteria = append(criteria, crit)
			}
		}
		return criteria, nil
	} else {
		// Existing markdown parsing logic starts here
		text := string(content)
		// Go's regexp engine doesn't support lookaheads. Instead, we find the start
		// index of each delimiter and slice the text into sections manually.
		re := regexp.MustCompile(`(?m)^\s*###\s*#\d+:\s*[a-fA-F0-9-]{36}`)
		matches := re.FindAllStringIndex(text, -1)

		if len(matches) == 0 {
			return nil, fmt.Errorf("no criteria sections found in %s", filePath)
		}

		var sections []string
		for i := 0; i < len(matches); i++ {
			start := matches[i][0]
			var end int
			if i+1 < len(matches) {
				end = matches[i+1][0]
			} else {
				end = len(text)
			}
			sections = append(sections, text[start:end])
		}

		var criteria []Criterion

		// Regexes to parse components of a rubric section.
		headerRe := regexp.MustCompile(`(?m)^\s*###\s*#(\d+):\s*([a-fA-F0-9-]{36})`)
		scoreRe := regexp.MustCompile(`\*\*Score\*\*:\s*(\d+)`)
		requiredRe := regexp.MustCompile(`\*\*Required\*\*:\s*(true|false)`)
		criterionRe := regexp.MustCompile(`(?s)\*\*Criterion\*\*:\s*(.*?)(?:\n\n|$)`)
		heldOutTestRe := regexp.MustCompile("(?s)\\*\\*Held-out tests\\*\\*:\\n```(?:bash)?\\n(.*?)\\n```")

		for _, section := range sections {
			if strings.TrimSpace(section) == "" {
				continue
			}

			headerMatch := headerRe.FindStringSubmatch(section)
			if len(headerMatch) < 3 {
				continue // Not a valid criterion section
			}

			crit := Criterion{
				Counter: strings.TrimSpace(headerMatch[1]),
				Title:   strings.TrimSpace(headerMatch[2]),
			}

			scoreMatch := scoreRe.FindStringSubmatch(section)
			if len(scoreMatch) > 1 {
				if score, err := strconv.Atoi(scoreMatch[1]); err == nil {
					crit.Score = score
				}
			}

			requiredMatch := requiredRe.FindStringSubmatch(section)
			if len(requiredMatch) > 1 {
				crit.Required = (requiredMatch[1] == "true")
			}

			criterionMatch := criterionRe.FindStringSubmatch(section)
			if len(criterionMatch) > 1 {
				crit.Rubric = strings.TrimSpace(criterionMatch[1])
			}

			heldOutTestMatch := heldOutTestRe.FindStringSubmatch(section)
			if len(heldOutTestMatch) > 1 {
				crit.HeldOutTest = strings.TrimSpace(heldOutTestMatch[1])
			}

			// Only add if we have the essential parts
			if crit.Title != "" && crit.HeldOutTest != "" {
				criteria = append(criteria, crit)
			}
		}

		return criteria, nil
	}
}

// GetRubricSetFromDependencies recursively searches for a rubric_set step in the dependency tree.
func GetRubricSetFromDependencies(db *sql.DB, stepID int, stepLogger *log.Logger) (*RubricSetConfig, error) {
	var settings string
	err := db.QueryRow("SELECT settings FROM steps WHERE id = $1", stepID).Scan(&settings)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Step not found, not an error
		}
		return nil, fmt.Errorf("failed to query step %d: %w", stepID, err)
	}

	var holder StepConfigHolder
	if err := json.Unmarshal([]byte(settings), &holder); err != nil {
		return nil, fmt.Errorf("failed to unmarshal settings for step %d: %w", stepID, err)
	}

	if holder.RubricSet != nil {
		return holder.RubricSet, nil
	}

	// If not found, recurse through dependencies
	for _, dep := range holder.AllDependencies() {
		rubricSet, err := GetRubricSetFromDependencies(db, dep.ID, stepLogger)
		if err != nil {
			stepLogger.Printf("Error searching for rubric_set in dependency %d: %v", dep.ID, err)
			continue // Log error and continue searching other branches
		}
		if rubricSet != nil {
			return rubricSet, nil // Found in a dependency branch
		}
	}

	return nil, nil // Not found in this branch
}

// RunRubric checks if the rubric file has changed and parses it.
// It returns the parsed criteria, the new hash, a boolean indicating if the content has changed, and an error.
func RunRubric(localPath, file, oldHash string) ([]Criterion, string, bool, error) {
	filePath := filepath.Join(localPath, file)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, "", false, fmt.Errorf("rubric file %s does not exist", filePath)
	}

	currentHash, err := GetSHA256(filePath)
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to calculate hash for %s: %w", filePath, err)
	}

	// Always parse the rubric to get the criteria
	criteria, err := ParseRubric(filePath)
	if err != nil {
		// If parsing fails, we can't determine 'changed' status reliably based on content,
		// but we can still return the hash and an error. Let's return the current hash
		// and a 'false' changed status to avoid re-runs on a broken file.
		return nil, currentHash, false, fmt.Errorf("failed to parse rubric file %s: %w", filePath, err)
	}

	// The 'changed' flag is determined by the hash comparison
	changed := currentHash != oldHash

	return criteria, currentHash, changed, nil
}
