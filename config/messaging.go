package config

type MessagingConfig struct {
	Telegram TelegramConfig `json:"telegram"`
	Signal   SignalConfig   `json:"signal,omitempty"`
}

// TelegramConfig configures the Telegram long-poll transport.
type TelegramConfig struct {
	Token          string  `json:"token,omitempty" env:"INVOKR_TELEGRAM_TOKEN"`
	AllowedChatIDs []int64 `json:"allowed_chat_ids,omitempty"`
	// AllowAll disables the allowlist check entirely, admitting every chat.
	// This is an explicit escape hatch — an empty AllowedChatIDs otherwise
	// denies all chats (fail closed).
	AllowAll bool `json:"allow_all,omitempty" env:"INVOKR_TELEGRAM_ALLOW_ALL"`
}

// SignalConfig configures the signal-cli JSON-RPC transport.
type SignalConfig struct {
	Socket         string   `json:"socket,omitempty"`          // path to signal-cli JSON-RPC UNIX socket
	AllowedNumbers []string `json:"allowed_numbers,omitempty"` // E.164 format
	// AllowAll disables the allowlist check entirely, admitting every number.
	// This is an explicit escape hatch — an empty AllowedNumbers otherwise
	// denies all senders (fail closed).
	AllowAll bool `json:"allow_all,omitempty" env:"INVOKR_SIGNAL_ALLOW_ALL"`
}
