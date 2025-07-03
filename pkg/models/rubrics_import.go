package models

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// ProcessRubricsMHTML parses the rubrics.mhtml file and generates TASK_DATA.md.
func ProcessRubricsMHTML(mhtmlFilePath, mdFilePath string) error {
	// Read the MHTML file
	mhtmlContentBytes, err := os.ReadFile(mhtmlFilePath)
	if err != nil {
		return fmt.Errorf("failed to read MHTML file: %w", err)
	}

	// Find the end of the main headers, handling possible line endings (LF or CRLF)
	headerEndIndexLF := bytes.Index(mhtmlContentBytes, []byte("\n\n"))
	headerEndIndexCRLF := bytes.Index(mhtmlContentBytes, []byte("\r\n\r\n"))
	headerEndIndex := -1
	if headerEndIndexLF != -1 {
		headerEndIndex = headerEndIndexLF
	} else if headerEndIndexCRLF != -1 {
		headerEndIndex = headerEndIndexCRLF
	}
	if headerEndIndex == -1 {
		return fmt.Errorf("could not find end of main headers in MHTML file")
	}
	headerContent := string(mhtmlContentBytes[:headerEndIndex])
	fmt.Printf("DEBUG: Header content: %s\n", headerContent)  // Added for debugging MHTML header parsing
	bodyStart := headerEndIndex + len([]byte("\n\n")) // Use len of LF pattern, adjust if CRLF was found
	if bytes.Equal(mhtmlContentBytes[headerEndIndex:headerEndIndex+4], []byte("\r\n\r\n")) {
		bodyStart = headerEndIndex + 4
	}

	// Parse headers to find and unfold Content-Type for accurate parameter extraction
	var fullCTValue string
	inCTHeader := false
	for _, line := range strings.Split(headerContent, "\n") {
		if strings.HasPrefix(line, "Content-Type:") {
			fullCTValue = strings.TrimPrefix(line, "Content-Type:")
			inCTHeader = true  // Start collecting folded lines
		} else if inCTHeader && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {  // Folded line (check raw line for leading whitespace)
			fullCTValue += strings.TrimSpace(line)  // Trim and append folded content
		} else if inCTHeader {
			inCTHeader = false  // End of header if no folding
		}
	}
	if fullCTValue == "" {
		return fmt.Errorf("could not find Content-Type header in MHTML file")
	}
	fmt.Printf("DEBUG: fullCTValue: %s\n", fullCTValue)  // Debug: log the full unfolded Content-Type value
	_, params, err := mime.ParseMediaType(fullCTValue)
	if err != nil {
		return fmt.Errorf("failed to parse media type: %w", err)
	}
	fmt.Printf("DEBUG: Parsed params: %v\n", params)  // Debug: log the parsed parameters
	boundary, ok := params["boundary"]
	if !ok {
		return fmt.Errorf("could not find MIME boundary in MHTML file")
	}

	// Create a multipart reader for the body
	reader := multipart.NewReader(bytes.NewReader(mhtmlContentBytes[bodyStart:]), boundary)

	// Find the HTML part with specific Content-ID
	var htmlPart []byte
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading MIME part: %w", err)
		}
		fmt.Printf("DEBUG: Part Content-Type: %s, Content-ID: %s\n", part.Header.Get("Content-Type"), part.Header.Get("Content-ID"))  // Debug: log each part's headers
		if part.Header.Get("Content-Type") == "text/html" && strings.HasPrefix(strings.Trim(part.Header.Get("Content-ID"), "<>"), "frame-") {
			// Read and decode the part if necessary
			htmlData, err := io.ReadAll(part)
			if err != nil {
				return fmt.Errorf("failed to read HTML part: %w", err)
			}
			// Decode quoted-printable if needed
			if part.Header.Get("Content-Transfer-Encoding") == "quoted-printable" {
				decoder := quotedprintable.NewReader(bytes.NewReader(htmlData))
				htmlDataDecoded, err := io.ReadAll(decoder)
				if err != nil {
					return fmt.Errorf("failed to decode quoted-printable: %w", err)
				}
				htmlData = htmlDataDecoded
			}
			htmlPart = htmlData
			break
		}
	}
	if len(htmlPart) == 0 {
		return fmt.Errorf("could not find HTML part with Content-ID starting with 'frame-' in MHTML file")
	}

	// After decoding HTML, log for debugging
	fmt.Printf("DEBUG: HTML Content: %s\n", string(htmlPart))  // Debug: log the full HTML content

	// Parse the HTML content using goquery
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(htmlPart))
	if err != nil {
		return fmt.Errorf("failed to parse HTML: %w", err)
	}
	var criteria []Criterion
	doc.Find(`div[role="button"][tabindex="0"]`).Each(func(i int, s *goquery.Selection) {
		id := ""
		idElem := s.Find("div.font-mono.text-xs")
		idText := strings.TrimSpace(idElem.Text())
		reItemID := regexp.MustCompile(`Item ID: ([a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12})`)
		idMatchItemID := reItemID.FindStringSubmatch(idText)

		if len(idMatchItemID) > 1 {
			id = idMatchItemID[1]
		} else {
			reUUID := regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
			idMatchUUID := reUUID.FindString(idText)
			if idMatchUUID != "" {
				id = idMatchUUID
			} else {
				// Fallback to searching the full text of the element
				fullText := strings.TrimSpace(s.Text())
				idMatchFullText := reUUID.FindString(fullText)
				if idMatchFullText != "" {
					id = idMatchFullText
					fmt.Printf("DEBUG: UUID found in fallback for index %d: %s\n", i, idMatchFullText)
				} else {
					fmt.Printf("DEBUG: No UUID found in criterion for index %d: %s\n", i, fullText)
				}
			}
		}

		// Extract the counter
		counterElem := s.Find("div.flex.w-10.items-center.justify-end.font-mono")
		counter := strings.TrimSpace(counterElem.Text())

		// If no ID is found after all attempts, skip this criterion
		if id == "" {
			return // continue to the next element
		}

		scoreElem := s.Find("input[type='number']")
		scoreStr := strings.TrimSpace(scoreElem.AttrOr("value", "0")) // Extract value attribute from number input
		requiredElem := s.Find("input[type='checkbox']")
		requiredStr := requiredElem.AttrOr("data-state", "unchecked") // Ensure data-state is captured correctly
		textElem := s.Find("label:contains('Criterion')").Find("textarea")
		rubricTextStr := strings.TrimSpace(textElem.Text()) // Target textarea under 'Criterion' label
		heldOutTestsElem := s.Find("textarea[aria-label*='Held-out test']")
		heldOutTests := strings.TrimSpace(heldOutTestsElem.Text()) // Target textarea with aria-label containing 'Held-out test'
		fmt.Printf("DEBUG: Criterion %d - ID: %s, Score: %s, Required: %s, Text: %s, RubricText: %s, HeldOutTests: %s\n", i, id, scoreStr, requiredStr, idText, rubricTextStr, heldOutTests)
		score, err := strconv.Atoi(scoreStr)
		if err != nil {
			score = 0
		}
		required := requiredStr == "checked"
		criteria = append(criteria, Criterion{Title: id, Score: score, Required: required, Rubric: rubricTextStr, HeldOutTest: heldOutTests, Counter: counter})
	})
	fmt.Printf("DEBUG: Number of criterion elements found: %d\n", len(doc.Find(`div[role="button"][tabindex="0"]`).Nodes))

	// Generate TASK_DATA.md content
	var mdContent strings.Builder
	mdContent.WriteString("# TASK DATA\n\n")

	for _, c := range criteria {
		mdContent.WriteString(fmt.Sprintf("### %s: %s\n\n", c.Counter, c.Title))
		mdContent.WriteString(fmt.Sprintf("**Score**: %d\n", c.Score))
		mdContent.WriteString(fmt.Sprintf("**Required**: %t\n", c.Required))
		mdContent.WriteString(fmt.Sprintf("**Criterion**:\n%s\n\n", c.Rubric))
		mdContent.WriteString(fmt.Sprintf("**Held-out tests**:\n```bash\n%s\n```\n\n\n", c.HeldOutTest))
	}

	// Write to TASK_DATA.md
	err = os.WriteFile(mdFilePath, []byte(mdContent.String()), 0644)
	if err != nil {
		return fmt.Errorf("failed to write TASK_DATA.md: %w", err)
	}

	return nil
}
