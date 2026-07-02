package notesimap

/*
	mailbox.go - the "Notes" IMAP mailbox: each note is presented as one
	message, built on demand from meta.json + the note's .txt file. Sequence
	numbers are recomputed from the notes slice (sorted by UID) on every
	command, the same trade-off the CalDAV handler makes by reloading
	events.json on every request instead of caching mailbox state in memory.
*/

import (
	"bufio"
	"bytes"
	"io"
	"time"

	imap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-imap/backend/backendutil"
	message "github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"
)

const mailboxName = "Notes"

var supportedFlags = []string{imap.SeenFlag, imap.DeletedFlag, imap.FlaggedFlag, imap.AnsweredFlag, imap.DraftFlag}

type notesMailbox struct {
	username string
	store    *store
}

func (mbox *notesMailbox) Name() string { return mailboxName }

func (mbox *notesMailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{Delimiter: "/", Name: mailboxName}, nil
}

func (mbox *notesMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	meta, err := mbox.store.loadMeta(mbox.username)
	if err != nil {
		return nil, err
	}

	status := imap.NewMailboxStatus(mailboxName, items)
	status.Flags = supportedFlags
	status.PermanentFlags = []string{"\\*"}
	status.UnseenSeqNum = firstUnseenSeqNum(meta.Notes)

	for _, item := range items {
		switch item {
		case imap.StatusMessages:
			status.Messages = uint32(len(meta.Notes))
		case imap.StatusUidNext:
			status.UidNext = meta.UidNext
		case imap.StatusUidValidity:
			status.UidValidity = meta.UidValidity
		case imap.StatusRecent:
			status.Recent = 0
		case imap.StatusUnseen:
			status.Unseen = countUnseen(meta.Notes)
		}
	}
	return status, nil
}

func (mbox *notesMailbox) SetSubscribed(bool) error { return nil }

func (mbox *notesMailbox) Check() error { return nil }

func (mbox *notesMailbox) ListMessages(uid bool, seqSet *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)

	meta, err := mbox.store.loadMeta(mbox.username)
	if err != nil {
		return err
	}

	for i, entry := range meta.Notes {
		seqNum := uint32(i + 1)
		id := seqNum
		if uid {
			id = entry.Uid
		}
		if !seqSet.Contains(id) {
			continue
		}

		content, err := mbox.store.readNoteContent(mbox.username, entry.ID)
		if err != nil {
			continue
		}
		raw := buildRawMessage(entry, content, time.UnixMilli(entry.UpdatedAt))
		m, err := fetchNote(entry, raw, seqNum, items)
		if err != nil {
			continue
		}
		ch <- m
	}
	return nil
}

func (mbox *notesMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	meta, err := mbox.store.loadMeta(mbox.username)
	if err != nil {
		return nil, err
	}

	var ids []uint32
	for i, entry := range meta.Notes {
		seqNum := uint32(i + 1)

		content, err := mbox.store.readNoteContent(mbox.username, entry.ID)
		if err != nil {
			continue
		}
		raw := buildRawMessage(entry, content, time.UnixMilli(entry.UpdatedAt))

		ok, err := matchNote(entry, raw, seqNum, criteria)
		if err != nil || !ok {
			continue
		}

		if uid {
			ids = append(ids, entry.Uid)
		} else {
			ids = append(ids, seqNum)
		}
	}
	return ids, nil
}

// CreateMessage handles APPEND. If the incoming message carries an
// X-Universally-Unique-Identifier matching an existing note, that note is
// updated in place (this is how Apple Notes saves an edit: APPEND the new
// version, then STORE \Deleted + EXPUNGE the old one). Otherwise a new note
// is created.
func (mbox *notesMailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	parsed, err := parseIncomingMessage(raw)
	if err != nil {
		return err
	}
	if date.IsZero() {
		date = time.Now()
	}

	return mbox.store.withMeta(mbox.username, func(meta *noteMeta) (bool, error) {
		id := ""
		if parsed.UUID != "" && idPattern.MatchString(parsed.UUID) {
			for _, n := range meta.Notes {
				if n.ID == parsed.UUID {
					id = n.ID
					break
				}
			}
			if id == "" {
				id = parsed.UUID
			}
		} else {
			id = newNoteID(meta)
		}

		if err := mbox.store.writeNoteContent(mbox.username, id, parsed.Content); err != nil {
			return false, err
		}

		persistedFlags := flags
		if len(persistedFlags) == 0 {
			persistedFlags = []string{imap.SeenFlag}
		}
		entry := noteEntry{ID: id, Title: parsed.Title, UpdatedAt: date.UnixMilli(), Flags: persistedFlags}

		found := false
		for i, n := range meta.Notes {
			if n.ID == id {
				entry.Uid = n.Uid
				meta.Notes[i] = entry
				found = true
				break
			}
		}
		if !found {
			entry.Uid = meta.UidNext
			meta.UidNext++
			meta.Notes = append(meta.Notes, entry)
		}
		meta.LastOpened = id
		if meta.Theme == "" {
			meta.Theme = "dark"
		}
		return true, nil
	})
}

