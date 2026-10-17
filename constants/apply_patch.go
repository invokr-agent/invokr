package constants

const (
	ApplyPatchMaxBytes = 1 << 20

	ApplyPatchBeginMarker = "*** Begin Patch"
	ApplyPatchEndMarker   = "*** End Patch"

	ApplyPatchActionCreate, ApplyPatchActionUpdate, ApplyPatchActionDelete = "create", "update", "delete"
)

var defaultApplyPatchSensitivePathGlobs = []string{
	".env",
	".env.*",
	"*.pem",
	"*.key",
	"*.p12",
	"*.pfx",
	"**/id_*",
	"**/known_hosts",
	"**/.ssh/**",
}

func DefaultApplyPatchSensitivePathGlobs() []string {
	return append([]string(nil), defaultApplyPatchSensitivePathGlobs...)
}
