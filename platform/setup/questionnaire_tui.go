package setup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"charm.land/huh/v2"

	"github.com/invokr-ai/invokr/capability/skills/hub"
	"github.com/invokr-ai/invokr/config"

	"github.com/invokr-ai/invokr/constants"
)

// ErrCancelled reports that the user aborted the questionnaire.
var ErrCancelled = errors.New("setup cancelled")

// modelListTimeout bounds the GET /v1/models probe. Setup should not hang on a
// provider that is unreachable or slow — an empty list just means the model is
// typed instead of picked.
const modelListTimeout = 5 * time.Second

// skillsInstallTimeout bounds the starter-skills install (one shallow hub
// clone plus local copies); on expiry setup continues with warnings.
const skillsInstallTimeout = 60 * time.Second

// keepDefaultModel is the sentinel option shown when the provider advertises no
// models, so the select always has something to choose.
const keepDefaultModel = ""

// defaultMemoryAnswer is the memory question's default in a scripted run: the
// same "no" the pre-huh plain path defaulted to.
const defaultMemoryAnswer = "no"

// scriptedQuestions is how many answers a scripted run must supply: base URL,
// home path, memory, syntax theme, outside reads — the model select is
// interactive-only.
//
// Invariant, because the truncation check compares this against
// byteReader.served(): served() counts *lines* handed to the fields, and one
// line equals one answered question only while no accessible field ever reads
// a second line. That holds today — the accessible fields are plain inputs
// with no Validate, and parseYes accepts anything — but a Validate() that
// rejects input, or an accessible Select (which recoverSelectPanic
// anticipates), re-prompts and consumes an extra line, which would make a
// complete run look truncated. Adding either means counting answers rather
// than lines.
const scriptedQuestions = 5

// runQuestionnaireHuh runs the setup questionnaire as huh forms. With
// accessible set — stdin is not a terminal, or TERM=dumb — the forms run in
// huh's accessible mode: plain line-oriented prompts read from in, so piped
// and scripted input keeps working without a duplicate plain-text
// implementation. A blank line accepts each question's default.
func runQuestionnaireHuh(in io.Reader, out io.Writer, accessible bool, baseURL string, prefill Answers, prefilled bool) (QuestionnaireResult, error) {
	answers := Answers{
		ProviderBaseURL: cmp.Or(strings.TrimSpace(prefill.ProviderBaseURL), cmp.Or(strings.TrimSpace(baseURL), DefaultProviderBaseURL)),
		Model:           strings.TrimSpace(prefill.Model),
		MemoryEnabled:   prefill.MemoryEnabled,
		OutsideReads:    prefill.OutsideReads,
		SyntaxTheme:     cmp.Or(strings.TrimSpace(prefill.SyntaxTheme), SyntaxThemeAuto),
		InstallSkills:   !prefilled,
	}
	homeDisplay, memoryAnswer, outsideAnswer := defaultHomePrompt(), boolAnswer(answers.MemoryEnabled), boolAnswer(answers.OutsideReads)

	var piped *byteReader
	if accessible {
		// huh gives every accessible field a fresh bufio.Scanner over the
		// shared reader, and a scanner reads far past the line it hands back —
		// the next field would see only EOF. One byte per Read keeps every
		// unconsumed byte in the shared reader.
		piped = &byteReader{r: in}
		in = piped
	}
	if !accessible {
		return runQuestionnairePage(in, out, answers, homeDisplay)
	}
	// Scripted runs keep two forms: the URL is read before the rest.
	urlForm := newForm(in, out, accessible, huh.NewGroup(question(
		"Invokr setup: LLM provider base URL",
		"An OpenAI-compatible endpoint. Answers are written to config.json.",
		&answers.ProviderBaseURL, accessible,
	)))
	if err := runForm(urlForm, accessible); err != nil {
		return QuestionnaireResult{}, err
	}
	answers.ProviderBaseURL = strings.TrimSpace(answers.ProviderBaseURL)

	answers.Model = cmp.Or(strings.TrimSpace(answers.Model), keepDefaultModel)
	fields := []huh.Field{
		question("Invokr home path", "Where config, memory, skills and the toolbox live.", &homeDisplay, accessible),
		memoryQuestion(accessible, &answers.MemoryEnabled, &memoryAnswer),
		question("Syntax highlighting theme", `"auto" matches the terminal background; /syntax changes it later.`, &answers.SyntaxTheme, accessible),
		yesNoQuestion(accessible, "Let the agent read outside the workspace?", "It asks before each such read; /reads changes it later.", &answers.OutsideReads, &outsideAnswer),
	}
	if err := runForm(newForm(in, out, accessible, huh.NewGroup(fields...)), accessible); err != nil {
		return QuestionnaireResult{}, err
	}

	if accessible {
		answers.MemoryEnabled = parseYes(memoryAnswer)
		answers.OutsideReads = parseYes(outsideAnswer)
		// huh's accessible runner swallows read errors (Form.runAccessible
		// discards every field's error and returns nil), so a truncated pipe
		// would otherwise complete with silent defaults and exit 0. Report it
		// the way the pre-huh path did, before any config is written.
		if piped.served() < scriptedQuestions {
			return QuestionnaireResult{}, fmt.Errorf("setup questionnaire ended early: %w", io.ErrUnexpectedEOF)
		}
	}
	return finishQuestionnaire(out, answers, homeDisplay)
}

