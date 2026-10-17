package setup

import (
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

	"github.com/invokr-ai/invokr/clients"
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
// home path, memory, syntax theme — the model select is interactive-only.
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
const scriptedQuestions = 4

// runQuestionnaireHuh runs the setup questionnaire as huh forms. With
// accessible set — stdin is not a terminal, or TERM=dumb — the forms run in
// huh's accessible mode: plain line-oriented prompts read from in, so piped
// and scripted input keeps working without a duplicate plain-text
// implementation. A blank line accepts each question's default.
func runQuestionnaireHuh(in io.Reader, out io.Writer, accessible bool) (QuestionnaireResult, error) {
	answers := Answers{ProviderBaseURL: DefaultProviderBaseURL, SyntaxTheme: SyntaxThemeAuto}
	homeDisplay, memoryAnswer := defaultHomePrompt(), defaultMemoryAnswer

	var piped *byteReader
	if accessible {
		// huh gives every accessible field a fresh bufio.Scanner over the
		// shared reader, and a scanner reads far past the line it hands back —
		// the next field would see only EOF. One byte per Read keeps every
		// unconsumed byte in the shared reader.
		piped = &byteReader{r: in}
		in = piped
	}
	// Two forms, not one: the model list is fetched from the base URL, so the
	// URL has to be answered before the options exist.
	urlForm := newForm(in, out, accessible, huh.NewGroup(question(
		"Invokr setup: LLM provider base URL",
		"An OpenAI-compatible endpoint. Answers are written to config.json.",
		&answers.ProviderBaseURL, accessible,
	)))
	if err := runForm(urlForm, accessible); err != nil {
		return QuestionnaireResult{}, err
	}
	answers.ProviderBaseURL = strings.TrimSpace(answers.ProviderBaseURL)

	groups := append(modelGroup(accessible, &answers), personaGroup(accessible)...)
	groups = append(groups, skillsGroup(accessible, &answers)...)
	fields := []huh.Field{
		question("Invokr home path", "Where config, memory, skills and the toolbox live.", &homeDisplay, accessible),
		memoryQuestion(accessible, &answers.MemoryEnabled, &memoryAnswer),
		question("Syntax highlighting theme", `"auto" matches the terminal background; /syntax changes it later.`, &answers.SyntaxTheme, accessible),
	}
	if !accessible {
		fields = append(fields, huh.NewConfirm().Title("Reset JSON profiles?").
			Description("Replace shipped JSON profiles in <home>/profiles.").Value(&answers.ResetToolTemplates))
	}
	groups = append(groups, huh.NewGroup(fields...))
	if err := runForm(newForm(in, out, accessible, groups...), accessible); err != nil {
		return QuestionnaireResult{}, err
	}

	if accessible {
		answers.MemoryEnabled = parseYes(memoryAnswer)
		// huh's accessible runner swallows read errors (Form.runAccessible
		// discards every field's error and returns nil), so a truncated pipe
		// would otherwise complete with silent defaults and exit 0. Report it
		// the way the pre-huh path did, before any config is written.
		if piped.served() < scriptedQuestions {
			return QuestionnaireResult{}, fmt.Errorf("setup questionnaire ended early: %w", io.ErrUnexpectedEOF)
		}
	}
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

// skillsGroup asks whether to install the curated starter skills, in
// interactive mode only: like the model select, a scripted run skips it so
// unattended setup stays off the network.
func skillsGroup(accessible bool, answers *Answers) []*huh.Group {
	if accessible {
		return nil
	}
	answers.InstallSkills = true
	return []*huh.Group{
		huh.NewGroup(huh.NewConfirm().
			Title("Install the starter skills?").
			Description(fmt.Sprintf("From the %s hub: %s.",
				constants.DefaultSkillsHub, strings.Join(constants.DefaultSetupSkills, ", "))).
			Value(&answers.InstallSkills)),
	}
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
	const title, hint = "Enable built-in memory recall?", "Zerfoo models download on first use."
	if accessible {
		return huh.NewInput().Title(fmt.Sprintf("%s [y/N] (%s)", title, hint)).Value(answer)
	}
	return huh.NewConfirm().Title(title).Description(hint).Value(enabled)
}

func parseYes(value string) bool {
	return slices.Contains([]string{"y", "yes", "true", "1"}, strings.ToLower(strings.TrimSpace(value)))
}

// personaGroup previews the persona (base system prompt) that setup seeds
// into <home>/PERSONA.md, in interactive mode only. It is a note, not a
// question: the file is the editor, setup just shows what lands there.
// Skipping it in accessible mode keeps the scripted input shape
// (scriptedQuestions) intact — a note would otherwise consume no line but
// still render noise into pipes.
func personaGroup(accessible bool) []*huh.Group {
	if accessible {
		return nil
	}
	return []*huh.Group{
		huh.NewGroup(huh.NewNote().
			Title("Persona (system prompt)").
			Description(fmt.Sprintf(
				"%s\n\nSaved to <home>/%s — edit that file to change the prompt.\nAn existing %s is kept as-is.",
				strings.TrimSpace(defaultPersonaTemplate),
				constants.PersonaFilename, constants.PersonaFilename,
			))),
	}
}

// modelGroup is the "Default model" select, and only in interactive mode. A
// scripted (accessible) run keeps the built-in default model, which restores
// the pre-huh plain path's four-answer input shape, keeps an unattended run
// off the network (no /v1/models probe), and avoids huh v2.0.3's accessible
// Select — the field that panics on truncated input (see runForm).
// `invokr config` sets the model after the fact.
func modelGroup(accessible bool, answers *Answers) []*huh.Group {
	if accessible {
		answers.Model = keepDefaultModel
		return nil
	}
	options := modelOptions(answers.ProviderBaseURL)
	// Land the cursor on a real model when the provider advertised any; the
	// "keep the default" option is the last resort, not the first suggestion.
	answers.Model = options[0].Value
	return []*huh.Group{huh.NewGroup(huh.NewSelect[string]().
		Title("Default model").
		Description("Fetched from the provider's /v1/models.").
		Options(options...).
		Value(&answers.Model))}
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

// modelOptions lists what the provider advertises. A provider that is
// unreachable or has no /v1/models endpoint yields the single "keep the
// default" option rather than an error: setup should still complete offline.
var modelOptions = func(baseURL string) []huh.Option[string] {
	fallback := []huh.Option[string]{
		huh.NewOption("pick the server's first model at startup", keepDefaultModel),
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelListTimeout)
	defer cancel()
	models, err := clients.NewClient(baseURL).ListModels(ctx)
	if err != nil || len(models) == 0 {
		return fallback
	}
	return append(huh.NewOptions(models...), fallback...)
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
