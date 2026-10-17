package docker_sandbox

var (
	BuildDockerArgs       = buildDockerArgs // func(engine, image, workspaceRoot, invokrHome string, agentArgs []string, isTTY, selinux bool, baseURL string) []string
	FilterEnv             = filterEnv
	RawArgRequiresSandbox = rawArgRequiresSandbox
	RawArgDisablesSandbox = rawArgDisablesSandbox
	HostLoopbackArgs      = hostLoopbackArgs
)
