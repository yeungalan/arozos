package notesimap

import (
	"testing"

	imap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
)

type fakeValidator struct {
	tokens map[string]string // token -> owner username
	users  map[string]bool
}

func (f *fakeValidator) ValidateAutoLoginToken(token string) (bool, string) {
	owner, ok := f.tokens[token]
	return ok, owner
}

func (f *fakeValidator) UserExists(username string) bool { return f.users[username] }

func TestBackendLoginValidToken(t *testing.T) {
	be := &imapBackend{
		authAgent: &fakeValidator{
			tokens: map[string]string{"tok-alice": "alice"},
			users:  map[string]bool{"alice": true},
		},
		store: newTestStore(t),
	}

	u, err := be.Login(&imap.ConnInfo{}, "alice", "tok-alice")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if u.Username() != "alice" {
		t.Errorf("Username = %q, want alice", u.Username())
	}
}

func TestBackendLoginWrongOwner(t *testing.T) {
	be := &imapBackend{
		authAgent: &fakeValidator{
			tokens: map[string]string{"tok-alice": "alice"},
			users:  map[string]bool{"alice": true, "eve": true},
		},
		store: newTestStore(t),
	}

	_, err := be.Login(&imap.ConnInfo{}, "eve", "tok-alice")
	if err != backend.ErrInvalidCredentials {
		t.Fatalf("Login with mismatched owner = %v, want ErrInvalidCredentials", err)
	}
}

func TestBackendLoginUnknownToken(t *testing.T) {
	be := &imapBackend{
		authAgent: &fakeValidator{tokens: map[string]string{}, users: map[string]bool{"alice": true}},
		store:     newTestStore(t),
	}

	_, err := be.Login(&imap.ConnInfo{}, "alice", "bogus")
	if err != backend.ErrInvalidCredentials {
		t.Fatalf("Login with bogus token = %v, want ErrInvalidCredentials", err)
	}
}

func TestBackendLoginUserDoesNotExist(t *testing.T) {
	be := &imapBackend{
		authAgent: &fakeValidator{
			tokens: map[string]string{"tok-ghost": "ghost"},
			users:  map[string]bool{},
		},
		store: newTestStore(t),
	}

	_, err := be.Login(&imap.ConnInfo{}, "ghost", "tok-ghost")
	if err != backend.ErrInvalidCredentials {
		t.Fatalf("Login for nonexistent user = %v, want ErrInvalidCredentials", err)
	}
}
