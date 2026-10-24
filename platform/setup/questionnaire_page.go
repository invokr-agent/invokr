package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2/quick"

	"github.com/alecthomas/chroma/v2/styles"

	"charm.land/bubbles/v2/key"
	"charm.land/huh/v2"

	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/constants"
	"github.com/rivt-ai/go-inference-router"
)

// runQuestionnairePage is the interactive questionnaire: every question on one
// page, up/down to move between them, left/right to pick a model. The model
// list follows the base URL as it is edited, and the line under it says why
// it is empty when the server cannot be asked.
func runQuestionnairePage(in io.Reader, out io.Writer, answers Answers, homeDisplay string) (QuestionnaireResult, error) {
	probe := &modelProbe{}
	fields := []huh.Field{
		huh.NewInput().Title("LLM provider base URL").
			Description("An OpenAI-compatible endpoint, e.g. http://localhost:8080/v1.").Value(&answers.ProviderBaseURL),
		huh.NewSelect[string]().Title("Default model").Inline(true).
			OptionsFunc(func() []huh.Option[string] { return probe.options(answers.ProviderBaseURL, &answers.Model) }, &answers.ProviderBaseURL).
			DescriptionFunc(func() string { return probe.status(answers.ProviderBaseURL) }, &answers.ProviderBaseURL).
			Value(&answers.Model),
		huh.NewInput().Title("Invokr home path").Description("Where config, memory, skills and the toolbox live.").Value(&homeDisplay),
		huh.NewNote().Title("Persona (system prompt)").
			Description(fmt.Sprintf("Seeded into <home>/%s; edit that file to change it. An existing one is kept.", constants.PersonaFilename)),
		huh.NewConfirm().Title("Install the starter skills?").
			Description(fmt.Sprintf("%d skills from %s (%s, …).", len(constants.DefaultSetupSkills), constants.DefaultSkillsHub,
				strings.Join(constants.DefaultSetupSkills[:min(3, len(constants.DefaultSetupSkills))], ", "))).
			Value(&answers.InstallSkills),
		huh.NewConfirm().Title("Enable built-in memory recall?").Description("Zerfoo models download on first use.").Value(&answers.MemoryEnabled),
		huh.NewInput().Title("Syntax highlighting theme").
			DescriptionFunc(func() string { return themeDescription(answers.SyntaxTheme) }, &answers.SyntaxTheme).
			Suggestions(syntaxThemes()).Validate(validSyntaxTheme).Value(&answers.SyntaxTheme),
		huh.NewConfirm().Title("Let the agent read outside the workspace?").Description("It asks before each such read.").Value(&answers.OutsideReads),
		huh.NewConfirm().Title("Reset JSON profiles?").Description("Replace shipped JSON profiles in <home>/profiles.").Value(&answers.ResetToolTemplates),
	}
	form := newForm(in, out, false, huh.NewGroup(fields...)).WithKeyMap(pageKeyMap())
	if err := runForm(form, false); err != nil {
		return QuestionnaireResult{}, err
	}
	answers.ProviderBaseURL = strings.TrimSpace(answers.ProviderBaseURL)
	return finishQuestionnaire(out, answers, homeDisplay)
}

// pageKeyMap lets up/down walk the page like tab/shift+tab. The model select
// is inline, so its choice moves with left/right and frees up/down.
func pageKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	for _, pair := range [][2]*key.Binding{{&km.Input.Next, &km.Input.Prev}, {&km.Confirm.Next, &km.Confirm.Prev},
		{&km.Note.Next, &km.Note.Prev}, {&km.Select.Next, &km.Select.Prev}} {
		*pair[0] = key.NewBinding(key.WithKeys(append(pair[0].Keys(), "down")...), key.WithHelp("↓", "next"))
		*pair[1] = key.NewBinding(key.WithKeys(append(pair[1].Keys(), "up")...), key.WithHelp("↑", "back"))
	}
	km.Input.AcceptSuggestion = key.NewBinding(key.WithKeys("ctrl+e", "right"), key.WithHelp("→", "complete"))
	km.Select.Up = key.NewBinding(key.WithKeys("ctrl+p"))
	km.Select.Down = key.NewBinding(key.WithKeys("ctrl+n"))
	km.Select.Left = key.NewBinding(key.WithKeys("h", "left"), key.WithHelp("←", "prev model"))
	km.Select.Right = key.NewBinding(key.WithKeys("l", "right"), key.WithHelp("→", "next model"))
	return km
}

