package snapshot

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// LabelsFile is the JSON shape produced by judge-import.
//
//	{
//	  "<trace_id>": {
//	    "topics":    {"<topic_id>":    {"score": 9, "reason": "..."}, ...},
//	    "people":    {"<person_id>":   {"score": 7, "reason": "..."}, ...},
//	    "artifacts": {"<artifact_id>": {"score": 5, "reason": "..."}, ...}
//	  }
//	}
type LabelsFile map[string]TraceLabels

// TraceLabels groups labels by candidate type. Entries map ID (as string,
// to keep JSON keys stable) to a Label.
type TraceLabels struct {
	Topics    map[string]Label `json:"topics"`
	People    map[string]Label `json:"people"`
	Artifacts map[string]Label `json:"artifacts"`
}

// Label is one filled-in cell. Reason is optional ("" allowed).
type Label struct {
	Score  int    `json:"score"`
	Reason string `json:"reason,omitempty"`
}

// traceHeaderRE captures `## Trace <id>` — id may be wrapped in backticks
// (judge-prepare always emits backticks; the relaxed form is for hand-edited
// docs).
var traceHeaderRE = regexp.MustCompile("^## Trace `?([^`\\s]+)`?\\s*$")

// Section headers — judge-prepare emits these for each table. Optional trailing
// " (...)" segment carries cap counts and is ignored.
var (
	topicsHeaderRE    = regexp.MustCompile(`^###\s+Topics(\s|$)`)
	peopleHeaderRE    = regexp.MustCompile(`^###\s+People(\s|$)`)
	artifactsHeaderRE = regexp.MustCompile(`^###\s+Artifacts(\s|$)`)
	otherSectionRE    = regexp.MustCompile(`^###\s+`)
)

// section is the table the scanner is currently inside.
type section int

const (
	sectionNone section = iota
	sectionTopics
	sectionPeople
	sectionArtifacts
)

// columnLayout describes a section's table shape for parseLabelRow.
//
// `expected` is the number of parts after splitting on `|` for a well-formed
// row, including the leading and trailing empty cells produced by the bracketing
// pipes. Score and reason are always the second-to-last and last data columns.
type columnLayout struct {
	scoreIdx  int
	reasonIdx int
	expected  int
}

func (s section) layout() columnLayout {
	switch s {
	case sectionTopics:
		// | Status | ID | Sim | Date | Size | Msgs | Summary | Score | Reason |
		return columnLayout{scoreIdx: 8, reasonIdx: 9, expected: 11}
	case sectionPeople:
		// | Status | ID | Name | Circle | Bio | Score | Reason |
		return columnLayout{scoreIdx: 6, reasonIdx: 7, expected: 9}
	case sectionArtifacts:
		// | Status | ID | Sim | Type | File | Summary | Score | Reason |
		return columnLayout{scoreIdx: 7, reasonIdx: 8, expected: 10}
	}
	return columnLayout{}
}

// ParseLabelsMarkdown reads a filled labels-todo.md and returns a LabelsFile.
// Only rows with non-empty Score are emitted; "Leave Score blank for trivial
// 0/10 rows" is the convention from docs/plans/reranker-replay-eval.md.
//
// Bad rows are skipped silently — labelling is human work and the parser must
// be resilient to typos. The caller can compare the row count vs. emitted
// label count to gauge completeness.
func ParseLabelsMarkdown(r io.Reader) (LabelsFile, error) {
	out := LabelsFile{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024) // long lines: large summaries

	var (
		currentTrace string
		current      section
	)

	for sc.Scan() {
		line := sc.Text()

		if m := traceHeaderRE.FindStringSubmatch(line); m != nil {
			currentTrace = m[1]
			current = sectionNone
			if _, ok := out[currentTrace]; !ok {
				out[currentTrace] = TraceLabels{
					Topics:    map[string]Label{},
					People:    map[string]Label{},
					Artifacts: map[string]Label{},
				}
			}
			continue
		}

		if currentTrace == "" {
			continue
		}

		switch {
		case topicsHeaderRE.MatchString(line):
			current = sectionTopics
			continue
		case peopleHeaderRE.MatchString(line):
			current = sectionPeople
			continue
		case artifactsHeaderRE.MatchString(line):
			current = sectionArtifacts
			continue
		case otherSectionRE.MatchString(line):
			// Any unknown ### heading closes the current table. The scanner
			// then waits for a known section header or the next trace.
			current = sectionNone
			continue
		}

		if current == sectionNone {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		// Skip table header rows (always include "Status" + "Reason") and
		// dividers (only `-`, `|`, `:`, space).
		if strings.Contains(line, "Status") && strings.Contains(line, "Reason") {
			continue
		}
		if isTableDivider(line) {
			continue
		}

		id, label, ok := parseLabelRow(line, current.layout())
		if !ok {
			continue
		}
		switch current {
		case sectionTopics:
			out[currentTrace].Topics[id] = label
		case sectionPeople:
			out[currentTrace].People[id] = label
		case sectionArtifacts:
			out[currentTrace].Artifacts[id] = label
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	return out, nil
}

// parseLabelRow reads one body row from any of the three tables. The layout
// argument tells us which column carries Score / Reason and how many parts a
// well-formed split produces.
//
// Returns ok=false if the score cell is blank (skip rule) or malformed.
func parseLabelRow(line string, lo columnLayout) (id string, lab Label, ok bool) {
	parts := strings.Split(line, "|")
	if len(parts) < lo.expected {
		return "", Label{}, false
	}

	// If the row's long-text cell (the one immediately before Score) contained
	// an escaped `\|`, the naive split overshot. Collapse the excess back into
	// that cell so column indices line up.
	gluedIdx := lo.scoreIdx - 1
	for len(parts) > lo.expected {
		parts[gluedIdx] = parts[gluedIdx] + "|" + parts[gluedIdx+1]
		parts = append(parts[:gluedIdx+1], parts[gluedIdx+2:]...)
	}

	idStr := strings.TrimSpace(parts[2])
	scoreStr := strings.TrimSpace(parts[lo.scoreIdx])
	reasonStr := strings.TrimSpace(parts[lo.reasonIdx])

	if scoreStr == "" {
		return "", Label{}, false
	}
	score, err := strconv.Atoi(scoreStr)
	if err != nil {
		return "", Label{}, false
	}
	if score < 0 || score > 10 {
		return "", Label{}, false
	}
	if _, err := strconv.Atoi(idStr); err != nil {
		return "", Label{}, false
	}
	return idStr, Label{Score: score, Reason: reasonStr}, true
}

func isTableDivider(line string) bool {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "|") {
		return false
	}
	for _, r := range s {
		switch r {
		case '|', '-', ':', ' ':
		default:
			return false
		}
	}
	return true
}
