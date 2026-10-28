package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/surface/messaging"
	"github.com/invokr-agent/invokr/surface/messaging/signal"
	"github.com/invokr-agent/invokr/surface/messaging/telegram"
)

// NewChatCmd returns the "bot" command with its "serve" sub-command. It is
// named "bot", not "chat": `invokr chat` reads like "talk to the agent", which
// is what a bare `invokr` already does.
//
// Unlike the other group commands it has no default sub-command on purpose —
// a bare invocation must not start a long-running daemon that talks to the
// outside world.
func NewChatCmd(services ServicesProvider) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "bot",
		Short:   "Run the agent as a messaging bot (Telegram / Signal)",
		Long:    "Bridges a chat transport into the agent: messages become prompts and\nanswers go back to the chat, one stored session per conversation.\n\nConfigure a transport under `messaging` in the config first — `serve`\nrefuses to start a transport that has neither an allowlist nor an\nexplicit allow_all, rather than admitting the whole internet.",
		Example: "  invokr bot serve",
		GroupID: groupServe,
	}
	cmd.AddCommand(newChatServeCmd(services))
	return cmd
}

func newChatServeCmd(services ServicesProvider) *cobra.Command {
	return &cobra.Command{
		Use:          "serve",
		Short:        "Start the bot and serve until interrupted",
		Long:         "Long-running. Starts every configured transport, or fails if none is configured.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := services()
			if err != nil {
				return err
			}
			unlock, err := lockBotServe(s.App.Memory.Dir)
			if err != nil {
				return err
			}
			defer unlock()

			store, err := messaging.NewStore(filepath.Join(s.App.Memory.Dir, "messaging.json"))
			if err != nil {
				return fmt.Errorf("chat serve: open store: %w", err)
			}
			defer func() { _ = store.Close() }()

			transports, err := buildChatTransports(s.App.Messaging, s.Logger)
			if err != nil {
				return fmt.Errorf("chat serve: %w", err)
			}
			if len(transports) == 0 {
				return fmt.Errorf("chat serve: no transports configured")
			}
			router := messaging.NewRouter(s, s.App, store, transports)
			return router.Serve(cmd.Context())
		},
	}
}

// lockBotServe takes an exclusive, non-blocking lock on the memory dir so a
// second `bot serve` fails fast instead of clobbering the shared session map.
func lockBotServe(memoryDir string) (func(), error) {
	path := filepath.Join(memoryDir, "bot-serve.lock")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return nil, fmt.Errorf("chat serve: %w", err)
	}
	lock := flock.New(path)
	locked, err := lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("chat serve: lock %s: %w", path, err)
	}
	if !locked {
		return nil, fmt.Errorf(
			"chat serve: another `invokr bot serve` is already running for this memory dir (lock held: %s); stop it first — only one instance may serve a memory dir",
			path)
	}
	return func() { _ = lock.Unlock() }, nil
}

// buildChatTransports constructs transports for every configured backend.
// Each transport requires a non-empty allowlist or an explicit AllowAll —
// otherwise it refuses to start (fail closed) rather than silently
// admitting or silently skipping the transport.
func buildChatTransports(m config.MessagingConfig, logger *zap.Logger) ([]messaging.Transport, error) {
	var transports []messaging.Transport
	if m.Telegram.Token != "" {
		if len(m.Telegram.AllowedChatIDs) == 0 && !m.Telegram.AllowAll {
			return nil, fmt.Errorf("telegram: refusing to start with no allowed_chat_ids and allow_all unset")
		}
		if m.Telegram.AllowAll {
			logger.Warn("telegram allow_all is enabled: every chat is admitted, allowlist is not enforced")
		}
		t, err := telegram.New(m.Telegram.Token, m.Telegram.AllowedChatIDs, m.Telegram.AllowAll)
		if err != nil {
			return nil, fmt.Errorf("telegram: %w", err)
		}
		transports = append(transports, t)
	}
	// Socket has a config-wide default, so "configured" means the operator
	// opted in via an allowlist or allow_all — not merely a non-empty socket path.
	if m.Signal.Socket != "" && (len(m.Signal.AllowedNumbers) > 0 || m.Signal.AllowAll) {
		if m.Signal.AllowAll {
			logger.Warn("signal allow_all is enabled: every sender is admitted, allowlist is not enforced")
		}
		t, err := signal.New(m.Signal.Socket, m.Signal.AllowedNumbers, m.Signal.AllowAll)
		if err != nil {
			return nil, fmt.Errorf("signal: %w", err)
		}
		transports = append(transports, t)
	}
	return transports, nil
}
