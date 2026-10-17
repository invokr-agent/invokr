package constants

const (
	// WorkspaceAgentsFilename is the repository-root instructions file (the
	// cross-tool AGENTS.md convention), read as untrusted data.
	WorkspaceAgentsFilename = "AGENTS.md"
	UserMemoryFilename      = "USER.md"
	ProjectMemoryFilename   = "MEMORY.md"

	UserMemoryTitle    = "USER PROFILE"
	ProjectMemoryTitle = "MEMORY"

	UserMemoryCharLimit    = 1375
	ProjectMemoryCharLimit = 2200

	UserMemoryPromptTitle    = "User Memory"
	ProjectMemoryPromptTitle = "Project Memory"

	UserMemoryPromptPriority    = 200
	ProjectMemoryPromptPriority = 100

	// WorkspaceInstructions* describe the repository's own AGENTS.md, read as
	// untrusted data. The priority sits between user memory (200) and project
	// memory (100) so the user's own profile survives a large repository file.
	WorkspaceInstructionsPromptTitle    = "Workspace Instructions"
	WorkspaceInstructionsPromptPriority = 150
	WorkspaceInstructionsCharLimit      = 4000

	// ProjectMemoryDirName is the subdirectory of the memory dir that holds
	// per-workspace project memory, one digest-named directory per root.
	ProjectMemoryDirName = "projects"

	// EnvironmentValueCharLimit caps each value rendered into the unfenced
	// environment section of the system prompt.
	EnvironmentValueCharLimit = 120
)
