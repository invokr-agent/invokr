package schedule

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"unicode"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/jobs"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/tools/core"
)

const unattendedNote = "Note: schedule creates deny-only unattended jobs. Approval-gated tools (for example shell) will be denied; list reports the denial in last_status. Use the jobs CLI for privileged jobs."
const verificationNote = "Registration does not verify cron execution. To verify setup, run the underlying command/probe once with normal approvals and list the saved job once. Keep the requested job and schedule; do not sleep, poll, or create marker jobs to wait for cron. If no run is recorded, report scheduled execution as unverified, not successful or unavailable. Investigate the scheduler only when explicitly requested."

type scheduleArgs struct {
	Action   string `json:"action" enum:"list,add,modify,remove" desc:"Operation to perform."`
	Name     string `json:"name,omitempty" desc:"Job name. Required for add/modify/remove."`
	Schedule string `json:"schedule,omitempty" desc:"5-field cron expression. Required for add; optional for modify."`
	Prompt   string `json:"prompt,omitempty" desc:"Prompt sent to the agent on each run. Required for add; optional for modify."`
}

// RegisterScheduleTool lets the agent list, add, modify, and remove cron jobs
// managed by `invokr jobs`. It cannot create or modify privileged jobs.
func RegisterScheduleTool(c core.NativeRegistrar, deps core.CallerDeps) {
	description := "Manage scheduled cron jobs that re-invoke this agent on a schedule. Actions: list, add, modify, remove. add creates a deny-only job; modify only changes deny-only jobs. modify replaces the named job's schedule and/or prompt (unset fields keep their current value). "
	// Checked once at registration so the model sees the limitation in the schema
	// instead of burning a turn discovering it (#514).
	if _, err := exec.LookPath("crontab"); err != nil {
		description += "WARNING: this host has no crontab binary: add and modify fail, remove only deletes the stored record. "
	}
	core.Register(c, core.Tool[scheduleArgs]{
		Spec: core.ToolSpec{
			Name: "schedule", Description: description + unattendedNote + " " + verificationNote,
			Example:    `{"name":"schedule","arguments":{"action":"add","name":"daily-report","schedule":"0 8 * * *","prompt":"summarize yesterday's logs"}}`,
			Parameters: core.SchemaFor[scheduleArgs](),
			Metadata:   core.NativeMetadata("system", []string{"write"}, constants.ApprovalPolicyAsk, core.ToolCapabilities{ExecutesCode: true}),
		},
		Decode: func(call *core.ToolCall) (scheduleArgs, error) {
			fields, err := core.DecodeOptionalToolArgs[map[string]json.RawMessage](call)
			if err != nil {
				return scheduleArgs{}, err
			}
			if _, present := fields["allow"]; present {
				return scheduleArgs{}, errors.New("schedule: unattended tier grants are unsupported; use the jobs CLI")
			}
			return core.DecodeOptionalToolArgs[scheduleArgs](call)
		},
		Approve: func(ctx context.Context, args scheduleArgs) core.ApprovalDecision {
			return core.Ask(approval.Request{
				Tool: "schedule", Tier: "write", Reason: "scheduling changes install/modify/remove crontab entries",
				Summary: fmt.Sprintf("%s scheduled job %q", args.Action, args.Name),
				Details: fmt.Sprintf("action: %s\nname: %s\nschedule: %s\nprompt: %s\nnew jobs: deny-only; modify: deny-only jobs only",
					args.Action, args.Name, args.Schedule, args.Prompt),
			})
		},
		Approver: deps.Approver,
		Execute:  func(ctx context.Context, args scheduleArgs) (string, error) { return execute(ctx, deps, args) },
	})
}

func execute(ctx context.Context, deps core.CallerDeps, args scheduleArgs) (string, error) {
	db, store, err := jobs.OpenStore(deps.App)
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()

	// Trimmed once here: every action below takes its fields already trimmed.
	name, schedule, prompt := strings.TrimSpace(args.Name), strings.TrimSpace(args.Schedule), strings.TrimSpace(args.Prompt)
	switch strings.ToLower(strings.TrimSpace(args.Action)) {
	case "list":
		return listJobs(ctx, store)
	case "add":
		return addJob(ctx, store, name, schedule, prompt)
	case "modify":
		return modifyJob(ctx, store, name, schedule, prompt)
	case "remove":
		return removeJob(ctx, store, name)
	default:
		return "", fmt.Errorf("schedule: unknown action %q; must be list, add, modify, or remove", args.Action)
	}
}

