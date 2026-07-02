package notesimap

/*
	inbox.go - a permanently-empty INBOX. Some IMAP account setup wizards
	(including iOS Mail/Notes account validation) expect every account to
	have an INBOX; this satisfies that check without exposing any mail.
*/

import (
	"errors"
	"time"

	imap "github.com/emersion/go-imap"
)

var errInboxReadOnly = errors.New("INBOX is not used for notes syncing")

type inboxMailbox struct{}

func (mbox *inboxMailbox) Name() string { return "INBOX" }

func (mbox *inboxMailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{Delimiter: "/", Name: "INBOX"}, nil
}

func (mbox *inboxMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	status := imap.NewMailboxStatus("INBOX", items)
	status.Flags = supportedFlags
	status.PermanentFlags = []string{"\\*"}
	status.UnseenSeqNum = 0
	for _, item := range items {
		switch item {
		case imap.StatusMessages:
			status.Messages = 0
		case imap.StatusUidNext:
			status.UidNext = 1
		case imap.StatusUidValidity:
			status.UidValidity = 1
		case imap.StatusRecent:
			status.Recent = 0
		case imap.StatusUnseen:
			status.Unseen = 0
		}
	}
	return status, nil
}

func (mbox *inboxMailbox) SetSubscribed(bool) error { return nil }

func (mbox *inboxMailbox) Check() error { return nil }

func (mbox *inboxMailbox) ListMessages(uid bool, seqSet *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	close(ch)
	return nil
}

func (mbox *inboxMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	return nil, nil
}

func (mbox *inboxMailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	return errInboxReadOnly
}

func (mbox *inboxMailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, op imap.FlagsOp, flags []string) error {
	return errInboxReadOnly
}

func (mbox *inboxMailbox) CopyMessages(uid bool, seqset *imap.SeqSet, dest string) error {
	return errInboxReadOnly
}

func (mbox *inboxMailbox) Expunge() error { return nil }