// modelProbe asks a base URL for its models once and remembers the answer, so
// the select's options and its status line share one request per URL.
type modelProbe struct{ results sync.Map } // base URL → probeResult

type probeResult struct {
	models []string
	err    error
}

func (p *modelProbe) get(baseURL string) probeResult {
	baseURL = strings.TrimSpace(baseURL)
	if cached, ok := p.results.Load(baseURL); ok || baseURL == "" {
		result, _ := cached.(probeResult)
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelListTimeout)
	defer cancel()
	var result probeResult
	result.models, result.err = listModels(ctx, baseURL)
	p.results.Store(baseURL, result)
	return result
}

// options lists the URL's models, then the fallback. When the server offers
// models and none is chosen yet, the first one becomes the choice: the
// fallback is the last resort, not the suggestion.
func (p *modelProbe) options(baseURL string, chosen *string) []huh.Option[string] {
	models := p.get(baseURL).models
	if len(models) > 0 && chosen != nil && !slices.Contains(models, *chosen) {
		*chosen = models[0]
	}
	fallback := huh.NewOption("pick the server's first model at startup", keepDefaultModel)
	return append(huh.NewOptions(models...), fallback)
}

func (p *modelProbe) status(baseURL string) string {
	const fix = " Fix the URL above, or keep this and set the model later with `invokr config`."
	parsed, parseErr := url.Parse(strings.TrimSpace(baseURL))
	if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "Type a full URL above, starting with http:// or https://, to list its models."
	}
	result := p.get(baseURL)
	switch {
	case inference.IsKind(result.err, inference.KindTransport):
		// No reply was ever in flight here, so any transport failure means
		// the server could not be reached.
		return fmt.Sprintf("Can't reach the model server at %s. Is it running?", parsed.Host) + fix
	case errors.Is(result.err, context.DeadlineExceeded):
		return fmt.Sprintf("No answer from the server within %s.", modelListTimeout) + fix
	case result.err != nil:
		return clients.DescribeError(result.err) + fix
	case len(result.models) == 0:
		return "The server lists no models; invokr will use whatever it serves."
	}
	return fmt.Sprintf("%d model(s) from the server · ←/→ to choose", len(result.models))
}

// listModels is the probe behind the select; tests replace it.
var listModels = func(ctx context.Context, baseURL string) ([]string, error) {
	return clients.NewClient(baseURL).ListModels(ctx) //nolint:wrapcheck // DescribeError reads the provider error
}

// syntaxThemes is what the theme question completes from: "auto", then every
// stylesheet chroma ships.
func syntaxThemes() []string { return append([]string{SyntaxThemeAuto}, styles.Names()...) }

// validSyntaxTheme accepts "auto" or a chroma stylesheet name, case aside, so
// a typo is caught here rather than silently replaced by the default later.
func validSyntaxTheme(value string) error {
	value = strings.TrimSpace(value)
	if slices.ContainsFunc(syntaxThemes(), func(name string) bool { return strings.EqualFold(name, value) }) {
		return nil
	}
	return fmt.Errorf("unknown theme %q; type to see the names, or use auto", value)
}

// themePreviewSource is the code the theme question previews: short, but with
// a keyword, a string, a number, a comment and a call, so themes differ.
const themePreviewSource = `// greet says hi n times
func greet(n int) string {
	return strings.Repeat("hi ", n+1)
}`

// themeDescription is the theme question's hint with a highlighted preview of
// the theme the answer resolves to beside it.
func themeDescription(value string) string {
	hint := "\"auto\" follows the\nterminal background.\n\nType to complete;\n→ or ctrl+e accepts."
	name, label := previewTheme(value)
	var preview strings.Builder
	if name == "" || quick.Highlight(&preview, themePreviewSource, "go", "terminal16m", name) != nil {
		return hint + "\n\nNo theme matches yet."
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(26).Render(hint), "  ",
		label+"\n"+strings.TrimRight(preview.String(), "\n"))
}

// previewTheme resolves a partial answer the way the field would complete it:
// an exact name, else the first name it prefixes. auto previews the dark
// default the TUI picks on a dark terminal.
func previewTheme(value string) (name, label string) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.HasPrefix(SyntaxThemeAuto, value) {
		return "tokyonight-night", "auto → tokyonight-night (dark terminal)"
	}
	for _, theme := range styles.Names() {
		if lower := strings.ToLower(theme); lower == value {
			return theme, theme
		} else if name == "" && strings.HasPrefix(lower, value) {
			name, label = theme, theme+" (→ to accept)"
		}
	}
	return name, label
}
