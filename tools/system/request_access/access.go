package request_access

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/support/fsutil"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

const (
	toolName       = "request_access"
	MaxReasonBytes = 240
	// maxRetainedGrants bounds the store across sessions in a long-lived
	// process (one Caller serving many chat sessions): oldest first.
	maxRetainedGrants = 32
)

type accessArgs struct {
	Path   string `json:"path" desc:"Folder to reach, absolute or ~-prefixed (e.g. ~/.cache/lint). It must exist and be a directory."`
	Mode   string `json:"mode" enum:"read,write" desc:"read lets a shell line read it; write lets a shell line modify it."`
	Reason string `json:"reason" desc:"Why this folder is needed, one sentence in the user's terms. Shown to the user with the path and mode."`
	Scope  string `json:"scope" enum:"once,session" desc:"once applies to the next shell call only; session keeps it for the rest of the session. A grant is never written to config."`
}

// accessResult reports the granted directory, its mode, its scope and one
// line of guidance for the model.
type accessResult struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Scope  string `json:"scope"`
	Detail string `json:"detail"`
}

// preparedAccess is the request after resolution: path is canonical, the
// flags say what kind of grant to record.
type preparedAccess struct {
	access      accessArgs
	sessionID   string
	path        string
	write, once bool
}

// grantRecord is one directory this session was allowed to reach. It lives in
// memory only: nothing here is a config key or a standing rule.
type grantRecord struct {
	sessionID   string
	path        string
	write, once bool
}

// grantStore holds every session's records, filtered by session ID on read.
type grantStore struct {
	mu      sync.Mutex
	records []grantRecord
}

// granted is process-wide, so one Caller serving many sessions cannot leak a
// grant across them; maxRetainedGrants bounds what it can hold.
var granted = &grantStore{}

func (s *grantStore) add(record grantRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	if over := len(s.records) - maxRetainedGrants; over > 0 {
		s.records = slices.Delete(s.records, 0, over)
	}
}

// RootsFor names the directories this session may mount in a sandbox, split by
// mode: writable grants go to the sandbox's read-write list, read grants to
// its read-only one.
func (s *grantStore) rootsFor(sessionID string) (rw, ro []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.sessionID != sessionID {
			continue
		}
		if record.write {
			rw = append(rw, record.path)
			continue
		}
		ro = append(ro, record.path)
	}
	return rw, ro
}

// ConsumeOnce drops this session's once grants, which is what makes the next
// shell call the only one a once grant covers.
func (s *grantStore) consumeOnce(sessionID string) {
	s.mu.Lock()
	s.records = slices.DeleteFunc(s.records, func(record grantRecord) bool {
		return record.once && record.sessionID == sessionID
	})
	s.mu.Unlock()
}

// RequestAccessRoots is the shell's view of this session's grants: the
// directories to mount read-write and the ones to mount read-only.
func RequestAccessRoots(sessionID string) (rw, ro []string) {
	return granted.rootsFor(sessionID)
}

// ExpireOnceGrants drops the grants that only the call which just ran may
// cover, so a once grant never reaches a second shell call.
func ExpireOnceGrants(sessionID string) {
	granted.consumeOnce(sessionID)
}

// bannedNames are directory names no grant may reach at any depth: credential
// trees, keyrings and Invokr's own state. The name is matched wherever it sits,
// so ~/.gnupg, ~/.local/share/gnupg and /usr/share/keyrings are one refusal,
// and a credential tree reached through a bind or a renamed parent is still
// refused.
var bannedNames = []string{".ssh", ".gnupg", "gnupg", ".aws", ".pki", "keyrings", constants.InvokrHomeDirName}

// bannedPairs are trees named by parent and child: gh keeps its credentials in
// one directory under a configuration directory, which on its own is a
// legitimate place to ask for.
var bannedPairs = []string{".config/gh"}

// under reports whether path is at or below root.
func under(path, root string) bool {
	_, ok := fsutil.RelInside(root, path)
	return ok
}

