package constants

const DefaultSkillsDir = "skills"

// DefaultSkillsHub is the built-in skill hub: a GitHub repository holding
// installable skills as skills/<name>/SKILL.md directories.
const DefaultSkillsHub = "invokr-ai/skills-hub"

// DefaultHubsDir is where hub repositories are shallow-cloned, under the
// invokr home directory.
const DefaultHubsDir = "hubs"

// DefaultSetupSkills is the curated starter set the setup questionnaire
// offers to install from the default hub.
var DefaultSetupSkills = []string{
	"code-review", "commit", "debugging", "plan", "ponytail", "skill-authoring", "spike",
	"tdd", "verification-before-completion", "spec-checklist",
}
