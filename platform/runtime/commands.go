package runtime

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/invokr-ai/invokr/capability/agentdef"
	"github.com/invokr-ai/invokr/capability/jobs"
	"github.com/invokr-ai/invokr/capability/memory"
	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/capability/skills"
	"github.com/invokr-ai/invokr/capability/skills/hub"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/strutil"
	toolcore "github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/toolbox"

	"go.uber.org/zap"
)

// CommandService is the runtime-facing backend for interactive slash commands.
type CommandService struct {
	app        config.AppConfig
	tools      toolcore.ToolCaller
	skills     skills.Manager
	agents     agentdef.Registry
	memory     memory.Manager
	newSession ChatSessionFunc
	events     events.Dispatcher
	sessionID  string
	mu         sync.Mutex
	running    map[string]struct{}
	logger     *zap.Logger
}

// CommandBackend is consumed by cmd/agentui without importing runtime internals.
type CommandBackend interface {
	Execute(context.Context, string, []string) (string, error)
	CustomCommands(context.Context) []CustomCommand
}

// Execute runs commands which need access to runtime-owned subsystems.
func (c *CommandService) Execute(ctx context.Context, command string, args []string) (string, error) {
	switch command {
	case "agents":
		return c.renderAgents(), nil
	case "skills":
		if len(args) > 0 {
			return c.installSkill(ctx, args)
		}
		return c.renderSkills(ctx)
	case "jobs":
		return c.jobs(ctx, args)
	case "toolbox":
		return c.installTool(ctx, args)
	case "memory":
		return c.renderMemory(ctx)
	default:
		return c.executeCustom(ctx, command, args)
	}
}

func (c *CommandService) renderSkills(ctx context.Context) (string, error) {
	specs, err := c.skills.Specs(ctx)
	if err != nil {
		return "", err
	}
	if len(specs) == 0 {
		return "no skills available", nil
	}
	slices.SortFunc(specs, func(a, b skills.Spec) int { return cmp.Compare(a.Name, b.Name) })
	lines := toolcore.MapSlice(specs, func(spec skills.Spec) string {
		return fmt.Sprintf("%s — %s\n", spec.Name, strings.TrimSpace(spec.Description))
	})
	return strings.Join(lines, "") + "Load a skill with the load_skill tool.", nil
}

func (c *CommandService) installSkill(ctx context.Context, args []string) (string, error) {
	if len(args) != 2 || args[0] != "install" {
		return "", fmt.Errorf("usage: /skills [install <name>]")
	}
	name, err := hub.NewService(c.app).Install(ctx, args[1], "")
	if err != nil {
		return "", err
	}
	return "skill " + name + " installed", nil
}

// HubSkills scans the configured skill hubs (cached clones; cloning on first
// use) and marks the ones already installed. It backs the TUI's skill picker.
func (c *CommandService) HubSkills(ctx context.Context) ([]hub.HubSkill, error) {
	found, errs := hub.NewService(c.app).Skills(ctx, false)
	if len(found) == 0 && len(errs) > 0 {
		return nil, errs[0]
	}
	installed := map[string]bool{}
	if specs, err := c.skills.Specs(ctx); err == nil {
		for _, spec := range specs {
			installed[spec.Name] = true
		}
	}
	for i := range found {
		found[i].Installed = installed[found[i].Name]
	}
	return found, nil
}

// HubTools lists the toolbox hub's tools. It backs the /tools Install tab.
func (c *CommandService) HubTools(ctx context.Context) ([]toolbox.HubTool, error) {
	return toolbox.HubTools(ctx, c.app)
}

// installTool installs one hub tool with the chosen approval and asks the live toolbox to reload.
func (c *CommandService) installTool(ctx context.Context, args []string) (string, error) {
	if len(args) != 3 || args[0] != "install" {
		return "", fmt.Errorf("usage: toolbox install <name> <approval>")
	}
	found, err := toolbox.HubTools(ctx, c.app)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(found, func(t toolbox.HubTool) bool { return t.Name == args[1] })
	if i < 0 {
		return "", fmt.Errorf("tool %q not found in %s", args[1], toolbox.HubRepo)
	}
	if err := toolbox.InstallHubTool(c.app, found[i], args[2]); err != nil {
		return "", err
	}
	if c.events != nil {
		c.events.Dispatch(ctx, events.Event{Type: events.EventToolboxRefreshRequested})
	}
	return args[1] + " installed", nil
}

