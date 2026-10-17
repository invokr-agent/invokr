package cmd

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"io"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/capability/jobs"
	"github.com/invokr-ai/invokr/capability/jobs/jobrun"
	"github.com/invokr-ai/invokr/config"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// NewJobsCmd builds the `jobs` command tree. It takes *config.AppConfig so
// persistent-flag overrides applied by the root command are visible at run
// time. Jobs subcommands skip the root runtime bootstrap (see
// skipsRuntimeBootstrap): add/list/rm/purge only need the DB, and `jobs run`
// bootstraps its own runtime inside jobs.RunJob.
func NewJobsCmd(app *config.AppConfig) *cobra.Command {
	root := &cobra.Command{
		Use:     "jobs",
		Short:   "Manage scheduled jobs run via the OS crontab",
		Long:    "A job is a prompt plus a cron schedule. Registering one writes an\nentry to your user crontab that calls `invokr jobs run` at that time,\nso jobs fire whether or not you are logged in — and, like any\nunattended run, with nobody to answer tool approvals.\n\nRun bare, this lists what is registered.",
		Example: "  invokr jobs\n  invokr jobs add nightly --schedule \"0 3 * * *\" --prompt \"triage new issues\"\n  invokr jobs run nightly     # execute it now, as cron would\n  invokr jobs rm nightly",
		GroupID: groupInspect,
	}
	useDefaultSubcommand(root, "list", newJobsAddCmd(app), newJobsListCmd(app), newJobsRmCmd(app), newJobsPurgeCmd(app), newJobsRunCmd(app))
	return root
}

// withJobsStore opens the jobs DB, hands the store to fn, and closes the DB
// afterwards — the preamble every jobs subcommand shares.
func withJobsStore(cmd *cobra.Command, app *config.AppConfig, fn func(context.Context, *jobs.Store) error) error {
	db, store, err := jobs.OpenStore(*app)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return fn(cmd.Context(), store)
}

func newJobsAddCmd(app *config.AppConfig) *cobra.Command {
	var schedule, prompt string
	var allowAll bool
	var allowTiers []string
	cmd := &cobra.Command{
		Use:     "add <name>",
		Short:   "Register a job and install its crontab entry",
		Long:    "Tool approvals are denied by default in a scheduled run, since nobody\nis there to answer them; --allow opts in per tier, --allow-all to everything.",
		Example: "  invokr jobs add nightly --schedule \"0 3 * * *\" --prompt \"triage new issues\" --allow read-only",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJobsAdd(cmd, app, args[0], schedule, prompt, allowAll, allowTiers)
		},
	}
	cmd.Flags().StringVar(&schedule, "schedule", "", "When to run, as a 5-field cron expression, e.g. \"0 3 * * *\" (required)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt sent to the agent on every run (required)")
	// Named to match exec's --allow/--allow-all: one concept, one spelling.
	cmd.Flags().StringSliceVar(&allowTiers, "allow", nil, "Auto-approve one tool tier on every run, repeatable; \"read-only\" covers the read-only workspace tiers")
	cmd.Flags().BoolVar(&allowAll, "allow-all", false, "Auto-approve every tool call on every run — unattended arbitrary code execution")
	_ = cmd.MarkFlagRequired("schedule")
	_ = cmd.MarkFlagRequired("prompt")
	return cmd
}

func runJobsAdd(cmd *cobra.Command, app *config.AppConfig, name, schedule, prompt string, approveAll bool, allowTiers []string) error {
	if err := jobs.SanitizeSchedule(schedule); err != nil {
		return err
	}
	policy, err := approval.EncodePolicy(approveAll, allowTiers)
	if err != nil {
		return err
	}
	if approveAll {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: --allow-all enables unattended arbitrary tool execution")
	}
	return withJobsStore(cmd, app, func(ctx context.Context, store *jobs.Store) error {
		job, err := store.Add(ctx, name, schedule, prompt, policy)
		if err != nil {
			if errors.Is(err, jobs.ErrDuplicateName) {
				return fmt.Errorf("job %q already exists", name)
			}
			return err
		}
		warning, err := jobs.Install(schedule, job.ID)
		if err != nil {
			_ = store.Delete(context.Background(), job.ID) // roll back the DB insert
			return err
		}
		if warning != "" {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), warning)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "job %s added (id: %s)\n", job.Name, job.ID)
		return nil
	})
}