func (mbox *notesMailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, op imap.FlagsOp, flags []string) error {
	return mbox.store.withMeta(mbox.username, func(meta *noteMeta) (bool, error) {
		changed := false
		for i, entry := range meta.Notes {
			seqNum := uint32(i + 1)
			id := seqNum
			if uid {
				id = entry.Uid
			}
			if !seqset.Contains(id) {
				continue
			}
			meta.Notes[i].Flags = backendutil.UpdateFlags(entry.Flags, op, flags)
			changed = true
		}
		return changed, nil
	})
}

func (mbox *notesMailbox) CopyMessages(uid bool, seqset *imap.SeqSet, dest string) error {
	// Only one collection ("Notes") exists; there is no valid copy target.
	return backend.ErrNoSuchMailbox
}

// Expunge permanently deletes every note flagged \Deleted, mirroring
// backend/delete.agi: remove the note file, drop it from meta.json, and
// re-pick lastOpened if it pointed at a removed note.
func (mbox *notesMailbox) Expunge() error {
	return mbox.store.withMeta(mbox.username, func(meta *noteMeta) (bool, error) {
		kept := make([]noteEntry, 0, len(meta.Notes))
		removed := false
		for _, entry := range meta.Notes {
			if !hasFlag(entry.Flags, imap.DeletedFlag) {
				kept = append(kept, entry)
				continue
			}
			mbox.store.deleteNoteContent(mbox.username, entry.ID) //nolint - best effort cleanup
			removed = true
			if meta.LastOpened == entry.ID {
				meta.LastOpened = ""
			}
		}
		if !removed {
			return false, nil
		}
		if meta.LastOpened == "" && len(kept) > 0 {
			best := kept[0]
			for _, n := range kept[1:] {
				if n.UpdatedAt > best.UpdatedAt {
					best = n
				}
			}
			meta.LastOpened = best.ID
		}
		meta.Notes = kept
		return true, nil
	})
}

func hasFlag(flags []string, target string) bool {
	for _, f := range flags {
		if f == target {
			return true
		}
	}
	return false
}

func firstUnseenSeqNum(notes []noteEntry) uint32 {
	for i, entry := range notes {
		if !hasFlag(entry.Flags, imap.SeenFlag) {
			return uint32(i + 1)
		}
	}
	return 0
}

func countUnseen(notes []noteEntry) uint32 {
	var n uint32
	for _, entry := range notes {
		if !hasFlag(entry.Flags, imap.SeenFlag) {
			n++
		}
	}
	return n
}

func headerAndBody(raw []byte) (textproto.Header, io.Reader, error) {
	body := bufio.NewReader(bytes.NewReader(raw))
	hdr, err := textproto.ReadHeader(body)
	return hdr, body, err
}

func fetchNote(entry noteEntry, raw []byte, seqNum uint32, items []imap.FetchItem) (*imap.Message, error) {
	fetched := imap.NewMessage(seqNum, items)
	for _, item := range items {
		switch item {
		case imap.FetchEnvelope:
			hdr, _, _ := headerAndBody(raw)
			fetched.Envelope, _ = backendutil.FetchEnvelope(hdr)
		case imap.FetchBody, imap.FetchBodyStructure:
			hdr, body, _ := headerAndBody(raw)
			fetched.BodyStructure, _ = backendutil.FetchBodyStructure(hdr, body, item == imap.FetchBodyStructure)
		case imap.FetchFlags:
			fetched.Flags = entry.Flags
		case imap.FetchInternalDate:
			fetched.InternalDate = time.UnixMilli(entry.UpdatedAt)
		case imap.FetchRFC822Size:
			fetched.Size = uint32(len(raw))
		case imap.FetchUid:
			fetched.Uid = entry.Uid
		default:
			section, err := imap.ParseBodySectionName(item)
			if err != nil {
				break
			}
			hdr, body, err := headerAndBody(raw)
			if err != nil {
				return nil, err
			}
			l, _ := backendutil.FetchBodySection(hdr, body, section)
			fetched.Body[section] = l
		}
	}
	return fetched, nil
}

func matchNote(entry noteEntry, raw []byte, seqNum uint32, c *imap.SearchCriteria) (bool, error) {
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return false, err
	}
	return backendutil.Match(e, seqNum, entry.Uid, time.UnixMilli(entry.UpdatedAt), entry.Flags, c)
}
