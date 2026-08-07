package git

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
)

type writeArgs struct {
	Op      string   `json:"op" enum:"add,commit,branch_create,checkout,stash" desc:"Write operation to run."`
	Paths   []string `json:"paths,omitempty" desc:"add: paths to stage."`
	Message string   `json:"message,omitempty" desc:"commit: commit message; stash push: stash message."`
	Name    string   `json:"name,omitempty" desc:"branch_create: new branch name; checkout: branch or commit to switch to."`
	StashOp string   `json:"stash_op,omitempty" enum:"push,pop" desc:"stash: push (default) or pop."`
}

const writeDescription = "Run a safe git write operation: add (stage paths), commit, branch_create, checkout (switch branch/commit), stash (push/pop). Always asks for approval. Push, pull, rebase, reset and other history-rewriting commands are not available here — use the shell tool."

func registerWriteTool(c core.NativeRegistrar, deps core.CallerDeps, root string) {
	core.Register(c, core.Tool[writeArgs]{
		Spec: core.ToolSpec{
			Name: writeToolName, Description: writeDescription,
			Example:    `{"name":"git_write","arguments":{"op":"commit","message":"fix: handle nil ref"}}`,
			Parameters: core.SchemaFor[writeArgs](),
			Metadata:   core.WriteWorkspaceAskMetadata("system", core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true}),
		},
		Prepare: func(_ context.Context, args writeArgs) (writeArgs, error) {
			_, err := writeArgv(args)
			return args, err
		},
		Approve: func(_ context.Context, args writeArgs) core.ApprovalDecision {
			argv, err := writeArgv(args)
			if err != nil {
				return core.Deny(err.Error())
			}
			display := "git " + strings.Join(argv, " ")
			return core.Ask(approval.Request{
				Confined: sandbox.Available(), Writes: true,
				Tool: writeToolName, Reason: "git_write modifies the repository", Tier: "write_workspace",
				Summary: "Run " + display, Subject: display,
				Details: fmt.Sprintf("Command: %s\nRepository: %s", display, root),
			})
		},
		Approver: deps.Approver,
		Execute: func(ctx context.Context, args writeArgs) (string, error) {
			argv, err := writeArgv(args)
			if err != nil {
				return "", err
			}
			out, err := gitTextEnv(ctx, root, commitIdentityEnv(ctx, args.Op, root), argv...)
			if err != nil {
				return flagged(writeToolName, err)
			}
			return core.MarshalResult(writeToolName, map[string]string{"output": out})
		},
	})
}

// fallbackIdentity names the author of a commit made in a repository that has
// none configured. The address is under .invalid, which is reserved and never
// resolves, so a commit is plainly attributed to the agent rather than to a
// person who did not write it.
const (
	fallbackIdentityName  = "Invokr Agent"
	fallbackIdentityEmail = "agent@invokr.invalid"
)

// commitIdentityEnv supplies an author for a commit only when the repository
// has none. Git refuses to commit without one, and the model's own way out was
// a bare `git config user.email`, which writes the *shared* repo config and so
// rewrites the identity for the main checkout and every linked worktree (#498).
// Passing it in the environment touches no file and lasts one process.
//
// Environment rather than `-c`: config keys can name binaries git will execute
// (#318), and that door stays shut even for keys we choose ourselves.
//
// A repository that already has an identity is left entirely alone.
func commitIdentityEnv(ctx context.Context, op string, root string) []string {
	if op != "commit" {
		return nil
	}
	if email, err := gitText(ctx, root, "config", "--get", "user.email"); err == nil && strings.TrimSpace(email) != "" {
		return nil
	}
	return append(os.Environ(),
		"GIT_AUTHOR_NAME="+fallbackIdentityName,
		"GIT_AUTHOR_EMAIL="+fallbackIdentityEmail,
		"GIT_COMMITTER_NAME="+fallbackIdentityName,
		"GIT_COMMITTER_EMAIL="+fallbackIdentityEmail,
	)
}

// writeArgv maps a validated op to its literal argv. Every op builds explicit
// arguments — no passthrough of arbitrary flags.
func writeArgv(args writeArgs) ([]string, error) {
	switch args.Op {
	case "add":
		if len(args.Paths) == 0 {
			return nil, fmt.Errorf("add requires paths")
		}
		return append([]string{"add", "--"}, args.Paths...), nil
	case "commit":
		if strings.TrimSpace(args.Message) == "" {
			return nil, fmt.Errorf("commit requires message")
		}
		return []string{"commit", "-m", args.Message}, nil
	case "branch_create":
		if strings.TrimSpace(args.Name) == "" {
			return nil, fmt.Errorf("branch_create requires name")
		}
		return []string{"branch", args.Name}, nil
	case "checkout":
		if strings.TrimSpace(args.Name) == "" {
			return nil, fmt.Errorf("checkout requires name")
		}
		return []string{"checkout", args.Name}, nil
	case "stash":
		if args.StashOp == "pop" {
			return []string{"stash", "pop"}, nil
		}
		if strings.TrimSpace(args.Message) != "" {
			return []string{"stash", "push", "-m", args.Message}, nil
		}
		return []string{"stash", "push"}, nil
	default:
		return nil, fmt.Errorf("unknown git_write op %q", args.Op)
	}
}