func (c *CommandService) renderAgents() string {
	names := c.agents.Names()
	if len(names) == 0 {
		return "no custom agents configured"
	}
	return strings.TrimSpace(strings.Join(toolcore.MapSlice(names, func(name string) string {
		def, _ := c.agents.Get(name)
		tools := "all tools"
		if def.ToolsExplicit {
			tools = strutil.FirstNonEmptyTrimmed(strings.Join(def.Tools, ", "), "no tools")
		}
		return fmt.Sprintf("%s — model: %s; tools: %s\n", name, strutil.FirstNonEmptyTrimmed(def.Model, "inherit"), tools)
	}), ""))
}

func (c *CommandService) jobs(ctx context.Context, args []string) (string, error) {
	db, store, err := jobs.OpenStore(c.app)
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	if len(args) == 0 {
		return listJobs(ctx, store)
	}
	return c.startJob(ctx, store, args)
}

func listJobs(ctx context.Context, store *jobs.Store) (string, error) {
	items, err := store.List(ctx)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "no scheduled jobs", nil
	}
	return strings.TrimSpace(strings.Join(toolcore.MapSlice(items, func(job jobs.Job) string {
		status := "never run"
		if job.LastStatus != nil {
			status = *job.LastStatus
		}
		return fmt.Sprintf("%s — %s; %s\n", job.Name, job.Schedule, status)
	}), "")), nil
}

func (c *CommandService) startJob(ctx context.Context, store *jobs.Store, args []string) (string, error) {
	if len(args) != 2 || args[0] != "run" {
		return "", fmt.Errorf("usage: /jobs [run <name>]")
	}
	job, err := store.GetByName(ctx, args[1])
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("job %q not found", args[1])
		}
		return "", err
	}
	c.mu.Lock()
	if _, active := c.running[job.Name]; active {
		c.mu.Unlock()
		return "", fmt.Errorf("job %q is already running", job.Name)
	}
	c.running[job.Name] = struct{}{}
	c.mu.Unlock()
	go c.runJob(job)
	return "job " + job.Name + " started", nil
}

func (c *CommandService) runJob(job jobs.Job) {
	defer func() { c.mu.Lock(); delete(c.running, job.Name); c.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	status := "ok"
	defer func() {
		events.Notice(context.Background(), c.events, c.sessionID, "", "job "+job.Name+" completed: "+status)
	}()
	db, store, err := jobs.OpenStore(c.app)
	if err != nil {
		status = "error: " + err.Error()
		return
	}
	defer func() { _ = db.Close() }()
	session, broker, err := c.newSession("")
	if err != nil {
		status = "error: " + err.Error()
		jobs.RecordStatus(store, job.ID, status, c.logger)
		return
	}
	// Job sessions are short-lived; release their bus handlers (#324).
	if closer, ok := session.(interface{ Close() }); ok {
		defer closer.Close()
	}
	// FailOnToolError stays off here on purpose: the slash command reports
	// completion, not exit status, so a run with tool failures is still "ok"
	// — unlike `invokr jobs run --fail-on-tool-error`, which cron reads.
	if err := jobs.Run(ctx, session, broker, store, job, jobs.RunOptions{Logger: c.logger}); err != nil {
		status = "error: " + err.Error()
	}
}

func (c *CommandService) renderMemory(ctx context.Context) (string, error) {
	records, err := c.memory.List(ctx, core.Query{Scopes: []config.MemoryScope{config.ScopeUser, config.ScopeProject}, Limit: 10})
	if err != nil {
		return "", err
	}
	status := "memory enabled; zerfoo disabled"
	if c.app.Memory.Zerfoo.Enabled {
		status = "memory enabled; zerfoo enabled"
	}
	if len(records) == 0 {
		return status + "\nno user/project records", nil
	}
	return status + strings.Join(toolcore.MapSlice(records, func(record core.Record) string {
		return fmt.Sprintf("\n%s: %s", record.Scope, strutil.FirstNonEmptyTrimmed(record.Content, record.Summary))
	}), ""), nil
}
