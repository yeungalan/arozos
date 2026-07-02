package notesimap

import (
	"path/filepath"
	"testing"
)

// newTestStore returns a store rooted at t.TempDir(), one subdirectory per
// username, bypassing user.UserHandler entirely.
func newTestStore(t *testing.T) *store {
	root := t.TempDir()
	return newStoreWithResolver(func(username, vpath string) (string, error) {
		return filepath.Join(root, username, filepath.FromSlash(vpath)), nil
	})
}

func TestLoadMetaDefaultsOnMissingFile(t *testing.T) {
	s := newTestStore(t)

	meta, err := s.loadMeta("alice")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if meta.Theme != "dark" {
		t.Errorf("Theme = %q, want dark", meta.Theme)
	}
	if len(meta.Notes) != 0 {
		t.Errorf("Notes = %v, want empty", meta.Notes)
	}
	if meta.UidValidity == 0 || meta.UidNext == 0 {
		t.Errorf("expected UidValidity/UidNext to be backfilled, got %+v", meta)
	}
}

func TestLoadMetaBackfillsUidsAndPersists(t *testing.T) {
	s := newTestStore(t)

	meta := &noteMeta{Theme: "dark", Notes: []noteEntry{
		{ID: "a", Title: "A", UpdatedAt: 1},
		{ID: "b", Title: "B", UpdatedAt: 2},
	}}
	if err := s.saveMeta("bob", meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	reloaded, err := s.loadMeta("bob")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if reloaded.Notes[0].Uid == 0 || reloaded.Notes[1].Uid == 0 {
		t.Fatalf("expected uids to be backfilled, got %+v", reloaded.Notes)
	}
	if reloaded.Notes[0].Uid >= reloaded.Notes[1].Uid {
		t.Errorf("expected ascending uids, got %d then %d", reloaded.Notes[0].Uid, reloaded.Notes[1].Uid)
	}
	for _, n := range reloaded.Notes {
		if len(n.Flags) == 0 {
			t.Errorf("expected default flags for note %s", n.ID)
		}
	}

	// A second load must not reassign uids.
	again, err := s.loadMeta("bob")
	if err != nil {
		t.Fatalf("loadMeta (again): %v", err)
	}
	if again.Notes[0].Uid != reloaded.Notes[0].Uid || again.Notes[1].Uid != reloaded.Notes[1].Uid {
		t.Errorf("uids changed across reloads: %+v vs %+v", reloaded.Notes, again.Notes)
	}
}

func TestLoadMetaSortsByUid(t *testing.T) {
	s := newTestStore(t)
	meta := &noteMeta{Notes: []noteEntry{
		{ID: "second", Uid: 5},
		{ID: "first", Uid: 2},
	}}
	if err := s.saveMeta("carol", meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}
	reloaded, err := s.loadMeta("carol")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if reloaded.Notes[0].ID != "first" || reloaded.Notes[1].ID != "second" {
		t.Errorf("notes not sorted by uid: %+v", reloaded.Notes)
	}
}

func TestWithMetaAtomicUpdate(t *testing.T) {
	s := newTestStore(t)

	err := s.withMeta("dave", func(meta *noteMeta) (bool, error) {
		meta.Notes = append(meta.Notes, noteEntry{ID: "n1", Title: "Hello", UpdatedAt: 100})
		return true, nil
	})
	if err != nil {
		t.Fatalf("withMeta: %v", err)
	}

	meta, err := s.loadMeta("dave")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if len(meta.Notes) != 1 || meta.Notes[0].ID != "n1" {
		t.Fatalf("expected note n1 to be persisted, got %+v", meta.Notes)
	}
}

func TestNoteContentReadWriteDelete(t *testing.T) {
	s := newTestStore(t)

	if content, err := s.readNoteContent("erin", "missing"); err != nil || content != "" {
		t.Fatalf("readNoteContent on missing file = (%q, %v), want (\"\", nil)", content, err)
	}

	if err := s.writeNoteContent("erin", "n1", "hello world"); err != nil {
		t.Fatalf("writeNoteContent: %v", err)
	}
	content, err := s.readNoteContent("erin", "n1")
	if err != nil || content != "hello world" {
		t.Fatalf("readNoteContent = (%q, %v), want (\"hello world\", nil)", content, err)
	}

	if err := s.deleteNoteContent("erin", "n1"); err != nil {
		t.Fatalf("deleteNoteContent: %v", err)
	}
	if content, _ := s.readNoteContent("erin", "n1"); content != "" {
		t.Errorf("expected empty content after delete, got %q", content)
	}
	// Deleting again must be a no-op, not an error.
	if err := s.deleteNoteContent("erin", "n1"); err != nil {
		t.Errorf("deleteNoteContent on already-missing file: %v", err)
	}
}

func TestNewNoteIDIsUniqueAndSafe(t *testing.T) {
	meta := &noteMeta{}
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		id := newNoteID(meta)
		if !idPattern.MatchString(id) {
			t.Errorf("generated id %q does not match safe-id pattern", id)
		}
		if seen[id] {
			t.Errorf("generated duplicate id %q", id)
		}
		seen[id] = true
		meta.Notes = append(meta.Notes, noteEntry{ID: id})
	}
}

func TestDeriveTitle(t *testing.T) {
	cases := []struct {
		content string
		want    string
	}{
		{"", "New Note"},
		{"\n\n  \n", "New Note"},
		{"Hello world", "Hello world"},
		{"\n  Trimmed line  \nSecond line", "Trimmed line"},
		{string(make([]byte, 0)), "New Note"},
	}
	for _, c := range cases {
		if got := deriveTitle(c.content); got != c.want {
			t.Errorf("deriveTitle(%q) = %q, want %q", c.content, got, c.want)
		}
	}

	long := ""
	for i := 0; i < 100; i++ {
		long += "a"
	}
	got := deriveTitle(long)
	if len(got) != 60 {
		t.Errorf("deriveTitle truncation: len = %d, want 60", len(got))
	}
}
