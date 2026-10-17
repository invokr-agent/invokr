package agentui

import (
	"fmt"
	"strings"
	"time"
)

// formatDuration renders a tool or turn duration at the precision the design
// asks for: three significant figures at most, never a µs tail.
//
// tuikit.CoarseDuration rounds to unit/100, which in the millisecond range
// leaves five digits (`272.91ms`), so it is not usable here.
func formatDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0ms"
	case d < time.Millisecond:
		// Sub-millisecond calls are real (a cached tool, a rejected arg parse);
		// "0ms" would read as "not measured".
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Round(time.Millisecond)/time.Millisecond)
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", d/time.Minute, d%time.Minute/time.Second)
}

// formatTokensCompact renders a token count for the footer, where the design
// writes "12.4k / 128k" — the full "8,049 / 65,536 tokens" no longer fits now
// that the count shares a row with the keybindings.
func formatTokensCompact(n int64) string {
	switch {
	case n < 0:
		return "0"
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 100_000:
		text := fmt.Sprintf("%.1fk", float64(n)/1000)
		return strings.Replace(text, ".0k", "k", 1)
	}
	return fmt.Sprintf("%dk", (n+500)/1000)
}

// formatBytes renders an output size in decimal units. The design writes
// "3.1 KB"; tuikit.FormatBytes is IEC ("3.1 KiB"), which is more correct and
// less readable, and the design wins on chrome.
func formatBytes(n int64) string {
	if n < 0 {
		return "0 B"
	}
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, exp := float64(n)/unit, 0
	for value >= unit && exp < 3 {
		value, exp = value/unit, exp+1
	}
	return fmt.Sprintf("%.1f %s", value, [...]string{"KB", "MB", "GB", "TB"}[exp])
}