func listJobs(ctx context.Context, store *jobs.Store) (string, error) {
	all, err := store.List(ctx)
	if err != nil {
		return "", err
	}
	if len(all) == 0 {
		return "no scheduled jobs.", nil
	}
	var b strings.Builder
	for _, j := range all {
		status, lastRun := "unverified (no recorded run)", "-"
		if j.LastStatus != nil {
			status = strings.ReplaceAll(*j.LastStatus, "\n", " ")
		}
		if j.LastRunAt != nil {
			lastRun = j.LastRunAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		fmt.Fprintf(&b, "%s | schedule=%q | last_status=%s | last_run=%s | approval_policy=%s\n", j.Name, j.Schedule, status, lastRun, j.ApprovalPolicy)
	}
	return b.String() + verificationNote, nil
}

func validateJobName(name string) error {
	if strings.ContainsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	}) {
		return fmt.Errorf("schedule: invalid job name %q; only alphanumeric characters, hyphens, and underscores are allowed", name)
	}
	return nil
}

func addJob(ctx context.Context, store *jobs.Store, name, schedule, prompt string) (string, error) {
	if name == "" {
		return "", errors.New("schedule: name is required for add")
	}
	if err := validateJobName(name); err != nil {
		return "", err
	}
	if err := jobs.SanitizeSchedule(schedule); err != nil {
		return "", err
	}
	if prompt == "" {
		return "", errors.New("schedule: prompt is required for add")
	}
	job, err := store.Add(ctx, name, schedule, prompt, constants.ApprovalPolicyDeny)
	if err != nil {
		if errors.Is(err, jobs.ErrDuplicateName) {
			return "", fmt.Errorf("schedule: job %q already exists", name)
		}
		return "", err
	}
	warning, err := jobs.Install(schedule, job.ID)
	if err != nil {
		_ = store.Delete(context.Background(), job.ID)
		return "", err
	}
	return withInstallWarning(fmt.Sprintf("job %s added (id: %s), schedule=%q. %s", job.Name, job.ID, schedule, unattendedNote), warning), nil
}

func modifyJob(ctx context.Context, store *jobs.Store, name, schedule, prompt string) (string, error) {
	existing, err := getJob(ctx, store, "modify", name)
	if err != nil {
		return "", err
	}
	if existing.ApprovalPolicy != constants.ApprovalPolicyDeny {
		return "", errors.New("schedule: privileged jobs must be modified with the jobs CLI")
	}
	schedule, prompt = cmp.Or(schedule, existing.Schedule), cmp.Or(prompt, existing.Prompt)
	if schedule == existing.Schedule && prompt == existing.Prompt {
		return withInstallWarning(fmt.Sprintf("job %s unmodified (no changes detected), schedule=%q.", existing.Name, schedule), ""), nil
	}
	if err := jobs.SanitizeSchedule(schedule); err != nil {
		return "", err
	}

	if err := store.UpdateDenyJob(ctx, existing.ID, schedule, prompt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("schedule: job was removed or its policy changed; use the jobs CLI")
		}
		return "", err
	}
	warning, err := jobs.Install(schedule, existing.ID)
	if err != nil {
		_ = store.UpdateDenyJob(context.Background(), existing.ID, existing.Schedule, existing.Prompt)
		return "", err
	}
	return withInstallWarning(fmt.Sprintf("job %s modified (id: %s), schedule=%q. %s", existing.Name, existing.ID, schedule, unattendedNote), warning), nil
}

func withInstallWarning(message, warning string) string {
	return strings.TrimSpace(message + " " + verificationNote + " " + warning)
}

// getJob looks up a job by name for the named action, reporting a missing
// name and an unknown job with the messages the tool's callers expect.
func getJob(ctx context.Context, store *jobs.Store, action, name string) (jobs.Job, error) {
	if name = strings.TrimSpace(name); name == "" {
		return jobs.Job{}, fmt.Errorf("schedule: name is required for %s", action)
	}
	job, err := store.GetByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.Job{}, fmt.Errorf("schedule: job %q not found", name)
	}
	return job, err
}

func removeJob(ctx context.Context, store *jobs.Store, name string) (string, error) {
	job, err := getJob(ctx, store, "remove", name)
	if err != nil {
		return "", err
	}
	var warning string
	if err := jobs.Strip(job.ID); err != nil {
		warning = fmt.Sprintf(" (warning: crontab update failed: %v)", err)
	}
	if err := store.Delete(ctx, job.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("job %s removed.%s", job.Name, warning), nil
}
