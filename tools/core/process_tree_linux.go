//go:build linux

package core

import (
	"os"
	"strconv"
	"strings"
)

// descendants returns every live descendant of root found by walking /proc
// parent links, including ones that moved to another process group or session.
func descendants(root int) []int {
	entries, _ := os.ReadDir("/proc")
	children := map[int][]int{}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// Fields after the ")" that closes comm: state, ppid, ...
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) < 2 {
			continue
		}
		if ppid, err := strconv.Atoi(fields[1]); err == nil {
			children[ppid] = append(children[ppid], pid)
		}
	}
	var out []int
	for queue := children[root]; len(queue) > 0; queue = queue[1:] {
		out = append(out, queue[0])
		queue = append(queue, children[queue[0]]...)
	}
	return out
}
