package setup

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/invokr-ai/invokr/constants"
)

// huh v2.0.3 gives every accessible field its own bufio.Scanner over the
// shared reader, and a scanner reads far past the line it hands back. Without
// byteReader the second form of the questionnaire sees EOF instead of the
// remaining piped answers.
func TestByteReaderKeepsUnconsumedInputForLaterFields(t *testing.T) {
	in := &byteReader{r: strings.NewReader("first\nsecond\n")}

	var first, second string
	run := func(value *string) {
		t.Helper()
		form := huh.NewForm(
			huh.NewGroup(huh.NewInput().Title("Q").Value(value)),
		).WithInput(in).WithOutput(&bytes.Buffer{}).WithAccessible(true)
		if err := runForm(form, true); err != nil {
			t.Fatalf("runForm returned error: %v", err)
		}
	}

	run(&first)
	run(&second)

	if first != "first" {
		t.Fatalf("first answer = %q, want %q", first, "first")
	}
	if second != "second" {
		t.Fatalf("second answer = %q, want %q — the first field consumed input meant for the second", second, "second")
	}
}

// byteReader must hand back at most one byte per Read so nothing downstream
// can buffer ahead, and must otherwise be a transparent pass-through.
func TestByteReaderReadsOneByteAtATime(t *testing.T) {
	r := &byteReader{r: strings.NewReader("abc")}

	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if n != 1 || buf[0] != 'a' {
		t.Fatalf("Read = %d bytes %q, want 1 byte %q", n, buf[:n], "a")
	}

	if n, err := r.Read(nil); n != 0 || err != nil {
		t.Fatalf("zero-length Read = (%d, %v), want (0, nil)", n, err)
	}

	rest, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("draining reader: %v", err)
	}
	if string(rest) != "bc" {
		t.Fatalf("remaining input = %q, want %q", rest, "bc")
	}
}

// Counting served answers is what tells a truncated pipe from a complete one.
// A final answer without its newline still counts; reading past the end does
// not invent one.
func TestByteReaderCountsServedAnswers(t *testing.T) {
	for name, tc := range map[string]struct {
		input string
		want  int
	}{
		"newline terminated":           {"a\nb\n", 2},
		"final answer without newline": {"a\nb", 2},
		"empty":                        {"", 0},
		"one answer":                   {"a\n", 1},
		"blank lines count as answers": {"\n\n", 2},
	} {
		t.Run(name, func(t *testing.T) {
			r := &byteReader{r: strings.NewReader(tc.input)}
			if _, err := io.ReadAll(r); err != nil {
				t.Fatalf("draining reader: %v", err)
			}
			// Draining reads past the end; that must not change the count.
			if _, err := io.ReadAll(r); err != nil {
				t.Fatalf("second drain: %v", err)
			}
			if got := r.served(); got != tc.want {
				t.Fatalf("served() = %d, want %d for input %q", got, tc.want, tc.input)
			}
		})
	}
}

// huh's Form.runAccessible discards each field's error and always returns
// nil, so a short pipe would otherwise produce a complete default config and
// exit 0. The pre-huh path reported io.ErrUnexpectedEOF; so must this one,
// and before any config is written.
func TestScriptedQuestionnaireRejectsTruncatedInput(t *testing.T) {
	for name, input := range map[string]string{
		"empty stdin":     "",
		"one answer only": "http://llm.local:9090\n",
		"missing theme":   "http://llm.local:9090\n/tmp/does-not-matter\nyes\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			t.Setenv("INVOKR_HOME", home)

			_, err := RunQuestionnaire(strings.NewReader(input), &bytes.Buffer{})
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
			}
			if _, statErr := os.Stat(filepath.Join(home, constants.DefaultConfigFile)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("a truncated run must not write a config, stat = %v", statErr)
			}
		})
	}
}

// The pre-huh path accepted y/yes/true/1; huh's own Confirm accepts only
// y/yes/n/no and re-prompts otherwise, which in a pipe consumes the *next*
// answer as its retry. The scripted path keeps the wider vocabulary and never
// re-prompts, so a later answer is never eaten.
func TestScriptedMemoryAnswerVocabulary(t *testing.T) {
	for answer, want := range map[string]bool{
		"y": true, "yes": true, "true": true, "1": true, "YES": true,
		"n": false, "no": false, "": false, "nonsense": false,
	} {
		t.Run(answer, func(t *testing.T) {
			home := t.TempDir()
			in := strings.NewReader("http://llm.local:9090\n" + home + "\n" + answer + "\ncatppuccin-mocha\nno\n")

			result, err := RunQuestionnaire(in, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("RunQuestionnaire returned error: %v", err)
			}
			if result.MemoryEnabled != want {
				t.Fatalf("MemoryEnabled = %v for answer %q, want %v", result.MemoryEnabled, answer, want)
			}
			// The answer after the memory question must survive whatever the
			// memory answer was.
			if result.SyntaxTheme != "catppuccin-mocha" {
				t.Fatalf("SyntaxTheme = %q, want the piped value — answer %q ate the next line", result.SyntaxTheme, answer)
			}
		})
	}
}

