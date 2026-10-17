package summary

import (
	"fmt"
	"strings"
)

// titleTranscriptLimit caps how much transcript is sent for titling. The first
// couple of exchanges carry the topic; sending more just costs tokens.
const titleTranscriptLimit = 2000

// TitlePromptHeader asks for a bare label. It deliberately does not reuse
// PromptHeader: that one extracts durable facts into a JSON schema, whereas a
// title is one line of plain text.
const TitlePromptHeader = `Summarize this conversation as a short title.

Rules:
- 3 to 7 words, under 60 characters.
- Name the concrete topic or task, not the format.
- No quotes, no trailing period, no "the user asks about" preamble.
- Reply with the title only, nothing else.

Conversation:
`

// RenderTitlePrompt builds the titling prompt from a transcript. Roles and text
// are rendered plainly; the transcript is truncated to titleTranscriptLimit.
func RenderTitlePrompt(lines []string) (string, error) {
	body := strings.TrimSpace(strings.Join(lines, "\n"))
	if body == "" {
		return "", fmt.Errorf("cannot title an empty transcript")
	}
	if runes := []rune(body); len(runes) > titleTranscriptLimit {
		body = string(runes[:titleTranscriptLimit])
	}
	return TitlePromptHeader + body, nil
}

// CleanTitle strips the decorations small models habitually add — surrounding
// quotes, a "Title:" prefix, trailing punctuation, stray newlines — so the
// result is usable as a one-line label.
func CleanTitle(raw string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(raw), "\n")
	title := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(first), "Title:"))
	title = strings.Trim(title, ` "'*`)
	return strings.TrimRight(title, ".,;:")
}
