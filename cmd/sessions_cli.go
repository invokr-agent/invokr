package cmd

import (
	"cmp"
	"fmt"
	"io"
	"strconv"

	"github.com/invokr-ai/invokr/capability/sessionstore"

	"github.com/spf13/cobra"
)

func NewSessionsCmd(services ServicesProvider) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sessions",
		Short:   "Browse stored session history",
		Long:    "Every agent run is stored: prompts, answers, and the tool ledger.\nRun bare, this is the history view; search finds a session by content,\nshow prints one transcript, and the IDs it prints are what --resume\ntakes.",
		Example: "  invokr sessions                       # recent sessions, newest first\n  invokr sessions --limit 5 --json      # same, for scripts\n  invokr sessions search \"flaky test\"\n  invokr sessions show <session-id>\n  invokr sessions rm <session-id> -y\n  invokr --resume <session-id>          # continue one in the TUI",
		GroupID: groupInspect,
	}
	useDefaultSubcommand(cmd, "list", newSessionsListCmd(services), newSessionsSearchCmd(services), newSessionsShowCmd(services), newSessionsRmCmd(services))
	return cmd
}

func newSessionsListCmd(services ServicesProvider) *cobra.Command {
	var all bool
	var limit int
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List recent sessions from this directory, newest first",
		Example: "  invokr sessions list --limit 50\n  invokr sessions list --all",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withSessionStore(services, func(store *sessionstore.Store) error {
				sessions, err := store.List()
				if err != nil {
					return err
				}
				if !all {
					sessions = sessionstore.FilterByCwd(sessions, store.Cwd())
				}
				if limit > 0 && len(sessions) > limit {
					sessions = sessions[:limit]
				}
				return emit(cmd, sessions, func() error { return renderSessionTable(cmd.OutOrStdout(), sessions) })
			})
		},
	}
	addJSONFlag(cmd)
	cmd.Flags().BoolVar(&all, "all", false, "List sessions from every directory, not just this one")
	cmd.Flags().IntVar(&limit, "limit", 20, "Show at most this many sessions; 0 lists every stored session")
	return cmd
}

// renderSessionTable is the default listing: a session ID is only useful next
// to something that identifies the conversation, and the JSON blob the command
// used to print made the reader do that join by eye.
func renderSessionTable(out io.Writer, sessions []sessionstore.SessionInfo) error {
	return renderTable(out, []string{"ID", "UPDATED", "MSGS", "TITLE"},
		"no stored sessions yet; run `invokr` to start one", sessions,
		func(r renderer, info sessionstore.SessionInfo) []string {
			return []string{r.muted(info.ID), r.when(info.UpdatedAt), strconv.Itoa(info.Messages), sessionLabel(info)}
		})
}

// sessionLabel mirrors the TUI picker's fallback chain so the CLI listing and
// the picker never disagree about what a session is called.
func sessionLabel(info sessionstore.SessionInfo) string {
	return cmp.Or(info.Title, info.Preview, "(untitled)")
}

func newSessionsSearchCmd(services ServicesProvider) *cobra.Command {
	var maxResults int
	cmd := &cobra.Command{
		Use:     "search <query>",
		Short:   "Find stored sessions by their content",
		Long:    "Matches against the stored transcripts, not just titles.",
		Example: "  invokr sessions search \"panic in the runner\" --max-results 3",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSessionStore(services, func(store *sessionstore.Store) error {
				hits, err := store.Search(cmd.Context(), args[0], maxResults)
				if err != nil {
					return err
				}
				return emit(cmd, hits, func() error {
					return renderTable(cmd.OutOrStdout(), []string{"SESSION", "ROLE", "SNIPPET"}, "no matching sessions", hits,
						func(r renderer, h sessionstore.SearchHit) []string {
							return []string{r.muted(h.SessionID), h.Role, h.Snippet}
						})
				})
			})
		},
	}
	addJSONFlag(cmd)
	cmd.Flags().IntVar(&maxResults, "max-results", 10, "Maximum number of matching sessions to return")
	return cmd
}

func newSessionsRmCmd(services ServicesProvider) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <session-id>",
		Short: "Delete one stored session: its transcript and its memory records",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !confirm(cmd, "This will delete session "+args[0]+".") {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "aborted")
				return nil
			}
			s, err := services()
			if err != nil {
				return err
			}
			store := sessionstore.NewForWorkspace(s.App.Memory.Dir, s.App.Workspace.Root, s.Logger)
			if err := purgeSession(cmd, s, store)(args[0]); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "session %s removed\n", args[0])
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

func newSessionsShowCmd(services ServicesProvider) *cobra.Command {
	var around string
	cmd := &cobra.Command{
		Use:     "show <session-id>",
		Short:   "Print one stored session transcript",
		Example: "  invokr sessions show <session-id>\n  invokr sessions show <session-id> --around 12   # that message, plus 3 either side",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withSessionStore(services, func(store *sessionstore.Store) error {
				messages, err := store.Load(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if around != "" {
					messages = aroundMessages(messages, around, 3)
				}
				return emit(cmd, messages, func() error {
					for _, m := range messages {
						if _, err := fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s\n%s\n\n", m.Role, m.ID, m.Text); err != nil {
							return err
						}
					}
					return nil
				})
			})
		},
	}
	addJSONFlag(cmd)
	cmd.Flags().StringVar(&around, "around", "", "Print only the 3 messages either side of this message ID or zero-based index")
	return cmd
}

// withSessionStore resolves the services and hands the workspace's session
// store to fn — the preamble every sessions subcommand shares.
func withSessionStore(services ServicesProvider, fn func(*sessionstore.Store) error) error {
	s, err := services()
	if err != nil {
		return err
	}
	return fn(sessionstore.NewForWorkspace(s.App.Memory.Dir, s.App.Workspace.Root, s.Logger))
}

func aroundMessages(messages []sessionstore.Message, id string, radius int) []sessionstore.Message {
	for i, msg := range messages {
		if msg.ID == id || strconv.Itoa(i) == id {
			return messages[max(i-radius, 0):min(i+radius+1, len(messages))]
		}
	}
	return messages
}
