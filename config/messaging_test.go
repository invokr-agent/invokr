package config

import "testing"

func TestMessagingConfig_AllowAllDefaultsFalse(t *testing.T) {
	c := MessagingConfig{}
	applyEnv(&c)
	if c.Telegram.AllowAll {
		t.Fatal("expected Telegram.AllowAll to default to false")
	}
	if c.Signal.AllowAll {
		t.Fatal("expected Signal.AllowAll to default to false")
	}
}

func TestMessagingConfig_AllowAllEnvOverride(t *testing.T) {
	t.Setenv("INVOKR_TELEGRAM_ALLOW_ALL", "true")
	t.Setenv("INVOKR_SIGNAL_ALLOW_ALL", "true")
	c := MessagingConfig{}
	applyEnv(&c)
	if !c.Telegram.AllowAll {
		t.Fatal("expected Telegram.AllowAll true from env")
	}
	if !c.Signal.AllowAll {
		t.Fatal("expected Signal.AllowAll true from env")
	}
}
