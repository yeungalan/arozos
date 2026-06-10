package applenotessync

/*
	IMAP backend implementation.

	Login authenticates against the arozos auth agent, so devices use the
	same username / password as the arozos web interface.

	Every user gets two mailboxes:
	  INBOX - permanently empty, required for mail clients to accept the account
	  Notes - backed by user:/Document/Notes, where Apple Notes reads and writes
*/

import (
	"errors"
	"os"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
)

type imapBackend struct {
	handler *Handler
}

func (b *imapBackend) Login(connInfo *imap.ConnInfo, username, password string) (backend.User, error) {
	if !b.handler.opts.AuthAgent.ValidateUsernameAndPassword(username, password) {
		return nil, backend.ErrInvalidCredentials
	}
	return &imapUser{handler: b.handler, username: username}, nil
}

type imapUser struct {
	handler  *Handler
	username string
}

func (u *imapUser) Username() string {
	return u.username
}

func (u *imapUser) ListMailboxes(subscribed bool) ([]backend.Mailbox, error) {
	return []backend.Mailbox{
		&emptyMailbox{name: "INBOX"},
		newNotesMailbox(u.handler, u.username),
	}, nil
}

func (u *imapUser) GetMailbox(name string) (backend.Mailbox, error) {
	switch name {
	case "INBOX":
		return &emptyMailbox{name: "INBOX"}, nil
	case "Notes":
		return newNotesMailbox(u.handler, u.username), nil
	default:
		return nil, backend.ErrNoSuchMailbox
	}
}

func (u *imapUser) CreateMailbox(name string) error {
	// The mailboxes we serve always exist; report success so clients that
	// try to create "Notes" on first use proceed normally. Anything else
	// (e.g. Apple Notes folders, which arozos Notes has no concept of) is
	// rejected so notes are never silently stored in a dead mailbox.
	if name == "INBOX" || name == "Notes" {
		return nil
	}
	return errors.New("only the Notes mailbox is supported on this server")
}

func (u *imapUser) DeleteMailbox(name string) error {
	return errors.New("mailboxes on this server cannot be deleted")
}

func (u *imapUser) RenameMailbox(existingName, newName string) error {
	return errors.New("mailboxes on this server cannot be renamed")
}

func (u *imapUser) Logout() error {
	return nil
}

// resolveUserNotesDir returns the real filesystem path of the user's Notes
// directory, creating it if needed.
func (h *Handler) resolveUserNotesDir(username string) (string, error) {
	userinfo, err := h.opts.UserHandler.GetUserInfoFromUsername(username)
	if err != nil {
		return "", err
	}
	homeFSH, err := userinfo.GetHomeFileSystemHandler()
	if err != nil {
		return "", err
	}
	realPath, err := homeFSH.FileSystemAbstraction.VirtualPathToRealPath("user:/Document/Notes", username)
	if err != nil {
		return "", err
	}
	if mkErr := os.MkdirAll(realPath, 0755); mkErr != nil {
		return "", mkErr
	}
	return realPath, nil
}
