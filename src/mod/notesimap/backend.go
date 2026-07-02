package notesimap

/*
	backend.go - IMAP LOGIN authentication.

	Following the CalDAV handler's convention: username is the ArozOS
	username, and password is an ArozOS auto-login token for that user (not
	their account password), so Notes sync credentials can be revoked
	independently of the user's login password.
*/

import (
	imap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"imuslab.com/arozos/mod/auth"
	"imuslab.com/arozos/mod/user"
)

// credentialValidator is the slice of *auth.AuthAgent this package depends
// on, factored into an interface so tests can supply a fake instead of
// standing up a full AuthAgent.
type credentialValidator interface {
	ValidateAutoLoginToken(token string) (bool, string)
	UserExists(username string) bool
}

type imapBackend struct {
	authAgent credentialValidator
	store     *store
}

func newBackend(authAgent *auth.AuthAgent, userHandler *user.UserHandler) *imapBackend {
	return &imapBackend{
		authAgent: authAgent,
		store:     newStore(userHandler),
	}
}

func (b *imapBackend) Login(connInfo *imap.ConnInfo, username, password string) (backend.User, error) {
	valid, tokenOwner := b.authAgent.ValidateAutoLoginToken(password)
	if !valid || tokenOwner != username {
		return nil, backend.ErrInvalidCredentials
	}
	if !b.authAgent.UserExists(username) {
		return nil, backend.ErrInvalidCredentials
	}
	return &imapUser{username: username, store: b.store}, nil
}
