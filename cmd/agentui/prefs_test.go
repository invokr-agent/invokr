package agentui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHideHintsRoundTripsThroughDisk(t *testing.T) {
	path := prefsPath(t.TempDir())

	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.prefsPath = path
	m.setHideHints(true)

	if got := loadPrefs(path); !got.HideHints {
		t.Fatalf("hide_hints did not reach disk: %+v", got)
	}

	next := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	next.prefsPath = path
	next.hideHints = loadPrefs(path).HideHints
	if !next.hideHints {
		t.Fatal("a fresh model did not pick the hidden hints back up")
	}

	next.setHideHints(false)
	if got := loadPrefs(path); got.HideHints {
		t.Fatalf("un-hiding was not persisted: %+v", got)
	}
}

func TestPrefsWithoutADirAreInMemoryOnly(t *testing.T) {
	if path := prefsPath(""); path != "" {
		t.Fatalf("empty dir should disable persistence, got %q", path)
	}

	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.setHideHints(true) // prefsPath is empty: must not panic or write
	if !m.hideHints {
		t.Fatal("the toggle should still apply in memory")
	}
}

func TestLoadPrefsToleratesGarbage(t *testing.T) {
	path := prefsPath(t.TempDir())
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := loadPrefs(path); got.HideHints {
		t.Fatalf("a corrupt file should read as defaults, got %+v", got)
	}
	if got := loadPrefs(filepath.Join(t.TempDir(), "missing.json")); got.HideHints {
		t.Fatalf("a missing file should read as defaults, got %+v", got)
	}
}
