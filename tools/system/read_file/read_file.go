package read_file

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

const (
	defaultLineLimit = 2000
	maxLineLimit     = 5000
	maxReadBytes     = 256 << 10
	// multiReadBytes keeps a multi-file read under the 4000-token spill gate
	// (constants.ToolResultGateTokens), so the files arrive in context rather
	// than as a spilled preview the model has to page (#577).
	multiReadBytes = 14000
	// Reserve space for omissions so headers, text and recovery guidance all fit.
	multiReadNoticeBytes = 1024
)

type readArgs struct {
	Paths  []string `json:"paths" desc:"Files to read, whole, each under a === path === header. Give every file you need in this one call; a single file is a list of one."`
	Offset int      `json:"offset,omitempty" desc:"1-based first line to return. Defaults to 1."`
	Limit  int      `json:"limit,omitempty" desc:"Maximum lines to return, capped at 5000."`
}

func RegisterReadFileTool(c core.NativeRegistrar, wsp workspace.Policy) {
	core.Register(c, core.Tool[readArgs]{
		Spec: core.ToolSpec{
			Name: "read", Description: "Read workspace files as text. paths is a list: put every file you need into one call rather than one call per file. A single file is a list of one. Returns verbatim content; offset/limit page through a large file and apply only when paths holds exactly one.",
			Example:    `{"name":"read","arguments":{"paths":["README.md","src/app.py","src/models.py"]}}`,
			Parameters: core.SchemaFor[readArgs](), Metadata: core.ReadWorkspaceMetadata("workspace", core.ToolCapabilities{ReadsWorkspace: true}),
		},
		Execute: func(ctx context.Context, args readArgs) (string, error) {
			switch len(args.Paths) {
			case 0:
				return "", errors.New("paths is required: a list of files to read, one entry for a single file")
			case 1:
				return runReadFile(args.Paths[0], args, wsp)
			default:
				return runReadFiles(args.Paths, wsp), nil
			}
		},
	})
}

func runReadFile(path string, args readArgs, wsp workspace.Policy) (string, error) {
	abs, err := wsp.Resolve(path)
	if err != nil {
		return "", err
	}
	if !wsp.AllowsHiddenPath(wsp.DisplayPath(abs)) {
		return "", fmt.Errorf("path %q is hidden; set workspace.allow_hidden_paths to read it", path)
	}
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		// The model guessed the name from the task text; the raw stat error
		// with the host path taught nothing and it guessed again (#572).
		return "", fmt.Errorf("path %q does not exist; run file_search for the name or list the directory with shell before reading", path)
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path %q is not a regular file", path)
	}
	if workspace.ObviousBinaryExtension(abs) {
		return "", fmt.Errorf("path %q looks like a binary file; read returns text only", path)
	}
	window, err := workspace.ReadLineWindow(abs, max(args.Offset, 1), mathutil.ClampInt(args.Limit, defaultLineLimit, maxLineLimit), maxReadBytes)
	if err != nil {
		return "", err
	}
	return window.Text + window.Marker(), nil
}

// runReadFiles reads several files whole into one result. Pi reads a codebase
// in a handful of batched shell calls where invokr spent one read_file call
// per file (#577); this is the same batch as a native read. Files that would
// push the result past multiReadBytes are named, not silently dropped.
func runReadFiles(paths []string, wsp workspace.Policy) string {
	var b strings.Builder
	var skipped []string
	for _, p := range paths {
		text, err := runReadFile(p, readArgs{}, wsp)
		if err != nil {
			text = "error: " + err.Error()
		}
		entry := fmt.Sprintf("=== %s ===\n%s\n", p, strings.TrimSuffix(text, "\n"))
		if b.Len()+len(entry) > multiReadBytes-multiReadNoticeBytes {
			skipped = append(skipped, p)
			continue
		}
		b.WriteString(entry)
	}
	if len(skipped) > 0 {
		notice := fmt.Sprintf("=== not read (would exceed one result; read them in another call): %s ===\n", strings.Join(skipped, ", "))
		if len(notice) > multiReadNoticeBytes {
			notice = fmt.Sprintf("=== not read: %d requested paths exceeded the batch budget. Paths without a header above were omitted; use the original paths list to read them individually with offset/limit. ===\n", len(skipped))
		}
		b.WriteString(notice)
	}
	return b.String()
}
