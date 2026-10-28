package tool_search

import (
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/tools/core"
	"slices"
	"testing"
)

func TestListAndSearchDoNotChangeAvailability(t *testing.T) {
	specs := []core.ToolSpec{{Name: "read", Description: "Read files. More detail."}, {Name: "optional", Description: "Search remote data", Parameters: map[string]any{"type": "object"}}}
	listed := List(specs, nil, "", nil, defaultProtected)
	if len(listed) != 2 || !listed[0].Protected || !listed[0].Enabled || listed[1].Enabled || listed[1].Parameters != nil {
		t.Fatalf("list: %+v", listed)
	}
	searched := List(specs, nil, "REMOTE data", nil, defaultProtected)
	if len(searched) != 1 || searched[0].Enabled {
		t.Fatalf("search: %+v", searched)
	}
	enabled := List(specs, []string{"optional"}, "optional", []string{"optional"}, defaultProtected)
	if len(enabled) != 1 || !enabled[0].Enabled || enabled[0].Parameters == nil {
		t.Fatalf("enabled: %+v", enabled)
	}
	if listed[0].Description != "Read files" {
		t.Fatalf("description: %q", listed[0].Description)
	}
}

func defaultProtected(name string) bool { return slices.Contains(constants.ManagedCoreTools, name) }