// bannedTree names the banned tree a path's components hold, or "" when they
// hold none. Matching whole components keeps ~/.sshfs, a name that is not a
// credential tree, from being refused for sharing a prefix.
func bannedTree(path string) string {
	components := strings.Split(filepath.ToSlash(path), "/")
	for _, banned := range slices.Concat(bannedNames, bannedPairs) {
		needle := strings.Split(banned, "/")
		for i := 0; i+len(needle) <= len(components); i++ {
			if slices.Equal(components[i:i+len(needle)], needle) {
				return banned
			}
		}
	}
	return ""
}

// refused reports a path that may never be opened to the agent. It runs on the
// canonical path, so a symlink into a refused tree is caught by its target
// rather than by the spelling the model chose.
func refused(path string) error {
	if path == "/" {
		return errors.New("the filesystem root cannot be opened to the agent")
	}
	home, _ := os.UserHomeDir()
	home = filepath.Clean(home)
	if home != "" && path == home {
		return errors.New("the home directory itself cannot be opened to the agent; ask for the one directory inside it that you need")
	}
	ownDirs := []string{config.InvokrHomeDir(), filepath.Dir(config.DefaultConfigPath())}
	for _, dir := range ownDirs {
		dir = filepath.Clean(dir)
		if dir != "" && under(path, dir) {
			return fmt.Errorf("path is inside Invokr's own configuration directory %s, which cannot be opened to the agent", dir)
		}
	}
	if banned := bannedTree(path); banned != "" {
		return fmt.Errorf("path holds %s, a tree of credentials or agent state that cannot be opened to the agent", banned)
	}
	return nil
}

// expandHome turns ~ and ~/ into the user's home. A ~user name is left alone,
// which leaves the path relative and the caller refuses it.
func expandHome(path string) string {
	home, _ := os.UserHomeDir()
	if home == "" || !strings.HasPrefix(path, "~") {
		return ""
	}
	rest := path[1:]
	if rest != "" && !strings.HasPrefix(rest, "/") {
		return ""
	}
	return filepath.Clean(filepath.Join(home, rest))
}

