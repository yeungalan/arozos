package notesimap

import (
	"testing"

	"github.com/emersion/go-imap/backend"
)

func TestUserGetMailboxCaseInsensitive(t *testing.T) {
	u := &imapUser{username: "alice", store: newTestStore(t)}

	for _, name := range []string{"Notes", "notes", "NOTES"} {
		mbox, err := u.GetMailbox(name)
		if err != nil {
			t.Fatalf("GetMailbox(%q): %v", name, err)
		}
		if mbox.Name() != mailboxName {
			t.Errorf("GetMailbox(%q).Name() = %q, want %q", name, mbox.Name(), mailboxName)
		}
	}

	for _, name := range []string{"INBOX", "inbox"} {
		mbox, err := u.GetMailbox(name)
		if err != nil {
			t.Fatalf("GetMailbox(%q): %v", name, err)
		}
		if mbox.Name() != "INBOX" {
			t.Errorf("GetMailbox(%q).Name() = %q, want INBOX", name, mbox.Name())
		}
	}

	if _, err := u.GetMailbox("Drafts"); err != backend.ErrNoSuchMailbox {
		t.Errorf("GetMailbox(unknown) = %v, want ErrNoSuchMailbox", err)
	}
}

func TestUserListMailboxesIncludesInboxAndNotes(t *testing.T) {
	u := &imapUser{username: "alice", store: newTestStore(t)}
	mboxes, err := u.ListMailboxes(false)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(mboxes) != 2 {
		t.Fatalf("ListMailboxes returned %d mailboxes, want 2", len(mboxes))
	}
}

func TestUserMutationsUnsupported(t *testing.T) {
	u := &imapUser{username: "alice", store: newTestStore(t)}
	if err := u.CreateMailbox("Personal"); err == nil {
		t.Error("CreateMailbox should be unsupported")
	}
	if err := u.DeleteMailbox("Notes"); err == nil {
		t.Error("DeleteMailbox should be unsupported")
	}
	if err := u.RenameMailbox("Notes", "Personal"); err == nil {
		t.Error("RenameMailbox should be unsupported")
	}
	if err := u.Logout(); err != nil {
		t.Errorf("Logout: %v", err)
	}
}
