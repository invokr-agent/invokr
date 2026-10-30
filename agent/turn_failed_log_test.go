package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestLogTurnFailedLevel(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want zapcore.Level
	}{
		{"canceled", canceled, zapcore.InfoLevel},
		{"deadline", expired, zapcore.ErrorLevel},
		{"live", context.Background(), zapcore.ErrorLevel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.DebugLevel)
			(&Runner{logger: zap.New(core)}).logTurnFailed(tc.ctx, 1, time.Now(), errors.New("boom"))
			if entries := logs.All(); len(entries) != 1 || entries[0].Level != tc.want {
				t.Fatalf("entries = %+v, want one at %s", entries, tc.want)
			}
		})
	}
}
