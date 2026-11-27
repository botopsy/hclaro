package scanners

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Issue is one parsed finding before redaction. Only the fields below are decoded; code
// snippets, resource values and links in the scanner's output are never read.
type Issue struct {
	Severity string // critical, high, medium, low or unrated
	Rule     string
	Message  string
	File     string
	Line     int
}

// ErrUnreadable means the output was not the document the scanner prints.
var ErrUnreadable = errors.New("scanner output is not readable")

const maxIssues = 5000

// Parse reads a scanner's JSON output. hidden counts issues past the cap.
func Parse(name string, b []byte) (issues []Issue, hidden int, err error) {
	b = bytes.TrimSpace(b)
	var all []Issue
	switch name {
	case TFLint:
		all, err = parseTFLint(b)
	case Trivy:
		all, err = parseTrivy(b)
	case Checkov:
		all, err = parseCheckov(b)
	default:
		err = ErrUnreadable
	}
	if err != nil {
		return nil, 0, ErrUnreadable
	}
	if len(all) > maxIssues {
		return all[:maxIssues], len(all) - maxIssues, nil
	}
	return all, 0, nil
}

func parseTFLint(b []byte) ([]Issue, error) {
	var doc struct {
		Issues []struct {
			Rule struct {
				Name     string `json:"name"`
				Severity string `json:"severity"`
			} `json:"rule"`
			Message string `json:"message"`
			Range   struct {
				Filename string `json:"filename"`
				Start    struct {
					Line int `json:"line"`
				} `json:"start"`
			} `json:"range"`
		} `json:"issues"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(b, &doc); err != nil || doc.Issues == nil {
		return nil, ErrUnreadable
	}
	if len(doc.Errors) > 0 {
		// tflint could not check everything; a partial result must not read as clean.
		return nil, ErrUnreadable
	}
	out := make([]Issue, 0, len(doc.Issues))
	for _, i := range doc.Issues {
		sev := map[string]string{"error": "high", "warning": "medium", "notice": "low"}[strings.ToLower(i.Rule.Severity)]
		out = append(out, Issue{normalize(sev), i.Rule.Name, i.Message, i.Range.Filename, i.Range.Start.Line})
	}
	return out, nil
}

func parseTrivy(b []byte) ([]Issue, error) {
	var doc struct {
		SchemaVersion int `json:"SchemaVersion"`
		Results       *[]struct {
			Target            string `json:"Target"`
			Misconfigurations []struct {
				ID            string `json:"ID"`
				Title         string `json:"Title"`
				Message       string `json:"Message"`
				Severity      string `json:"Severity"`
				CauseMetadata struct {
					StartLine int `json:"StartLine"`
				} `json:"CauseMetadata"`
			} `json:"Misconfigurations"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, ErrUnreadable
	}
	out := []Issue{}
	if doc.Results == nil {
		// Trivy omits Results when nothing was found, but still prints SchemaVersion.
		if doc.SchemaVersion == 0 {
			return nil, ErrUnreadable
		}
		return out, nil
	}
	for _, r := range *doc.Results {
		for _, m := range r.Misconfigurations {
			msg := m.Title
			if msg == "" {
				msg = m.Message
			}
			out = append(out, Issue{normalize(strings.ToLower(m.Severity)), m.ID, msg, r.Target, m.CauseMetadata.StartLine})
		}
	}
	return out, nil
}

type checkovReport struct {
	// Results is a pointer so a document without it is not mistaken for a clean report.
	Results *struct {
		Failed []struct {
			ID       string  `json:"check_id"`
			Name     string  `json:"check_name"`
			File     string  `json:"file_path"`
			Lines    []int   `json:"file_line_range"`
			Severity *string `json:"severity"`
		} `json:"failed_checks"`
	} `json:"results"`
}

func parseCheckov(b []byte) ([]Issue, error) {
	var reports []checkovReport
	if len(b) > 0 && b[0] == '[' {
		if json.Unmarshal(b, &reports) != nil {
			return nil, ErrUnreadable
		}
	} else {
		var one checkovReport
		if json.Unmarshal(b, &one) != nil {
			return nil, ErrUnreadable
		}
		reports = []checkovReport{one}
	}
	out := []Issue{}
	for _, r := range reports {
		if r.Results == nil {
			return nil, ErrUnreadable
		}
		for _, f := range r.Results.Failed {
			sev := ""
			if f.Severity != nil {
				sev = strings.ToLower(*f.Severity)
			}
			line := 0
			if len(f.Lines) > 0 {
				line = f.Lines[0]
			}
			out = append(out, Issue{normalize(sev), f.ID, f.Name, strings.TrimPrefix(f.File, "/"), line})
		}
	}
	return out, nil
}

// normalize keeps one severity scale for every scanner. Open-source Checkov rules carry no
// severity, so they read "unrated" rather than a guess.
func normalize(s string) string {
	switch s {
	case "critical", "high", "medium", "low":
		return s
	}
	return "unrated"
}