// finishQuestionnaire writes what was answered: the config under the chosen
// home, then the starter skills when asked for.
func finishQuestionnaire(out io.Writer, answers Answers, homeDisplay string) (QuestionnaireResult, error) {
	homeDir, err := expandHomePath(homeDisplay)
	if err != nil {
		return QuestionnaireResult{}, err
	}
	answers.SyntaxTheme = normalizeSyntaxTheme(answers.SyntaxTheme)
	result, err := EnsureConfigured(homeDir, answers)
	if err != nil {
		return QuestionnaireResult{}, err
	}
	if answers.InstallSkills {
		installDefaultSkills(homeDir, out)
	}
	return QuestionnaireResult{Result: result, Answers: answers}, nil
}

// installDefaultSkills installs the curated set from the default hub. Setup
// must complete even offline, so every failure is a printed warning, not an
// error.
func installDefaultSkills(homeDir string, out io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), skillsInstallTimeout)
	defer cancel()
	svc := hub.NewService(config.AppConfig{Skills: config.SkillsConfig{
		Dir: filepath.Join(homeDir, constants.DefaultSkillsDir), HubCacheDir: filepath.Join(homeDir, constants.DefaultHubsDir),
	}})
	for _, name := range constants.DefaultSetupSkills {
		if _, err := svc.Install(ctx, name, ""); err != nil {
			_, _ = fmt.Fprintf(out, "warning: install skill %s: %v\n", name, err)
			continue
		}
		_, _ = fmt.Fprintf(out, "installed skill %s\n", name)
	}
}

// newForm wires one of the questionnaire's forms to the shared IO and mode.
func newForm(in io.Reader, out io.Writer, accessible bool, groups ...*huh.Group) *huh.Form {
	form := huh.NewForm(groups...).WithInput(in).WithOutput(out)
	if !accessible {
		// Never WithAccessible(false): huh.NewForm already enables accessible
		// mode when TERM=dumb, and passing false would undo it.
		return form
	}
	// ThemeBase leaves titles unstyled. The default theme emits truecolor
	// escapes, which would land in captured output — the path this replaced
	// wrote plain text.
	return form.WithAccessible(true).WithTheme(huh.ThemeFunc(huh.ThemeBase))
}