// resolveAccess turns the model's path into one canonical directory and runs
// every refusal on that result: symlinks are resolved before the checks, so a
// link into a refused tree is caught by its target rather than by the spelling
// the model chose.
func resolveAccess(path string, wsp workspace.Policy) (string, error) {
	candidate := expandHome(path)
	if candidate == "" {
		candidate = filepath.Clean(path)
	}
	if !filepath.IsAbs(candidate) {
		return "", errors.New("path must be absolute or start with ~; a path inside the workspace is already reachable")
	}
	resolved, err := workspace.ResolveSymlinkPrefix(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	if err := refused(resolved); err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("path %q does not exist: %v", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path %q is not a directory; request_access grants a folder", path)
	}
	for _, root := range wsp.WritableRoots() {
		if under(resolved, root) {
			return "", fmt.Errorf("path %q is inside the writable root %s, which is already writable", path, root)
		}
	}
	if wsp.IsProtected(resolved) {
		return "", errors.New("path is Invokr's own configuration or a repository control directory; it cannot be opened to the agent")
	}
	return resolved, nil
}

func decodeAccess(call *core.ToolCall) (preparedAccess, error) {
	args, err := core.DecodeOptionalToolArgs[accessArgs](call)
	if err != nil {
		return preparedAccess{}, fmt.Errorf("parse request_access arguments: %w", err)
	}
	args.Path = strings.TrimSpace(args.Path)
	args.Mode = strings.ToLower(strings.TrimSpace(args.Mode))
	args.Reason = strings.TrimSpace(args.Reason)
	args.Scope = cmp.Or(strings.ToLower(strings.TrimSpace(args.Scope)), "session")
	switch args.Mode {
	case "read", "write":
	default:
		return preparedAccess{}, fmt.Errorf("mode must be read or write (got %q)", args.Mode)
	}
	switch args.Scope {
	case "once", "session":
	default:
		return preparedAccess{}, fmt.Errorf("scope must be once or session (got %q)", args.Scope)
	}
	if args.Path == "" {
		return preparedAccess{}, errors.New("path is required: the folder to reach, absolute or ~-prefixed")
	}
	if args.Reason == "" {
		return preparedAccess{}, errors.New("reason is required: one sentence saying what the folder is needed for")
	}
	args.Reason = core.TruncateRunes(args.Reason, MaxReasonBytes)
	return preparedAccess{access: args, once: args.Scope == "once", write: args.Mode == "write"}, nil
}

func prepareAccess(ctx context.Context, p preparedAccess, wsp workspace.Policy) (preparedAccess, error) {
	resolved, err := resolveAccess(p.access.Path, wsp)
	if err != nil {
		return preparedAccess{}, err
	}
	p.path = resolved
	p.sessionID = core.SessionIDFromContext(ctx)
	return p, nil
}

// auditAccess logs one decision with the facts the user saw, so a grant or a
// denial can be replayed after the session ends.
func auditAccess(ctx context.Context, dispatcher events.Dispatcher, p preparedAccess, outcome string, err error) {
	core.DispatchAudit(ctx, dispatcher, &events.AuditPayload{
		Action: toolName, Tool: toolName, Outcome: outcome, Paths: []string{filepath.ToSlash(p.path)},
		Details: map[string]any{"mode": p.access.Mode, "scope": p.access.Scope, "reason": p.access.Reason},
	}, err)
}

// executeAccess asks the user, records the grant this session may use, and logs
// whichever decision came back. Denial is the default: no approver, a cancelled
// call and a denied one all leave nothing granted.
func executeAccess(ctx context.Context, dispatcher events.Dispatcher, p preparedAccess) (string, error) {
	req := approval.Request{
		Tool:     toolName,
		Tier:     "outside_access",
		Summary:  "Grant " + p.access.Mode + " access to " + filepath.ToSlash(p.path),
		Subject:  p.path,
		Reason:   p.access.Reason,
		Details:  "Path: " + filepath.ToSlash(p.path) + "\nMode: " + p.access.Mode + "\nScope: " + p.access.Scope + "\nReason: " + p.access.Reason,
		Paths:    []string{p.path},
		Outside:  true,
		ReadOnly: !p.write,
		Writes:   p.write,
	}
	handled, err := approval.Authorize(ctx, nil, req, true)
	if !handled {
		return "", errors.New("request_access needs an approver; this session has none, so nothing was granted")
	}
	if err != nil {
		auditAccess(ctx, dispatcher, p, "denied", err)
		return "", err
	}
	granted.add(grantRecord{sessionID: p.sessionID, path: p.path, write: p.write, once: p.once})
	auditAccess(ctx, dispatcher, p, "granted", nil)
	detail := "granted for the rest of this session"
	if p.once {
		detail = "granted for the next shell call only"
	}
	return core.MarshalResult(toolName, accessResult{
		Path: filepath.ToSlash(p.path), Mode: p.access.Mode, Scope: p.access.Scope, Detail: detail,
	})
}

func RegisterRequestAccessTool(c core.NativeRegistrar, deps core.CallerDeps, wsp workspace.Policy) {
	core.Register(c, core.Tool[preparedAccess]{
		Spec: core.ToolSpec{
			Name:        toolName,
			Description: "Ask the user for temporary access to one folder outside the workspace, so a lint cache, a sibling checkout or a generated cache can be read or written without widening workspace.extra_roots. path is absolute or ~-prefixed and must name an existing directory. mode read or write says what is needed there; scope once covers only the next shell call, scope session covers the rest of it. A grant lives in memory for this session only and is never written to config: the user approves every request, and only the shell sandbox sees granted folders.",
			Example:     `{"name":"request_access","arguments":{"path":"~/.cache/lint","mode":"write","reason":"the lint cache the workspace build writes to","scope":"session"}}`,
			Parameters:  core.SchemaFor[accessArgs](),
			Metadata:    core.NativeMetadata("system", []string{"outside_access"}, constants.ApprovalPolicyAlwaysAsk, core.ToolCapabilities{ReadsWorkspace: true}),
		},
		Decode: decodeAccess,
		Prepare: func(ctx context.Context, p preparedAccess) (preparedAccess, error) {
			return prepareAccess(ctx, p, wsp)
		},
		Execute: func(ctx context.Context, p preparedAccess) (string, error) {
			return executeAccess(ctx, deps.Dispatcher, p)
		},
	})
}
