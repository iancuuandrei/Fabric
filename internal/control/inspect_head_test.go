package control

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestInspectWithHeadRetainsExactPrefixAcrossAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.jsonl")
	if _, head, err := InspectWithHead(path); err == nil || head != "" {
		t.Fatal("missing controller fabricated a prefix", head, err)
	}
	if err := Append(path, "run.created", creation(t)); err != nil {
		t.Fatal(err)
	}
	before, beforeHead, err := InspectWithHead(path)
	if err != nil || beforeHead == "" {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			snapshot, head, readErr := InspectWithHead(path)
			if readErr != nil {
				t.Error(readErr)
				return
			}
			events, prefixErr := agentToolJournalPrefix(path, head)
			if prefixErr != nil {
				t.Error(prefixErr)
				return
			}
			replayed, replayErr := Replay(events)
			if replayErr != nil || !reflect.DeepEqual(snapshot, replayed) {
				t.Error("snapshot was paired with another prefix", replayErr)
			}
		}()
	}
	appendErr := Append(path, "planning.started", struct{}{})
	readers.Wait()
	if appendErr != nil {
		t.Fatal(appendErr)
	}
	after, afterHead, err := InspectWithHead(path)
	if err != nil || afterHead == beforeHead || reflect.DeepEqual(before, after) {
		t.Fatal("append did not advance snapshot and head", err)
	}
	events, err := agentToolJournalPrefix(path, beforeHead)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(events)
	if err != nil || !reflect.DeepEqual(replayed, before) {
		t.Fatal("later append changed historical snapshot", err)
	}
}
