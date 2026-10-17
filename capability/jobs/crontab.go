package jobs

import (
	"errors"
	"fmt"
	"strings"
)

// MarkerComment returns the crontab marker for a job ID: "# invokr-job:<id>".
// Source of truth for both generator and parsers below.
func MarkerComment(id string) string { return fmt.Sprintf("# invokr-job:%s", id) }

// AddCrontabEntry appends a two-line block:
//
//	# invokr-job:<id>
//	<schedule> <binaryPath> jobs run <id>
//
// If the marker already exists, returns content unchanged (idempotent).
// Ensures existing content ends with "\n" before appending; empty content gets
// no leading newline. Output ends with "\n".
func AddCrontabEntry(content, schedule, binaryPath, id string) (string, error) {
	for _, required := range []struct{ value, name string }{{schedule, "schedule"}, {binaryPath, "binaryPath"}, {id, "id"}} {
		if strings.TrimSpace(required.value) == "" {
			return "", errors.New(required.name + " must not be empty")
		}
	}

	marker := MarkerComment(id)
	if strings.Contains(content, marker) {
		return content, nil
	}

	quotedPath := "'" + strings.ReplaceAll(binaryPath, "'", "'\"'\"'") + "'"
	block := fmt.Sprintf("%s\n%s %s jobs run %s\n", marker, schedule, quotedPath, id)

	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + block, nil
}

// stripBlocks is the shared helper for StripCrontabEntries and
// StripAllInvokrEntries. It removes each marker line (matched by isMarker)
// and, only if a following line exists, the line immediately after it.
// The trailing-newline shape of the input is preserved.
func stripBlocks(content string, isMarker func(line string) bool) string {
	if content == "" {
		return content
	}

	hadTrailing := strings.HasSuffix(content, "\n")
	// Split without losing the last empty element so we can detect trailing newline.
	raw := strings.Split(content, "\n")

	// If content ended with \n, Split produces a trailing empty string; remove
	// it for processing, then add back at the end if needed.
	lines := raw
	if hadTrailing {
		lines = raw[:len(raw)-1]
	}

	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if isMarker(lines[i]) {
			// Skip the marker line, and the command line after it when present.
			i++
			continue
		}
		out = append(out, lines[i])
	}

	result := strings.Join(out, "\n")
	if hadTrailing {
		result += "\n"
	}
	return result
}

// StripCrontabEntries removes each block for id: the marker line and, only if
// it exists (bounds check i+1 < len(lines)), the following line regardless of
// its content. Idempotent. Preserves the trailing-newline shape of the input.
func StripCrontabEntries(content, id string) string {
	marker := MarkerComment(id)
	return stripBlocks(content, func(line string) bool {
		return line == marker
	})
}

// StripAllInvokrEntries removes every block whose marker line has prefix
// "# invokr-job:". Uses the same skip-next-line logic as StripCrontabEntries.
func StripAllInvokrEntries(content string) string {
	const prefix = "# invokr-job:"
	return stripBlocks(content, func(line string) bool {
		return strings.HasPrefix(line, prefix)
	})
}
