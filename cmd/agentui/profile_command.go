package agentui

import (
	"fmt"
	"strings"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/turn"
)

// promptSwitcher is the session capability /profile needs to swap the base
// system prompt mid-session. *agent.Session satisfies it.
type promptSwitcher interface {
	SetSystemPrompt(string)
}

func profileCommandText(session turn.Session, args []string) string {
	home := config.InvokrHomeDir()
	switch len(args) {
	case 0:
		return renderProfileList(home)
	case 1:
		profile, err := config.LoadProfile(home, args[0])
		if err != nil {
			return "error: " + err.Error()
		}
		return applyProfileToSession(session, profile)
	default:
		return "usage: /profile [name]"
	}
}

func renderProfileList(home string) string {
	names, err := config.ListProfiles(home)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(names) == 0 {
		return fmt.Sprintf("no profiles in %s; create <name>.md or <name>.json there", config.ProfilesDir(home))
	}
	return "profiles: " + strings.Join(names, ", ") + " — /profile <name> switches model and prompt from the next turn"
}

// applyProfileToSession applies what a live session can change (model, system
// prompt) and names what it cannot: provider and sampling are baked into the
// gateway at bootstrap, so those overrides need a restart with --profile.
func applyProfileToSession(session turn.Session, profile config.Profile) string {
	var applied, deferred []string
	if profile.Model != nil {
		switchModelOnSession(session, *profile.Model)
		applied = append(applied, "model "+*profile.Model)
	}
	if profile.SystemPrompt != "" {
		if sw, ok := session.(promptSwitcher); ok {
			sw.SetSystemPrompt(profile.SystemPrompt)
			applied = append(applied, "system prompt")
		} else {
			deferred = append(deferred, "system prompt")
		}
	}
	if profile.Provider != nil {
		deferred = append(deferred, "provider")
	}
	if profile.Temperature != nil {
		deferred = append(deferred, "temperature")
	}
	if profile.Tools != nil && profile.Tools.Allowlist != nil {
		deferred = append(deferred, "tools")
	}
	if profile.Mode != nil {
		deferred = append(deferred, "mode")
	}
	out := "profile " + profile.Name + ": nothing to apply live"
	if len(applied) > 0 {
		out = "profile " + profile.Name + ": " + strings.Join(applied, ", ") + " (applies from next turn)"
	}
	if len(deferred) > 0 {
		out += fmt.Sprintf("; %s need a restart: invokr --profile %s", strings.Join(deferred, ", "), profile.Name)
	}
	return out
}