func newJobsListCmd(app *config.AppConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered jobs and their last run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withJobsStore(cmd, app, func(ctx context.Context, store *jobs.Store) error {
				all, err := store.List(ctx)
				if err != nil {
					return err
				}
				return emit(cmd, all, func() error { return renderJobTable(cmd.OutOrStdout(), all) })
			})
		},
	}
	addJSONFlag(cmd)
	return cmd
}

// renderJobTable prints the job listing. The approval policy is painted like a
// status word on purpose: "allow" means this job approves every tool call
// unattended, and that is the one field in the table worth catching an eye.
func renderJobTable(out io.Writer, all []jobs.Job) error {
	return renderTable(out, []string{"NAME", "SCHEDULE", "POLICY", "LAST STATUS", "LAST RUN"},
		"no jobs registered; see `invokr jobs add --help`", all,
		func(r renderer, j jobs.Job) []string {
			status, lastRun := r.muted("-"), r.muted("never")
			if j.LastStatus != nil {
				status = r.status(*j.LastStatus)
			}
			if j.LastRunAt != nil {
				lastRun = r.when(*j.LastRunAt)
			}
			return []string{j.Name, j.Schedule, r.policy(j.ApprovalPolicy), status, lastRun}
		})
}

func newJobsRmCmd(app *config.AppConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id-or-name>",
		Short: "Remove one job and its crontab entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withJobsStore(cmd, app, func(ctx context.Context, store *jobs.Store) error {
				job, err := lookupJob(ctx, store, args[0])
				if err != nil {
					return err
				}
				if err := jobs.Strip(job.ID); err != nil {
					// A stale crontab line is recoverable; proceed with DB deletion.
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: crontab update failed: %v\n", err)
				}
				if err := store.Delete(ctx, job.ID); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "job %s removed\n", job.Name)
				return nil
			})
		},
	}
}

func newJobsPurgeCmd(app *config.AppConfig) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Remove every job and its crontab entry",
		Long:  "Asks for confirmation unless -y is given. This deletes the job records too, not just the schedules.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !confirm(cmd, "This will remove all jobs.") {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "aborted")
				return nil
			}
			return withJobsStore(cmd, app, func(ctx context.Context, store *jobs.Store) error {
				if err := store.DeleteAll(ctx); err != nil {
					return err
				}
				// Best-effort: crontab may be absent entirely.
				_ = jobs.StripAll()
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "all jobs purged")
				return nil
			})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

// confirm asks a y/N question on the command's own streams; EOF means no.
func confirm(cmd *cobra.Command, what string) bool {
	_, _ = fmt.Fprint(cmd.OutOrStdout(), what+" Continue? [y/N]: ")
	scanner := bufio.NewScanner(cmd.InOrStdin())
	if !scanner.Scan() {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return answer == "y" || answer == "yes"
}

// lookupJob resolves a job by ID, falling back to its name.
func lookupJob(ctx context.Context, store *jobs.Store, arg string) (jobs.Job, error) {
	job, err := store.Get(ctx, arg)
	if errors.Is(err, sql.ErrNoRows) {
		job, err = store.GetByName(ctx, arg)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.Job{}, fmt.Errorf("job %q not found", arg)
	}
	return job, err
}

func newJobsRunCmd(app *config.AppConfig) *cobra.Command {
	var failOnToolError bool
	cmd := &cobra.Command{
		Use:   "run <id-or-name>",
		Short: "Execute one job now, the way cron would",
		Long:  "This is what the installed crontab entry calls. Running it by hand is\nthe way to test a job without waiting for its schedule; it uses the\njob's own approval policy and times out after 30 minutes.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withJobsStore(cmd, app, func(ctx context.Context, store *jobs.Store) error {
				job, err := lookupJob(ctx, store, args[0])
				if err != nil {
					return err
				}
				// RunJob bootstraps its own runtime (LLM client, memory, tools);
				// the logger here only reports RecordRun bookkeeping failures.
				runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
				defer cancel()
				return jobrun.RunJob(runCtx, *app, store, job, zap.NewNop(), failOnToolError)
			})
		},
	}
	cmd.Flags().BoolVar(&failOnToolError, "fail-on-tool-error", false, "Exit nonzero when the run had tool failures")
	return cmd
}
