package notesimap

import (
	"strings"

	"github.com/emersion/go-imap/backend"
)

type imapUser struct {
	username string
	store    *store
}

func (u *imapUser) Username() string { return u.username }

func (u *imapUser) ListMailboxes(subscribed bool) ([]backend.Mailbox, error) {
	return []backend.Mailbox{
		&inboxMailbox{},
		&notesMailbox{username: u.username, store: u.store},
	}, nil
}

func (u *imapUser) GetMailbox(name string) (backend.Mailbox, error) {
	switch strings.ToLower(name) {
	case "inbox":
		return &inboxMailbox{}, nil
	case "notes":
		return &notesMailbox{username: u.username, store: u.store}, nil
	default:
		return nil, backend.ErrNoSuchMailbox
	}
}

// CreateMailbox, DeleteMailbox and RenameMailbox are unsupported: the account
// exposes exactly one fixed collection (plus INBOX) matching the web-app's
// single Notes folder, so there is nothing to create/delete/rename.
func (u *imapUser) CreateMailbox(name string) error {
	return backend.ErrMailboxAlreadyExists
}

func (u *imapUser) DeleteMailbox(name string) error {
	return backend.ErrNoSuchMailbox
}

func (u *imapUser) RenameMailbox(existingName, newName string) error {
	return backend.ErrNoSuchMailbox
}

func (u *imapUser) Logout() error { return nil }
