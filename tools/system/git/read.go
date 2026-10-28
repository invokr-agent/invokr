package git

import (
	"cmp"
	"context"
	"fmt"
	"strconv"

	"github.com/invokr-agent/invokr/tools/core"
)

type readArgs struct {
	Op     string   `json:"op" enum:"status,log,diff,show,branch" desc:"Read operation to run."`
	Ref    string   `json:"ref,omitempty" desc:"Commit, branch, or range (e.g. main..HEAD) for log/diff/show."`
	Paths  []string `json:"paths,omitempty" desc:"Restrict diff/log/show to these paths. Use this to drill into files named by a spilled diff's stat list."`
	Staged bool     `json:"staged,omitempty" desc:"diff: compare the index instead of the worktree (--cached)."`
	Limit  int      `json:"limit,omitempty" desc:"log: max commits to return (default 20)."`
}

type diffResult struct {
	Stat string `json:"stat"`
	Diff string `json:"diff"`
}

const readDescription = "Inspect git: status, log, diff, show, branch (list). Use git_write for commits. diff/show return {stat, diff}; oversized diff text spills to a file. Narrow paths using the retained stat instead of paging the spill."

func registerReadTool(c core.NativeRegistrar, root string) {
	core.Register(c, core.Tool[readArgs]{
		Spec: core.ToolSpec{
			Name: readToolName, Description: readDescription,
			Example:    `{"name":"git_read","arguments":{"op":"diff","ref":"main..HEAD","paths":["README.md"]}}`,
			Parameters: core.SchemaFor[readArgs](),
			Metadata:   core.ReadWorkspaceMetadata("system", core.ToolCapabilities{ReadsWorkspace: true}),
		},
		Execute: func(ctx context.Context, args readArgs) (string, error) {
			out, err := executeRead(ctx, root, args)
			if err != nil {
				return flagged(readToolName, err)
			}
			return out, nil
		},
	})
}

func executeRead(ctx context.Context, root string, args readArgs) (string, error) {
	switch args.Op {
	case "status":
		return textResult(ctx, root, "status", "--branch", "--porcelain=v1")
	case "log":
		return textResult(ctx, root, logArgs(args)...)
	case "branch":
		return textResult(ctx, root, "branch", "--all", "--no-color")
	case "diff":
		return diffEnvelope(ctx, root, diffArgs(args))
	case "show":
		return diffEnvelope(ctx, root, append([]string{"show", "--no-color", cmp.Or(args.Ref, "HEAD")}, pathspec(args.Paths)...))
	default:
		return "", fmt.Errorf("unknown git_read op %q", args.Op)
	}
}

// git_read only reads, so it runs through gitProbe: the repository mounted
// read-only, nothing writable to seal, and so no approval prompt in Auto.
func textResult(ctx context.Context, root string, args ...string) (string, error) {
	out, err := gitProbe(ctx, root, args...)
	if err != nil {
		return "", err
	}
	return core.MarshalResult(readToolName, map[string]string{"output": out})
}

func logArgs(args readArgs) []string {
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	argv := []string{"log", "--oneline", "--no-color", "-n", strconv.Itoa(limit)}
	if args.Ref != "" {
		argv = append(argv, args.Ref)
	}
	return append(argv, pathspec(args.Paths)...)
}

func diffArgs(args readArgs) []string {
	argv := []string{"diff", "--no-color"}
	if args.Staged {
		argv = append(argv, "--cached")
	}
	if args.Ref != "" {
		argv = append(argv, args.Ref)
	}
	return append(argv, pathspec(args.Paths)...)
}

// diffEnvelope runs the diff-shaped command twice — once with --stat, once
// full — and returns {stat, diff}. The tool-result gate spills the longest
// string field of an oversize envelope, so a huge diff leaves the stat intact
// for the model while the TUI still receives the full text.
func diffEnvelope(ctx context.Context, root string, argv []string) (string, error) {
	statArgv := append([]string{argv[0], "--stat"}, argv[1:]...)
	stat, err := gitProbe(ctx, root, statArgv...)
	if err != nil {
		return "", err
	}
	diff, err := gitProbe(ctx, root, argv...)
	if err != nil {
		return "", err
	}
	return core.MarshalResult(readToolName, diffResult{Stat: stat, Diff: diff})
}

func pathspec(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	return append([]string{"--"}, paths...)
}
