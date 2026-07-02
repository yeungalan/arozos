package notesimap

/*
	integration_test.go - drives a real emersion/go-imap client against a
	real server.Server bound to a loopback port, backed by imapBackend. This
	exercises the whole wire protocol (LOGIN, SELECT, APPEND, FETCH, STORE,
	EXPUNGE, LOGOUT) instead of only the Go-level backend.Mailbox methods.
*/

import (
	"bytes"
	"net"
	"testing"
	"time"

	imap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
)

// startTestServer boots a real IMAP server on a loopback port backed by a
// temp-dir store, and returns a connected, ready-to-use client. The client
// and listener are closed automatically at test cleanup.
func startTestServer(t *testing.T, validUsers map[string]string) (*client.Client, *store) {
	t.Helper()

	st := newTestStore(t)
	be := &imapBackend{
		authAgent: &fakeValidator{
			tokens: validUsers,
			users:  usernamesFromTokens(validUsers),
		},
		store: st,
	}

	s := server.New(be)
	s.AllowInsecureAuth = true

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	go s.Serve(listener) //nolint - stopped via listener.Close() in cleanup

	t.Cleanup(func() {
		listener.Close()
	})

	c, err := client.Dial(listener.Addr().String())
	if err != nil {
		t.Fatalf("client.Dial: %v", err)
	}
	t.Cleanup(func() { c.Logout() })

	return c, st
}

func usernamesFromTokens(tokens map[string]string) map[string]bool {
	users := map[string]bool{}
	for _, owner := range tokens {
		users[owner] = true
	}
	return users
}

func TestIntegrationLoginSelectAppendFetch(t *testing.T) {
	c, st := startTestServer(t, map[string]string{"tok-alice": "alice"})

	if err := c.Login("alice", "tok-alice"); err != nil {
		t.Fatalf("Login: %v", err)
	}

	mboxStatus, err := c.Select("Notes", false)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if mboxStatus.Messages != 0 {
		t.Fatalf("expected empty mailbox, got %d messages", mboxStatus.Messages)
	}

	raw := buildRawMessage(noteEntry{Title: "Grocery List"}, "Milk\nEggs\nBread", time.Now())
	if err := c.Append("Notes", []string{imap.SeenFlag}, time.Now(), bytes.NewReader(raw)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// A real client re-selects (or STATUS) after APPEND to see the new count.
	mboxStatus, err = c.Select("Notes", false)
	if err != nil {
		t.Fatalf("Select (after append): %v", err)
	}
	if mboxStatus.Messages != 1 {
		t.Fatalf("Messages after append = %d, want 1", mboxStatus.Messages)
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(1)
	ch := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- c.Fetch(seqSet, []imap.FetchItem{imap.FetchEnvelope, imap.FetchUid}, ch) }()

	msg := <-ch
	if msg == nil {
		t.Fatal("expected one fetched message, got none")
	}
	if msg.Envelope == nil || msg.Envelope.Subject != "Grocery List" {
		t.Errorf("Envelope = %+v, want Subject Grocery List", msg.Envelope)
	}
	if err := <-done; err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Confirm the note actually landed on disk via the same files the web-app uses.
	meta, err := st.loadMeta("alice")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if len(meta.Notes) != 1 || meta.Notes[0].Title != "Grocery List" {
		t.Fatalf("expected note persisted in meta.json, got %+v", meta.Notes)
	}
	content, err := st.readNoteContent("alice", meta.Notes[0].ID)
	if err != nil || content != "Milk\nEggs\nBread" {
		t.Fatalf("readNoteContent = (%q, %v)", content, err)
	}
}

func TestIntegrationLoginRejectsBadToken(t *testing.T) {
	c, _ := startTestServer(t, map[string]string{"tok-alice": "alice"})

	if err := c.Login("alice", "wrong-token"); err == nil {
		t.Fatal("expected Login with wrong token to fail")
	}
}

func TestIntegrationDeleteViaStoreAndExpunge(t *testing.T) {
	c, st := startTestServer(t, map[string]string{"tok-bob": "bob"})
	if err := c.Login("bob", "tok-bob"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := c.Select("Notes", false); err != nil {
		t.Fatalf("Select: %v", err)
	}

	raw := buildRawMessage(noteEntry{Title: "Temp"}, "temporary", time.Now())
	if err := c.Append("Notes", nil, time.Now(), bytes.NewReader(raw)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := c.Select("Notes", false); err != nil {
		t.Fatalf("Select (after append): %v", err)
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(1)
	if err := c.Store(seqSet, imap.FormatFlagsOp(imap.AddFlags, false), []interface{}{imap.DeletedFlag}, nil); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := c.Expunge(nil); err != nil {
		t.Fatalf("Expunge: %v", err)
	}

	meta, err := st.loadMeta("bob")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if len(meta.Notes) != 0 {
		t.Fatalf("expected note to be expunged, got %+v", meta.Notes)
	}
}
