package cmd

import (
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/invokr-ai/invokr/config"
)

// TestBuildChatTransports_RefusesEmptyTelegramAllowlist verifies chat serve
// fails fast at startup when a Telegram token is configured but neither an
// allowlist nor allow_all is set (fail closed, not per-message).
func TestBuildChatTransports_RefusesEmptyTelegramAllowlist(t *testing.T) {
	m := config.MessagingConfig{Telegram: config.TelegramConfig{Token: "tok"}}
	_, err := buildChatTransports(m, zap.NewNop())
	if err == nil {
		t.Fatal("expected error for telegram with no allowlist and no allow_all")
	}
	if !strings.Contains(err.Error(), "telegram") {
		t.Fatalf("expected telegram-specific error, got: %v", err)
	}
}

// TestBuildChatTransports_RefusesEmptySignalAllowlist verifies the same
// fail-closed startup refusal for the Signal transport.
func TestBuildChatTransports_RefusesEmptySignalAllowlist(t *testing.T) {
	m := config.MessagingConfig{Signal: config.SignalConfig{AllowedNumbers: nil, AllowAll: false}}
	m.Signal.Socket = "/fake.sock"
	transports, err := buildChatTransports(m, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(transports) != 0 {
		t.Fatalf("expected signal to be skipped without an explicit allowlist or allow_all, got %d transports", len(transports))
	}
}

// TestBuildChatTransports_AllowAllStartsWithoutAllowlist verifies allow_all
// is a valid escape hatch that lets a transport start with no allowlist.
func TestBuildChatTransports_AllowAllStartsWithoutAllowlist(t *testing.T) {
	m := config.MessagingConfig{Telegram: config.TelegramConfig{Token: "tok", AllowAll: true}}
	transports, err := buildChatTransports(m, zap.NewNop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(transports) != 1 {
		t.Fatalf("expected 1 transport, got %d", len(transports))
	}
}

// TestLockBotServeRejectsSecondInstance verifies the memory-dir lock makes
// `bot serve` single-instance: a second acquisition fails while the first is
// held, and succeeds again once it is released.
func TestLockBotServeRejectsSecondInstance(t *testing.T) {
	dir := t.TempDir()

	unlock, err := lockBotServe(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	_, err = lockBotServe(dir)
	if err == nil {
		t.Fatalf("second lock succeeded while the first was held")
	}
	if !strings.Contains(err.Error(), "bot-serve.lock") {
		t.Fatalf("error does not name the lock path: %v", err)
	}

	unlock()
	unlock2, err := lockBotServe(dir)
	if err != nil {
		t.Fatalf("relock after unlock: %v", err)
	}
	unlock2()
}
