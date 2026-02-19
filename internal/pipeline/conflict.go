package pipeline

import (
	"bytes"
	"strings"
)

type ConflictRegion struct {
	FilePath  string
	StartLine int
	EndLine   int
	Ours      string
	Base      string
	Theirs    string
}

// ParseConflicts extracts conflict regions from conflict-marked content.
func ParseConflicts(filePath string, content []byte) []ConflictRegion {
	lines := bytes.Split(content, []byte("\n"))
	var regions []ConflictRegion
	inConflict := false
	section := ""
	startLine := 0
	ours := []string{}
	base := []string{}
	theirs := []string{}

	flush := func(endLine int) {
		if !inConflict || startLine == 0 {
			return
		}
		regions = append(regions, ConflictRegion{
			FilePath:  filePath,
			StartLine: startLine,
			EndLine:   endLine,
			Ours:      strings.Join(ours, "\n"),
			Base:      strings.Join(base, "\n"),
			Theirs:    strings.Join(theirs, "\n"),
		})
		inConflict = false
		section = ""
		startLine = 0
		ours = []string{}
		base = []string{}
		theirs = []string{}
	}

	for idx, line := range lines {
		switch {
		case bytes.HasPrefix(line, []byte("<<<<<<<")):
			if inConflict {
				flush(idx)
			}
			inConflict = true
			startLine = idx + 1
			section = "ours"
			continue
		case !inConflict:
			continue
		case bytes.HasPrefix(line, []byte("|||||||")):
			section = "base"
			continue
		case bytes.HasPrefix(line, []byte("=======")):
			section = "theirs"
			continue
		case bytes.HasPrefix(line, []byte(">>>>>>>")):
			flush(idx + 1)
			continue
		case section == "ours":
			ours = append(ours, string(line))
		case section == "base":
			base = append(base, string(line))
		case section == "theirs":
			theirs = append(theirs, string(line))
		}
	}
	if inConflict {
		flush(len(lines))
	}
	return regions
}

// CountConflictLines counts conflict marker lines including marker lines.
func CountConflictLines(regions []ConflictRegion) int {
	count := 0
	for _, r := range regions {
		if r.StartLine <= 0 || r.EndLine < r.StartLine {
			continue
		}
		count += r.EndLine - r.StartLine + 1
	}
	return count
}

// HasConflictMarkers returns true when unresolved conflict markers are present.
func HasConflictMarkers(content []byte) bool {
	return bytes.Contains(content, []byte("<<<<<<<"))
}
