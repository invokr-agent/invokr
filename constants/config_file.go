package constants

const (
	InvokrHomeDirName, DefaultConfigFile = ".invokr", "config.json"
	// PersonaFilename is the markdown file in the Invokr home whose content,
	// when present and non-empty, becomes the agent's base system prompt —
	// identity and personality in one place (env and flags still override it).
	PersonaFilename = "PERSONA.md"
	// ProfilesDirName is the directory under the Invokr home holding markdown
	// run profiles (<name>.md: front-matter config overrides + prompt body).
	ProfilesDirName = "profiles"
)
