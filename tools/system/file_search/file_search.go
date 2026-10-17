package file_search

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

const (
	defaultMaxResults   = 20
	maxResultsCap       = 100
	defaultContextLines = 2
	maxContextLinesCap  = 5
	maxFileBytes        = 5 << 20
	largeFileBytes      = 1 << 20
	policyTier          = "read_workspace"
	gitOutputLimit      = 32 << 20
)

type searchArgs struct {
	Query        string `json:"query" desc:"Filename or content query."`
	Mode         string `json:"mode,omitempty" enum:"auto,filename,literal,regex" desc:"Search mode. auto searches filename and literal content."`
	PathGlob     string `json:"path_glob,omitempty" desc:"Optional relative glob such as src/** or docs/*.md."`
	GitScope     string `json:"git_scope,omitempty" enum:"all,tracked,changed,staged,unstaged" desc:"Optional git file scope."`
	MaxResults   int    `json:"max_results,omitempty" desc:"Maximum results, capped at 100."`
	ContextLines *int   `json:"context_lines,omitempty" desc:"Snippet context lines, capped at 5."`
}

type searchResponse struct {
	Results []searchResult `json:"results"`
}

type searchResult struct {
	Path      string  `json:"path"`
	LineStart int     `json:"line_start"`
	LineEnd   int     `json:"line_end"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
	GitStatus string  `json:"git_status"`
	absPath   string
}

type candidateFile struct {
	absPath, relPath, gitStatus       string
	changed, generated, vendor, large bool
	modTime                           time.Time
	totalLines                        int
}

type matchHit struct {
	line int
	kind string
}

type searchPlan struct {
	args                      searchArgs
	contextLines              int
	regex                     *regexp.Regexp
	changedFiles, scopedPaths map[string]struct{}
	statusByPath              map[string]string
}

func RegisterFileSearchTool(c core.NativeRegistrar, deps core.CallerDeps, workspace workspace.Policy) {
	core.Register(c, core.Tool[searchArgs]{
		Spec: core.ToolSpec{
			Name: "file_search", Description: "Search workspace filenames, literal content, or regex; return line-numbered snippets. Read-only.",
			Example:    `{"name":"file_search","arguments":{"query":"HandleCall","mode":"auto","path_glob":"src/**","git_scope":"all","max_results":20,"context_lines":2}}`,
			Parameters: core.SchemaFor[searchArgs](), Metadata: core.ReadWorkspaceMetadata("workspace", core.ToolCapabilities{ReadsWorkspace: true}),
		},
		AuditFailure: func(ctx context.Context, args searchArgs, err error) {
			dispatchAudit(deps.Dispatcher, ctx, args, 0, 0, "failed", err)
		},
		Execute: func(ctx context.Context, args searchArgs) (string, error) {
			return runFileSearch(deps.Dispatcher, ctx, args, workspace)
		},
	})
}

func runFileSearch(dispatcher events.Dispatcher, ctx context.Context, args searchArgs, wsp workspace.Policy) (payload string, err error) {
	started := time.Now()
	var resultCount int
	defer func() {
		dispatchAudit(dispatcher, ctx, args, resultCount, time.Since(started), core.Outcome(err), err)
	}()

	plan, err := prepareSearchPlan(ctx, args, wsp)
	if err != nil {
		return "", err
	}
	args = plan.args

	results, err := executeSearch(ctx, plan, wsp)
	if err != nil {
		return "", err
	}
	results = results[:min(len(results), plan.args.MaxResults)]
	resultCount = len(results)

	return core.MarshalResult("file_search", searchResponse{Results: results})
}

func prepareSearchPlan(ctx context.Context, args searchArgs, wsp workspace.Policy) (searchPlan, error) {
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return searchPlan{}, fmt.Errorf("query is required")
	}
	if len(wsp.Roots()) == 0 {
		return searchPlan{}, fmt.Errorf("workspace root is not configured")
	}

	var err error
	if args.Mode, err = normalizeEnum("mode", args.Mode, "auto", "filename", "literal", "regex"); err != nil {
		return searchPlan{}, err
	}
	if args.GitScope, err = normalizeEnum("git_scope", args.GitScope, "all", "tracked", "changed", "staged", "unstaged"); err != nil {
		return searchPlan{}, err
	}
	if args.PathGlob, err = normalizePathGlob(args.PathGlob); err != nil {
		return searchPlan{}, err
	}
	args.MaxResults = mathutil.ClampInt(args.MaxResults, defaultMaxResults, maxResultsCap)
	contextLines := normalizeContextLines(args.ContextLines)
	args.ContextLines = &contextLines

	rx, err := compileSearchRegex(args.Mode, args.Query)
	if err != nil {
		return searchPlan{}, err
	}

	statuses, changed, scoped, err := resolveGitScope(ctx, wsp.Root, args.GitScope)
	if err != nil {
		return searchPlan{}, err
	}

	return searchPlan{
		args: args, contextLines: contextLines, regex: rx,
		changedFiles: changed, statusByPath: statuses, scopedPaths: scoped,
	}, nil
}

func resolveGitScope(ctx context.Context, root, gitScope string) (statuses map[string]string, changed, scoped map[string]struct{}, err error) {
	statuses, err = gitStatuses(ctx, root)
	if err != nil && gitScope != "all" {
		return nil, nil, nil, err
	}
	changed = map[string]struct{}{}
	for path, status := range statuses {
		if status != "" && status != "unmodified" {
			changed[path] = struct{}{}
		}
	}
	if gitScope != "all" {
		scoped, err = gitScopePaths(ctx, root, gitScope, statuses)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	return statuses, changed, scoped, nil
}

// normalizeEnum defaults a blank enum argument and rejects any value outside
// the allowed set, naming the field in the error.
func normalizeEnum(field string, value string, allowed ...string) (string, error) {
	value = strutil.FirstNonEmptyTrimmed(value, allowed[0])
	if !slices.Contains(allowed, value) {
		return "", fmt.Errorf("unsupported %s %q", field, value)
	}
	return value, nil
}

func normalizePathGlob(pathGlob string) (string, error) {
	pathGlob = strutil.FirstNonEmptyTrimmed(pathGlob, "**/*")
	slashed := filepath.ToSlash(pathGlob)
	if filepath.IsAbs(pathGlob) || strings.Contains(slashed, "../") || strings.HasPrefix(slashed, "..") {
		return "", fmt.Errorf("path_glob %q is outside allowed workspace roots", pathGlob)
	}
	return pathGlob, nil
}

func normalizeContextLines(value *int) int {
	contextLines := defaultContextLines
	if value != nil {
		contextLines = *value
	}
	return min(max(contextLines, 0), maxContextLinesCap)
}

func compileSearchRegex(mode string, query string) (*regexp.Regexp, error) {
	if mode != "regex" {
		return nil, nil
	}
	rx, err := regexp.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("invalid regex query: %w", err)
	}
	return rx, nil
}

func executeSearch(ctx context.Context, plan searchPlan, wsp workspace.Policy) ([]searchResult, error) {
	type rootResult struct {
		results []searchResult
		err     error
	}
	roots := wsp.Roots()
	completed := make(chan rootResult, len(roots))
	for _, root := range roots {
		go func() {
			var results []searchResult
			err := searchRoot(ctx, plan, wsp, root, &results)
			completed <- rootResult{results: results, err: err}
		}()
	}
	results := []searchResult{}
	for range roots {
		root := <-completed
		if root.err != nil {
			return nil, root.err
		}
		results = appendTopResults(results, root.results, plan.args.MaxResults)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return betterResult(results[i], results[j])
	})
	if err := populateSnippets(results); err != nil {
		return nil, err
	}
	return results, nil
}

func searchRoot(ctx context.Context, plan searchPlan, wsp workspace.Policy, root string, results *[]searchResult) error {
	relPaths, err := ListCandidateFiles(ctx, root)
	if err != nil {
		return err
	}
	for _, rel := range relPaths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		processRootCandidate(plan, wsp, root, rel, results)
	}
	return nil
}

// listCandidateFiles enumerates files under root. Inside a git work tree it
// defers to `git ls-files`, which natively honors nested .gitignore rules
// (including negation) instead of the hand-rolled matcher this replaces.
// Outside a git work tree it falls back to a plain directory walk.
// ListCandidateFiles lists workspace-relative file paths under root, preferring
// git's index over a filesystem walk. Exported for the TUI's @file completion.
func ListCandidateFiles(ctx context.Context, root string) ([]string, error) {
	if _, err := runGit(ctx, root, "rev-parse", "--show-toplevel"); err != nil {
		return walkFallbackFiles(root)
	}
	out, err := runGit(ctx, root, "ls-files", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	rels := core.MapSlice(strings.Split(strings.TrimSuffix(out, "\n"), "\n"), func(line string) string {
		return filepath.ToSlash(strings.TrimSpace(line))
	})
	rels = slices.DeleteFunc(core.DedupPreserveOrder(rels), func(rel string) bool { return rel == "" })
	return rels, nil
}

func walkFallbackFiles(root string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	return rels, err
}

func processRootCandidate(plan searchPlan, wsp workspace.Policy, root string, rel string, results *[]searchResult) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	displayRel := displayPathForWalk(wsp, root, abs)
	if !wsp.Allows(abs) || !wsp.AllowsHiddenPath(displayRel) {
		return
	}
	// The candidate is a lexical name; a symlink under it can point outside
	// the roots or into a hidden directory. Apply the policy to the resolved
	// target as read_file does (ADR 0009).
	abs, err := wsp.Resolve(abs)
	if err != nil || !wsp.AllowsHiddenPath(wsp.DisplayPath(abs)) {
		return
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() || !passFileFilters(displayRel, info.Size(), plan) {
		return
	}
	resultsForFile, err := searchCandidate(abs, displayRel, info, plan)
	if err != nil || resultsForFile == nil {
		return
	}
	*results = appendTopResults(*results, resultsForFile, plan.args.MaxResults)
}

func passFileFilters(rel string, size int64, plan searchPlan) bool {
	_, scoped := plan.scopedPaths[rel]
	return (plan.scopedPaths == nil || scoped) && workspace.MatchGlob(plan.args.PathGlob, rel) && size <= maxFileBytes
}

func displayPathForWalk(wsp workspace.Policy, root string, path string) string {
	if root == wsp.Root {
		rel := strings.TrimPrefix(path, root+string(os.PathSeparator))
		if rel != path && rel != "" {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

func appendTopResults(top []searchResult, incoming []searchResult, limit int) []searchResult {
	if limit <= 0 {
		return nil
	}
	for _, result := range incoming {
		if len(top) < limit {
			top = append(top, result)
			continue
		}
		worst := 0
		for i := 1; i < len(top); i++ {
			if betterResult(top[worst], top[i]) {
				worst = i
			}
		}
		if betterResult(result, top[worst]) {
			top[worst] = result
		}
	}
	return top
}

func betterResult(left searchResult, right searchResult) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	return left.LineStart < right.LineStart
}

func populateSnippets(results []searchResult) error {
	rangesByPath := map[string][]intRange{}
	for i := range results {
		rangesByPath[results[i].absPath] = append(rangesByPath[results[i].absPath], intRange{start: results[i].LineStart, end: results[i].LineEnd})
	}
	linesByPath := map[string]map[int]string{}
	for path, ranges := range rangesByPath {
		lines, err := readSnippetLines(path, ranges)
		if err != nil {
			return err
		}
		linesByPath[path] = lines
	}
	for i := range results {
		results[i].Snippet = snippet(linesByPath[results[i].absPath], results[i].LineStart, results[i].LineEnd)
	}
	return nil
}

type intRange struct {
	start int
	end   int
}

func readSnippetLines(path string, ranges []intRange) (map[int]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	scanner := workspace.NewFileScanner(file)
	lines := map[int]string{}
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		if !slices.ContainsFunc(ranges, func(r intRange) bool { return lineNo >= r.start && lineNo <= r.end }) {
			continue
		}
		lines[lineNo] = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func searchCandidate(path string, rel string, info os.FileInfo, plan searchPlan) ([]searchResult, error) {
	_, changed := plan.changedFiles[rel]
	candidate := candidateFile{
		absPath: path, relPath: rel, changed: changed, modTime: info.ModTime(),
		gitStatus: strutil.FirstNonEmptyTrimmed(plan.statusByPath[rel], "unmodified"),
		vendor:    strings.Contains("/"+filepath.ToSlash(rel), "/vendor/"),
		large:     info.Size() > largeFileBytes,
	}

	hits := []matchHit{}
	if strings.Contains(candidate.relPath, plan.args.Query) && (plan.args.Mode == "auto" || plan.args.Mode == "filename") {
		hits = append(hits, matchHit{line: 1, kind: "filename"})
	}
	if plan.args.Mode == "filename" || workspace.ObviousBinaryExtension(path) {
		candidate.totalLines = 1
		return candidateResults(candidate, hits, plan), nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	scanner := workspace.NewFileScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if workspace.LooksBinary(line) {
			// Err() is nil after a successful Scan, so the check below is a no-op here.
			break
		}
		if strings.Contains(line, "Code generated") || strings.Contains(line, "DO NOT EDIT") {
			candidate.generated = true
		}
		hits = append(hits, lineHits(line, lineNo, plan)...)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	candidate.totalLines = max(lineNo, 1)
	return candidateResults(candidate, hits, plan), nil
}

func candidateResults(candidate candidateFile, hits []matchHit, plan searchPlan) []searchResult {
	if len(hits) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	results := make([]searchResult, 0, len(hits))
	for _, hit := range hits {
		start := max(hit.line-plan.contextLines, 1)
		end := min(hit.line+plan.contextLines, candidate.totalLines)
		key := fmt.Sprintf("%d:%d", start, end)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		results = append(results, searchResult{
			Path: candidate.relPath, LineStart: start, LineEnd: end, Score: score(candidate, hit, plan),
			GitStatus: candidate.gitStatus, absPath: candidate.absPath,
		})
	}
	return results
}

// lineHits reports the hit one line carries, or nil when it does not match.
func lineHits(line string, lineNo int, plan searchPlan) []matchHit {
	matched, kind := strings.Contains(line, plan.args.Query), "literal"
	if plan.args.Mode == "regex" {
		matched, kind = plan.regex.MatchString(line), "regex"
	}
	if !matched {
		return nil
	}
	return []matchHit{{line: lineNo, kind: kind}}
}

func score(candidate candidateFile, hit matchHit, plan searchPlan) float64 {
	score := 0.30
	if strings.Contains(candidate.relPath, plan.args.Query) {
		score += 0.35
	}
	switch hit.kind {
	case "literal":
		score += 0.25
	case "regex":
		score += 0.20
	}
	if candidate.changed {
		score += 0.15
	}
	score += recencyBoost(candidate.modTime)
	if candidate.vendor {
		score -= 0.20
	}
	if candidate.generated {
		score -= 0.15
	}
	if candidate.large {
		score -= 0.10
	}
	return math.Round(mathutil.Clamp01(score)*100) / 100
}

// recencyBoosts are the score bonuses by maximum file age, oldest bracket last.
var recencyBoosts = [...]struct {
	maxAge time.Duration
	boost  float64
}{{24 * time.Hour, 0.10}, {7 * 24 * time.Hour, 0.07}, {30 * 24 * time.Hour, 0.04}, {90 * 24 * time.Hour, 0.02}}

func recencyBoost(modTime time.Time) float64 {
	age := time.Since(modTime)
	for _, bracket := range recencyBoosts {
		if age <= bracket.maxAge {
			return bracket.boost
		}
	}
	return 0
}

func snippet(lines map[int]string, start int, end int) string {
	var numbered []string
	for i := start; i <= end; i++ {
		numbered = append(numbered, fmt.Sprintf("%d: %s", i, lines[i]))
	}
	return strings.Join(numbered, "\n")
}

func gitStatuses(ctx context.Context, root string) (map[string]string, error) {
	if _, err := runGit(ctx, root, "rev-parse", "--show-toplevel"); err != nil {
		return nil, fmt.Errorf("git metadata unavailable: %w", err)
	}
	out, err := runGit(ctx, root, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	statuses := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" || len(line) < 4 {
			continue
		}
		code := strings.TrimSpace(line[:2])
		path := filepath.ToSlash(strings.TrimSpace(line[3:]))
		statuses[path] = statusName(code)
	}
	return statuses, nil
}

// gitScopeListArgs are the git commands that enumerate each scope's paths;
// "changed" is derived from the porcelain statuses instead.
var gitScopeListArgs = map[string][]string{
	"tracked":  {"ls-files"},
	"staged":   {"diff", "--cached", "--name-only"},
	"unstaged": {"diff", "--name-only"},
}

func gitScopePaths(ctx context.Context, root string, scope string, statuses map[string]string) (map[string]struct{}, error) {
	argv, listed := gitScopeListArgs[scope]
	if !listed && scope != "changed" {
		return nil, fmt.Errorf("unsupported git_scope %q", scope)
	}
	paths := map[string]struct{}{}
	if listed {
		out, err := runGit(ctx, root, argv...)
		if err != nil {
			return nil, err
		}
		addLines(paths, out)
	}
	for path, status := range statuses {
		if scope == "changed" || (scope == "unstaged" && status == "untracked") {
			paths[path] = struct{}{}
		}
	}
	return paths, nil
}

func runGit(ctx context.Context, root string, args ...string) (string, error) {
	res, err := core.RunProcess(ctx, core.ProcessSpec{Name: "git", Args: args, Dir: root, OutputLimit: gitOutputLimit})
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strutil.FirstNonEmptyTrimmed(res.Stderr, err.Error()))
	}
	return res.Stdout, nil
}

func addLines(paths map[string]struct{}, out string) {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		path := filepath.ToSlash(strings.TrimSpace(line))
		if path != "" {
			paths[path] = struct{}{}
		}
	}
}

// gitStatusNames maps porcelain status marks to names, in match order.
var gitStatusNames = [...][2]string{{"??", "untracked"}, {"A", "added"}, {"M", "modified"}, {"D", "deleted"}, {"R", "renamed"}}

func statusName(code string) string {
	for _, entry := range gitStatusNames {
		if strings.Contains(code, entry[0]) {
			return entry[1]
		}
	}
	return "modified"
}

func dispatchAudit(dispatcher events.Dispatcher, ctx context.Context, args searchArgs, resultCount int, duration time.Duration, outcome string, err error) {
	queryHash := ""
	if strings.TrimSpace(args.Query) != "" {
		queryHash = strutil.HashHex(args.Query)
	}
	core.AuditDetails(ctx, dispatcher, "file_search", outcome, map[string]any{
		"policy_tier": policyTier, "query_hash": queryHash, "result_count": resultCount, "mode": strutil.FirstNonEmptyTrimmed(args.Mode, "auto"),
		"git_scope": strutil.FirstNonEmptyTrimmed(args.GitScope, "all"),
		"path_glob": strutil.FirstNonEmptyTrimmed(args.PathGlob, "**/*"), "duration_ms": duration.Milliseconds(),
	}, err)
}
