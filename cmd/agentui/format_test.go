package agentui

import (
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{46620 * time.Nanosecond, "<1ms"},
		{60460 * time.Microsecond, "60ms"},
		{272914939 * time.Nanosecond, "273ms"},
		{791160 * time.Microsecond, "791ms"},
		{time.Second, "1.0s"},
		{6200 * time.Millisecond, "6.2s"},
		{10570 * time.Millisecond, "10.6s"},
		{95 * time.Second, "1m35s"},
		{-time.Second, "0ms"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.in); got != tc.want {
			t.Fatalf("formatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{3100, "3.1 KB"},
		{1500000, "1.5 MB"},
		{2_500_000_000, "2.5 GB"},
		{-1, "0 B"},
	}
	for _, tc := range cases {
		if got := formatBytes(tc.in); got != tc.want {
			t.Fatalf("formatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
