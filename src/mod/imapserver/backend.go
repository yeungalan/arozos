// Package imapserver implements an IMAP4rev1 server that exposes arozos Notes
// as an IMAP mailbox so Apple Notes (and any other IMAP client) can sync with it.
package imapserver

import (
	"fmt"

	imap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
)

// AuthFunc validates an arozos username and password.
type AuthFunc func(username, password string) bool

// NotesDirFunc resolves "user:/Document/Notes" to an absolute OS path.
type NotesDirFunc func(username string) (string, error)

// ArozosBackend implements the go-imap Backend interface.
type ArozosBackend struct {
	authenticate AuthFunc
	notesDir     NotesDirFunc
}

// NewBackend creates a new ArozosBackend.
func NewBackend(auth AuthFunc, nd NotesDirFunc) *ArozosBackend {
	return &ArozosBackend{authenticate: auth, notesDir: nd}
}

// Login implements backend.Backend.
func (b *ArozosBackend) Login(_ *imap.ConnInfo, username, password string) (backend.User, error) {
	if !b.authenticate(username, password) {
		return nil, backend.ErrInvalidCredentials
	}
	dir, err := b.notesDir(username)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve notes directory: %w", err)
	}
	return &ArozosUser{username: username, notesDir: dir}, nil
}

// ArozosUser implements backend.User.
type ArozosUser struct {
	username string
	notesDir string
}

func (u *ArozosUser) Username() string { return u.username }

func (u *ArozosUser) ListMailboxes(subscribed bool) ([]backend.Mailbox, error) {
	return []backend.Mailbox{
		newInboxMailbox(),
		newNotesMailbox(u.username, u.notesDir),
	}, nil
}

func (u *ArozosUser) GetMailbox(name string) (backend.Mailbox, error) {
	switch canonicalMailboxName(name) {
	case "INBOX":
		return newInboxMailbox(), nil
	case "NOTES":
		return newNotesMailbox(u.username, u.notesDir), nil
	}
	return nil, backend.ErrNoSuchMailbox
}

// Apple Notes creates the Notes folder on first connect if it doesn't exist.
// We accept the call silently since our Notes mailbox always exists.
func (u *ArozosUser) CreateMailbox(_ string) error { return nil }

func (u *ArozosUser) DeleteMailbox(_ string) error { return nil }

func (u *ArozosUser) RenameMailbox(_, _ string) error { return nil }

func (u *ArozosUser) Logout() error { return nil }
