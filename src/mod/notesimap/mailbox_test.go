package notesimap

import (
	"bytes"
	"testing"
	"time"

	imap "github.com/emersion/go-imap"
)

func appendNote(t *testing.T, mbox *notesMailbox, title, content string) {
	t.Helper()
	raw := buildRawMessage(noteEntry{ID: "", Title: title}, content, time.Now())
	if err := mbox.CreateMessage(nil, time.Now(), bytes.NewReader(raw)); err != nil {
		t.Fatalf("CreateMessage(%q): %v", title, err)
	}
}

func TestMailboxCreateAndListMessages(t *testing.T) {
	s := newTestStore(t)
	mbox := &notesMailbox{username: "alice", store: s}

	appendNote(t, mbox, "First", "First content")
	appendNote(t, mbox, "Second", "Second content")

	status, err := mbox.Status([]imap.StatusItem{imap.StatusMessages, imap.StatusUidNext})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Messages != 2 {
		t.Fatalf("Messages = %d, want 2", status.Messages)
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddRange(1, 2)
	ch := make(chan *imap.Message, 10)
	if err := mbox.ListMessages(false, seqSet, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope}, ch); err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	var got []*imap.Message
	for m := range ch {
		got = append(got, m)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].Envelope == nil || got[0].Envelope.Subject != "First" {
		t.Errorf("first message envelope subject = %+v, want First", got[0].Envelope)
	}
	if got[1].Uid <= got[0].Uid {
		t.Errorf("expected ascending uids, got %d then %d", got[0].Uid, got[1].Uid)
	}
}

func TestMailboxUpdateExistingNoteByUUID(t *testing.T) {
	s := newTestStore(t)
	mbox := &notesMailbox{username: "bob", store: s}

	raw := buildRawMessage(noteEntry{ID: "stable-id", Title: "V1"}, "version one", time.Now())
	if err := mbox.CreateMessage(nil, time.Now(), bytes.NewReader(raw)); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	raw2 := buildRawMessage(noteEntry{ID: "stable-id", Title: "V2"}, "version two", time.Now())
	if err := mbox.CreateMessage(nil, time.Now(), bytes.NewReader(raw2)); err != nil {
		t.Fatalf("CreateMessage (update): %v", err)
	}

	meta, err := s.loadMeta("bob")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if len(meta.Notes) != 1 {
		t.Fatalf("expected update in place, got %d notes", len(meta.Notes))
	}
	if meta.Notes[0].Title != "V2" {
		t.Errorf("Title = %q, want V2", meta.Notes[0].Title)
	}
	content, _ := s.readNoteContent("bob", "stable-id")
	if content != "version two" {
		t.Errorf("content = %q, want %q", content, "version two")
	}
}

func TestMailboxUpdateFlagsAndExpunge(t *testing.T) {
	s := newTestStore(t)
	mbox := &notesMailbox{username: "carol", store: s}

	appendNote(t, mbox, "Keep", "keep me")
	appendNote(t, mbox, "Drop", "drop me")

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(2)
	if err := mbox.UpdateMessagesFlags(false, seqSet, imap.AddFlags, []string{imap.DeletedFlag}); err != nil {
		t.Fatalf("UpdateMessagesFlags: %v", err)
	}

	meta, err := s.loadMeta("carol")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if !hasFlag(meta.Notes[1].Flags, imap.DeletedFlag) {
		t.Fatalf("expected second note to carry \\Deleted flag, got %+v", meta.Notes[1])
	}

	if err := mbox.Expunge(); err != nil {
		t.Fatalf("Expunge: %v", err)
	}

	meta, err = s.loadMeta("carol")
	if err != nil {
		t.Fatalf("loadMeta after expunge: %v", err)
	}
	if len(meta.Notes) != 1 || meta.Notes[0].Title != "Keep" {
		t.Fatalf("expected only Keep to remain, got %+v", meta.Notes)
	}
	if content, _ := s.readNoteContent("carol", meta.Notes[0].ID); content != "keep me" {
		t.Errorf("remaining note content = %q", content)
	}
}

func TestMailboxSearchMessagesAll(t *testing.T) {
	s := newTestStore(t)
	mbox := &notesMailbox{username: "dave", store: s}
	appendNote(t, mbox, "Only", "only note")

	ids, err := mbox.SearchMessages(false, &imap.SearchCriteria{})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("SearchMessages = %v, want [1]", ids)
	}
}
