package applenotessync

/*
	Integration tests for the Notes mailbox state machine: reconcile,
	Apple-side APPEND (create + edit) and the \Deleted + EXPUNGE flows.
*/

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	db "imuslab.com/arozos/mod/database"
)

func newTestMailbox(t *testing.T) (*notesMailbox, string) {
	t.Helper()
	notesDir := t.TempDir()

	database, err := db.NewDatabase(filepath.Join(t.TempDir(), "test.db"), false)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(database.Close)
	database.NewTable(dbTable)

	h := &Handler{
		opts:             Options{Database: database},
		notesDirResolver: func(string) (string, error) { return notesDir, nil },
	}
	return newNotesMailbox(h, "alice"), notesDir
}

func writeArozosNote(t *testing.T, dir, id, content string, ts int64) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, id+".txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	meta := loadNotesMeta(dir)
	upsertMetaEntry(meta, id, derivedTitle("", content), ts)
	if err := saveNotesMeta(dir, meta); err != nil {
		t.Fatal(err)
	}
}

func messageCount(t *testing.T, mb *notesMailbox) int {
	t.Helper()
	status, err := mb.Status([]imap.StatusItem{imap.StatusMessages})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return int(status.Messages)
}

func TestReconcileLifecycle(t *testing.T) {
	mb, dir := newTestMailbox(t)
	writeArozosNote(t, dir, "note_a", "Alpha\nfirst body", 1000)
	writeArozosNote(t, dir, "note_b", "Beta\nsecond body", 2000)

	if n := messageCount(t, mb); n != 2 {
		t.Fatalf("want 2 messages, got %d", n)
	}

	st := mb.loadState()
	var alphaUID uint32
	var alphaUUID string
	for _, e := range st.Entries {
		if e.NoteID == "note_a" {
			alphaUID, alphaUUID = e.UID, e.UUID
		}
	}
	if alphaUID == 0 || alphaUUID == "" {
		t.Fatal("note_a entry missing")
	}

	// Stability: a second reconcile must not regenerate anything
	uidNextBefore := st.UIDNext
	messageCount(t, mb)
	if st = mb.loadState(); st.UIDNext != uidNextBefore {
		t.Fatalf("reconcile not idempotent: uidnext %d -> %d", uidNextBefore, st.UIDNext)
	}

	// Edit on the arozos side: same UUID, new UID, old message gone
	writeArozosNote(t, dir, "note_a", "Alpha edited\nnew body", 3000)
	if n := messageCount(t, mb); n != 2 {
		t.Fatalf("want 2 messages after edit, got %d", n)
	}
	st = mb.loadState()
	found := false
	for _, e := range st.Entries {
		if e.NoteID == "note_a" {
			found = true
			if e.UID == alphaUID {
				t.Error("edited note kept its old UID; clients would never refetch it")
			}
			if e.UUID != alphaUUID {
				t.Error("edited note changed UUID; Apple would duplicate the note")
			}
		}
	}
	if !found {
		t.Fatal("note_a entry missing after edit")
	}

	// Delete on the arozos side
	os.Remove(filepath.Join(dir, "note_b.txt"))
	meta := loadNotesMeta(dir)
	removeMetaEntry(meta, "note_b")
	saveNotesMeta(dir, meta)
	if n := messageCount(t, mb); n != 1 {
		t.Fatalf("want 1 message after delete, got %d", n)
	}
}

func TestListMessagesFetch(t *testing.T) {
	mb, dir := newTestMailbox(t)
	writeArozosNote(t, dir, "note_x", "Fetch me\nbody line", 1000)
	messageCount(t, mb) // trigger reconcile

	seqset := new(imap.SeqSet)
	seqset.AddRange(1, 1000)
	ch := make(chan *imap.Message, 10)
	section := &imap.BodySectionName{Peek: true}
	err := mb.ListMessages(true, seqset, []imap.FetchItem{
		imap.FetchUid, imap.FetchEnvelope, section.FetchItem(),
	}, ch)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	msgs := []*imap.Message{}
	for m := range ch {
		msgs = append(msgs, m)
	}
	if len(msgs) != 1 {
		t.Fatalf("want 1 message, got %d", len(msgs))
	}
	if msgs[0].Envelope == nil || msgs[0].Envelope.Subject == "" {
		t.Error("envelope missing")
	}
	for _, lit := range msgs[0].Body {
		raw := new(bytes.Buffer)
		raw.ReadFrom(lit)
		if !strings.Contains(raw.String(), "X-Uniform-Type-Identifier: com.apple.mail-note") {
			t.Error("body missing Apple note type header")
		}
	}
}

