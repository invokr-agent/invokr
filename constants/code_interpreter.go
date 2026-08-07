package constants

const (
	CodeInterpreterToolName = "code_interpreter"

	CodeInterpreterBackendProcess   = "process"
	CodeInterpreterBackendContainer = "container"

	CodeInterpreterDefaultTimeoutMS      = 30000
	CodeInterpreterMaxTimeoutMS          = 120000
	CodeInterpreterMaxOutputBytes        = 100000
	CodeInterpreterMaxArtifactBytes      = 25 << 20
	CodeInterpreterMemoryLimitBytes      = 512 << 20
	CodeInterpreterDefaultContainerImage = "python:3.12-slim"

	CodeInterpreterTierCodeExecution  = "code_execution"
	CodeInterpreterTierReadWorkspace  = "read_workspace"
	CodeInterpreterTierArtifactWrite  = "artifact_write"
	CodeInterpreterTierNetworkCommand = ShellTierNetwork
)
