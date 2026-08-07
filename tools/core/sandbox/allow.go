package sandbox

import "os"

// AllowUnsandboxedEnv is toolbox's opt-out, shared by every caller that runs
// model-supplied code: it is the single switch an operator (and
// capability/docker_sandbox's relaunch, which sets it because the container is
// already the boundary) uses to say host execution is acceptable here.
const AllowUnsandboxedEnv = "INVOKR_TOOLBOX_ALLOW_UNSANDBOXED"

// AllowUnsandboxed reports whether that opt-out is set.
func AllowUnsandboxed() bool { return os.Getenv(AllowUnsandboxedEnv) == "1" }
