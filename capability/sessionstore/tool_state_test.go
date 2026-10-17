package sessionstore

import (
	"context"
	"os"
	"reflect"
	"sync"
	"testing"
)

func TestToolStateConcurrentSaveLoadPreservesWholeSnapshots(t *testing.T) {
	store := New(t.TempDir())
	states := []ToolState{
		{Enabled: []string{"first"}, Catalog: []ToolListing{{Name: "first", Description: "First tool."}}},
		{Enabled: []string{"second"}, Catalog: []ToolListing{{Name: "second", Description: "Second tool."}}},
	}
	if err := store.SaveToolState("s", states[0]); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, state := range states {
		workers.Go(func() {
			<-start
			for range 32 {
				if err := store.SaveToolState("s", state); err != nil {
					t.Error(err)
					return
				}
			}
		})
		workers.Go(func() {
			<-start
			for range 64 {
				got, err := store.LoadToolState("s")
				if err != nil || (!reflect.DeepEqual(got, states[0]) && !reflect.DeepEqual(got, states[1])) {
					t.Errorf("partial tool state: %+v, err=%v", got, err)
					return
				}
			}
		})
	}
	close(start)
	workers.Wait()
}

func TestToolStateRoundTripDeleteAndCorruption(t *testing.T) {
	store := New(t.TempDir())
	state := ToolState{Enabled: []string{"plugin"}, Catalog: []ToolListing{{Name: "plugin", Description: "Run plugin."}}}
	if err := store.SaveToolState("s", state); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadToolState("s")
	if err != nil || !reflect.DeepEqual(got, state) {
		t.Fatalf("state=%+v err=%v", got, err)
	}
	if _, err = store.Append(context.Background(), "s", "user", "task"); err != nil {
		t.Fatal(err)
	}
	if err = store.Delete("s"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(store.toolStatePath("s")); !os.IsNotExist(err) {
		t.Fatalf("state retained: %v", err)
	}
	if err = os.WriteFile(store.toolStatePath("s"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.LoadToolState("s"); err == nil {
		t.Fatal("corrupt state silently reset")
	}
}