func appleRaw(uuid, title, htmlBody string) []byte {
	return []byte(strings.Join([]string{
		"X-Uniform-Type-Identifier: com.apple.mail-note",
		"X-Universally-Unique-Identifier: " + uuid,
		"Subject: " + title,
		"Content-Type: text/html; charset=utf-8",
		"MIME-Version: 1.0",
		"Date: Mon, 01 Jun 2026 10:00:00 +0000",
		"",
		htmlBody,
	}, "\r\n"))
}

func TestAppleAppendCreatesNote(t *testing.T) {
	mb, dir := newTestMailbox(t)

	raw := appleRaw("AAAA1111-2222-3333-4444-555566667777", "Phone note",
		"<html><body><div>Phone note</div><div>made on iPhone</div></body></html>")
	if err := mb.CreateMessage([]string{imap.SeenFlag}, time.Now(), bytes.NewBuffer(raw)); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Note file + metadata must exist
	noteID := "apple_AAAA1111-2222-3333-4444-555566667777"
	data, err := os.ReadFile(filepath.Join(dir, noteID+".txt"))
	if err != nil {
		t.Fatalf("note file not created: %v", err)
	}
	if want := "Phone note\nmade on iPhone"; string(data) != want {
		t.Errorf("content: got %q want %q", string(data), want)
	}
	meta := loadNotesMeta(dir)
	if len(meta.Notes) != 1 || meta.Notes[0].ID != noteID {
		t.Fatalf("meta entry wrong: %+v", meta.Notes)
	}

	// Reconcile must treat the appended message as up to date (no churn)
	st := mb.loadState()
	uidNext := st.UIDNext
	if n := messageCount(t, mb); n != 1 {
		t.Fatalf("want 1 message, got %d", n)
	}
	if st = mb.loadState(); st.UIDNext != uidNext {
		t.Error("reconcile regenerated an unchanged Apple-appended note")
	}
}

func TestAppleEditAndDeleteFlows(t *testing.T) {
	mb, dir := newTestMailbox(t)
	writeArozosNote(t, dir, "note_e", "Original\nbody", 1000)
	messageCount(t, mb)

	st := mb.loadState()
	origUID := st.Entries[0].UID
	uuid := st.Entries[0].UUID

	// Apple edit: APPEND new version (same UUID) ...
	raw := appleRaw(uuid, "Edited on iPad",
		"<html><body><div>Edited on iPad</div><div>new body</div></body></html>")
	if err := mb.CreateMessage([]string{imap.SeenFlag}, time.Now(), bytes.NewBuffer(raw)); err != nil {
		t.Fatalf("append: %v", err)
	}
	// ... then flag the old message \Deleted and expunge it
	ss := new(imap.SeqSet)
	ss.AddNum(origUID)
	if err := mb.UpdateMessagesFlags(true, ss, imap.AddFlags, []string{imap.DeletedFlag}); err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := mb.Expunge(); err != nil {
		t.Fatalf("expunge: %v", err)
	}

	// The note must survive with the new content
	data, err := os.ReadFile(filepath.Join(dir, "note_e.txt"))
	if err != nil {
		t.Fatal("edit flow deleted the note file")
	}
	if want := "Edited on iPad\nnew body"; string(data) != want {
		t.Errorf("content: got %q want %q", string(data), want)
	}
	if n := messageCount(t, mb); n != 1 {
		t.Fatalf("want 1 message after edit flow, got %d", n)
	}

	// Apple delete: flag the only remaining message and expunge
	st = mb.loadState()
	ss = new(imap.SeqSet)
	ss.AddNum(st.Entries[0].UID)
	mb.UpdateMessagesFlags(true, ss, imap.AddFlags, []string{imap.DeletedFlag})
	if err := mb.Expunge(); err != nil {
		t.Fatalf("expunge: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "note_e.txt")); !os.IsNotExist(err) {
		t.Error("note file should be deleted")
	}
	if meta := loadNotesMeta(dir); len(meta.Notes) != 0 {
		t.Errorf("meta should be empty, got %+v", meta.Notes)
	}
	if n := messageCount(t, mb); n != 0 {
		t.Fatalf("want 0 messages, got %d", n)
	}
}