// huh v2.0.3's accessible Select indexes its options with the parsed answer
// and panics with index out of range when the reader hits EOF right after an
// invalid line. runForm must turn that into an error so a scripted
// `invokr setup` exits cleanly instead of crashing.
func TestRunFormRecoversAccessibleSelectPanicOnTruncatedInput(t *testing.T) {
	var choice string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Pick").
				Options(huh.NewOption("only", "only")).
				Value(&choice),
		),
	).WithInput(&byteReader{r: strings.NewReader("not-a-number\n")}).
		WithOutput(&bytes.Buffer{}).
		WithAccessible(true)

	err := runForm(form, true)
	if err == nil {
		t.Fatal("expected an error from truncated input, got nil")
	}
	if !strings.Contains(err.Error(), "invalid input") {
		t.Fatalf("error = %q, want it to mention invalid input", err.Error())
	}
}

// Only the accessible Select's known panic is swallowed; anything else is a
// real bug and must keep crashing, and the interactive path is not wrapped
// at all.
func TestRecoverSelectPanicReraisesOtherPanics(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected the panic to be re-raised")
		}
	}()
	_ = recoverSelectPanic("some unrelated panic", nil)
}

// A scripted run answers five questions in a fixed order: the model question
// is interactive-only, so no /v1/models probe happens and no
// extra input line is consumed.
func TestScriptedQuestionnaireSkipsTheModelQuestion(t *testing.T) {
	orig := modelOptions
	modelOptions = func(string) []huh.Option[string] {
		t.Error("scripted setup must not probe the provider for models")
		return []huh.Option[string]{huh.NewOption("x", "x")}
	}
	t.Cleanup(func() { modelOptions = orig })

	home := t.TempDir()
	in := strings.NewReader("http://llm.local:9090\n" + home + "\nyes\ncatppuccin-mocha\nyes\n")
	var out bytes.Buffer

	result, err := RunQuestionnaire(in, &out)
	if err != nil {
		t.Fatalf("RunQuestionnaire returned error: %v", err)
	}

	if result.Model != keepDefaultModel {
		t.Fatalf("Model = %q, want the built-in default (%q)", result.Model, keepDefaultModel)
	}
	if strings.Contains(out.String(), "Default model") {
		t.Fatalf("expected no model question in scripted output, got %q", out.String())
	}
	// The five answers land on the five scripted questions.
	if result.ProviderBaseURL != "http://llm.local:9090" || result.HomeDir != home {
		t.Fatalf("answers misaligned: %+v", result)
	}
	if !result.MemoryEnabled || result.SyntaxTheme != "catppuccin-mocha" || !result.OutsideReads {
		t.Fatalf("answers misaligned: %+v", result)
	}
}

// The path this replaced wrote plain text. Anything capturing setup's output
// must not have to strip terminal escapes, and the descriptions huh drops in
// accessible mode are folded into the titles instead.
func TestScriptedQuestionnaireOutputIsPlainText(t *testing.T) {
	home := t.TempDir()
	in := strings.NewReader("http://llm.local:9090\n" + home + "\nno\nauto\nno\n")
	var out bytes.Buffer

	if _, err := RunQuestionnaire(in, &out); err != nil {
		t.Fatalf("RunQuestionnaire returned error: %v", err)
	}

	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("expected no terminal escapes in scripted output, got %q", out.String())
	}
	for _, want := range []string{
		"Invokr setup: LLM provider base URL (An OpenAI-compatible endpoint.",
		"Enable built-in memory recall? [y/N] (Zerfoo models download on first use.)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in output, got %q", want, out.String())
		}
	}
}

// TERM=dumb is huh's own signal that a terminal cannot redraw. huh.NewForm
// honors it; the questionnaire must not undo that by forcing the mode off.
// The TERM branch only decides anything when input *is* a terminal, which is
// why accessibleMode takes that as a parameter: asserting through
// wantsAccessible with a non-terminal reader would pass with the TERM check
// deleted.
func TestDumbTerminalGetsAccessibleMode(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	if accessibleMode(true) {
		t.Fatal("a capable terminal must get the interactive form")
	}

	t.Setenv("TERM", "dumb")
	if !accessibleMode(true) {
		t.Fatal("expected TERM=dumb to select accessible mode even on a terminal")
	}
}

// Non-terminal input is accessible whatever TERM says, and wantsAccessible
// classifies a plain reader as non-terminal.
func TestNonTerminalInputGetsAccessibleMode(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")

	if !accessibleMode(false) {
		t.Fatal("expected non-terminal input to select accessible mode")
	}
	if !wantsAccessible(strings.NewReader("")) {
		t.Fatal("expected piped input to select accessible mode")
	}
}
