package dynamic_lab

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Criterion holds the parsed data for a single rubric criterion.
type Criterion struct {
	Title       string
	Score       int
	Required    bool
	Rubric      string
	HeldOutTest string
}

// RunRubric checks if the rubric file has changed, and if so, parses it.
// It returns the parsed criteria, the new hash, a boolean indicating if it changed, and an error.
func RunRubric(localPath, file, oldHash string) ([]Criterion, string, bool, error) {
	filePath := filepath.Join(localPath, file)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return nil, "", false, fmt.Errorf("rubric file %s does not exist", filePath)
	}

	currentHash, err := calculateHash(filePath)
	if err != nil {
		return nil, "", false, fmt.Errorf("failed to calculate hash for %s: %w", filePath, err)
	}

	if currentHash == oldHash {
		return nil, currentHash, false, nil
	}

	criteria, err := ParseRubric(filePath)
	if err != nil {
		return nil, currentHash, true, fmt.Errorf("failed to parse rubric file %s: %w", filePath, err)
	}

	return criteria, currentHash, true, nil
}

// ParseRubric parses a markdown file and extracts rubric criteria.
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

	text := string(content)
	// Split the text by criteria sections, which start with "### crit-"
	critSections := regexp.MustCompile(`(?m)^###\s*(crit-\d+.*)`).Split(text, -1)
	if len(critSections) < 2 {
		return []Criterion{}, nil // No criteria found
	}

	// Find all the titles for the criteria
	critTitles := regexp.MustCompile(`(?m)^###\s*(crit-\d+.*)`).FindAllStringSubmatch(text, -1)

	var criteria []Criterion
	for i, section := range critSections[1:] {
		// The title is the first capturing group from the critTitles regex
		title := strings.TrimSpace(critTitles[i][1])
		crit := Criterion{
			Title: title,
		}

		// Parse score: looks for "**Score**: 5"
		scoreRegex := regexp.MustCompile(`\*\*Score\*\*:\s*(\d+)`)
		scoreMatch := scoreRegex.FindStringSubmatch(section)
		if len(scoreMatch) > 1 {
			score, err := strconv.Atoi(strings.TrimSpace(scoreMatch[1]))
			if err == nil {
				crit.Score = score
			}
		}

		// Parse required: looks for "**Required**: true"
		requiredRegex := regexp.MustCompile(`\*\*Required\*\*:\s*(true|false)`)
		requiredMatch := requiredRegex.FindStringSubmatch(section)
		if len(requiredMatch) > 1 {
			if strings.ToLower(requiredMatch[1]) == "true" {
				crit.Required = true
			}
		}

		// Parse rubric description. This is trickier.
		// Let's get the text between the "Required" line and the "Held-out tests" line.
		rubricRegex := regexp.MustCompile(`(?s)\*\*Required\*\*:\s*(?:true|false)\s*\n(.*?)\n\*\*Held-out tests\*\*`)
		rubricMatch := rubricRegex.FindStringSubmatch(section)
		if len(rubricMatch) > 1 {
			crit.Rubric = strings.TrimSpace(rubricMatch[1])
		}

		// Parse held-out test: looks for "**Held-out tests**:\n` + "```bash\n...```"
		testRegex := regexp.MustCompile("(?s)\\*\\*Held-out tests\\*\\*:\\n```(?:bash)?\\n(.*?)\\n```")
		testMatch := testRegex.FindStringSubmatch(section)
		if len(testMatch) > 1 {
			crit.HeldOutTest = strings.TrimSpace(testMatch[1])
		}

		criteria = append(criteria, crit)
	}

	return criteria, nil
}
