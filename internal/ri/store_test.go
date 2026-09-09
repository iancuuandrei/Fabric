package ri

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStorePublishesWithoutReplacingContent(t *testing.T) {
	store := Store{t.TempDir()}
	data := []byte("opaque fixture\n")
	id := SnapshotID(data)
	path, err := store.Publish(id, data)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(id, data); err != nil {
		t.Fatal(err)
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(original, current) {
		t.Fatal("existing artifact replaced", err)
	}
	got, err := store.Read(id)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Publish(id, data); err == nil {
		t.Fatal("existing corruption overwritten")
	}
	if _, err := store.Read(id); err == nil {
		t.Fatal("corrupt artifact admitted")
	}
}

func TestStoreRecoveryPreservesPartialAndConflictingEvidence(t *testing.T) {
	for _, phase := range []string{"partial", "staged", "linked", "conflict"} {
		t.Run(phase, func(t *testing.T) {
			store := Store{t.TempDir()}
			data := []byte("complete\n")
			id := SnapshotID(data)
			pending := filepath.Join(store.Directory, id+".pending")
			final := filepath.Join(store.Directory, id+".jsonl")
			staged := data
			if phase == "partial" {
				staged = []byte("part")
			}
			if err := os.WriteFile(pending, staged, 0600); err != nil {
				t.Fatal(err)
			}
			if phase == "linked" {
				if err := os.Link(pending, final); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "conflict" {
				if err := os.WriteFile(final, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Read(id); err == nil {
				t.Fatal("pending publication admitted")
			}
			if _, err := store.Publish(id, data); err == nil {
				t.Fatal("pending publication silently retried")
			}
			_, err := store.Reconcile(id)
			if phase == "partial" || phase == "conflict" {
				if err == nil {
					t.Fatal("ambiguous recovery accepted")
				}
				if got, err := os.ReadFile(pending); err != nil || !bytes.Equal(got, staged) {
					t.Fatal("pending evidence changed", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if got, err := store.Read(id); err != nil || !bytes.Equal(got, data) {
					t.Fatal(err)
				}
				if _, err := os.Lstat(pending); !os.IsNotExist(err) {
					t.Fatal("staging link remains", err)
				}
			}
		})
	}
}

func TestConcurrentStorePublishRetainsExactBytes(t *testing.T) {
	store := Store{t.TempDir()}
	data := []byte("same object")
	id := SnapshotID(data)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); _, _ = store.Publish(id, data) }()
	}
	group.Wait()
	if _, err := store.Reconcile(id); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read(id)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal(err)
	}
}
