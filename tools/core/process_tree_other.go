//go:build unix && !linux

package core

// descendants is Linux-only (/proc); elsewhere the process-group kill applies.
func descendants(int) []int { return nil }