// boolAnswer maps an existing boolean answer to a scripted default line.
func boolAnswer(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

// question builds a text input. huh prints no descriptions in accessible
// mode, so the hint is folded into the title there rather than being lost.
func question(title, description string, value *string, accessible bool) huh.Field {
	if accessible {
		return huh.NewInput().Title(fmt.Sprintf("%s (%s)", title, description)).Value(value)
	}
	return huh.NewInput().Title(title).Description(description).Value(value)
}

// memoryQuestion asks whether to enable memory recall. Interactive runs get a
// Confirm; a scripted run gets a plain input parsed with parseYes. huh's
// Confirm accepts only y/yes/n/no and re-prompts on anything else, which in a
// pipe silently eats the *next* answer as its retry; parseYes keeps the
// pre-huh vocabulary (y/yes/true/1) and treats anything else as "no" without
// consuming another line.
func memoryQuestion(accessible bool, enabled *bool, answer *string) huh.Field {
	return yesNoQuestion(accessible, "Enable built-in memory recall?", "Zerfoo models download on first use.", enabled, answer)
}

func yesNoQuestion(accessible bool, title, hint string, enabled *bool, answer *string) huh.Field {
	if accessible {
		return huh.NewInput().Title(fmt.Sprintf("%s [y/N] (%s)", title, hint)).Value(answer)
	}
	return huh.NewConfirm().Title(title).Description(hint).Value(enabled)
}

func parseYes(value string) bool {
	return slices.Contains([]string{"y", "yes", "true", "1"}, strings.ToLower(strings.TrimSpace(value)))
}

// byteReader serves one byte per Read so line-oriented consumers never buffer
// past the line they hand back (see runQuestionnaireHuh), and counts the
// answers it has handed over. That is one read syscall per byte on a real
// pipe — fine for a questionnaire's few hundred bytes, wrong for bulk stdin.
type byteReader struct {
	r io.Reader
	// pending reports whether bytes have arrived since the last newline, so a
	// final answer without a trailing newline still counts as served.
	pending bool
	lines   int
}

func (b *byteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return b.r.Read(p) //nolint:wrapcheck // transparent pass-through
	}
	n, err := b.r.Read(p[:1])
	if n > 0 {
		if b.pending = p[0] != '\n'; !b.pending {
			b.lines++
		}
	}
	if errors.Is(err, io.EOF) && b.pending {
		// EOF closes a last answer that simply lacks its newline.
		b.pending = false
		b.lines++
	}
	return n, err //nolint:wrapcheck // transparent pass-through
}

// served reports how many complete answers the reader has handed over.
// Comparing it against the number of questions asked is what tells a
// truncated pipe apart from a complete one: huh's accessible runner reports
// neither, and unlike watching for EOF this does not depend on whether
// something happened to read one byte past the final answer.
func (b *byteReader) served() int { return b.lines }

var runForm = func(form *huh.Form, accessible bool) (err error) {
	if accessible {
		defer func() { err = recoverSelectPanic(recover(), err) }()
	}
	if runErr := form.Run(); runErr != nil {
		if errors.Is(runErr, huh.ErrUserAborted) {
			return ErrCancelled
		}
		return runErr
	}
	return nil
}

// recoverSelectPanic converts huh v2.0.3's accessible-Select panic — it
// indexes its options with the parsed answer and goes out of range when the
// reader hits EOF right after an invalid one — into an error, so a scripted
// run exits cleanly. Any other panic is re-raised: it is a real bug.
func recoverSelectPanic(recovered any, err error) error {
	if recovered == nil {
		return err
	}
	runtimeErr, ok := recovered.(runtime.Error)
	if !ok || !strings.Contains(runtimeErr.Error(), "index out of range") {
		panic(recovered)
	}
	return fmt.Errorf("setup questionnaire aborted on invalid input: %w", runtimeErr)
}

// SyntaxThemeAuto is what an empty syntax theme means: pick a stylesheet to
// match the terminal background rather than pinning one.
const SyntaxThemeAuto = "auto"

// normalizeSyntaxTheme maps the optional answer onto what the config stores.
// The name is not checked against chroma's list here — the UI corrects an
// unknown one to its default, and /syntax is where a name is picked from the
// list rather than typed.
func normalizeSyntaxTheme(value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, SyntaxThemeAuto) {
		return ""
	}
	return value
}
