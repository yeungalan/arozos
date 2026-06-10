package applenotessync

/*
	The "Notes" mailbox, generated on demand from the user's arozos notes.

	Mapping model: every arozos note is exposed as one immutable IMAP
	message. When a note changes on the arozos side, the old message
	disappears (as if expunged) and a new message with a fresh UID — but
	the same X-Universally-Unique-Identifier — replaces it. Apple Notes
	uses that UUID header to recognise it as an edit of the same note.

	Apple edits work the same way in reverse: the device APPENDs a new
	message carrying the existing UUID, flags the old one \Deleted and
	EXPUNGEs it. On APPEND we update the underlying .txt + meta.json, on
	EXPUNGE we only delete the arozos note when no other message still
	references it.
*/

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/backendutil"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/textproto"
	uuidlib "github.com/satori/go.uuid"
)

// msgEntry is one IMAP message backed by (a version of) an arozos note.
type msgEntry struct {
	UID          uint32   `json:"uid"`
	NoteID       string   `json:"noteId"`
	UUID         string   `json:"uuid"`
	Flags        []string `json:"flags"`
	InternalDate int64    `json:"internalDate"` // unix ms
	Hash         string   `json:"hash"`         // md5 of the plain-text content
	Raw          []byte   `json:"raw"`          // full RFC 2822 message
}

// mailboxState is the persisted per-user IMAP view.
type mailboxState struct {
	UIDValidity uint32     `json:"uidValidity"`
	UIDNext     uint32     `json:"uidNext"`
	Entries     []msgEntry `json:"entries"`
}

// notesMeta mirrors the meta.json maintained by the Notes app AGI scripts.
type notesMeta struct {
	LastOpened string     `json:"lastOpened"`
	Theme      string     `json:"theme"`
	Notes      []noteMeta `json:"notes"`
}

type noteMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

type notesMailbox struct {
	handler  *Handler
	username string
}

func newNotesMailbox(h *Handler, username string) *notesMailbox {
	return &notesMailbox{handler: h, username: username}
}

func (m *notesMailbox) Name() string { return "Notes" }

func (m *notesMailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{Delimiter: "/", Name: "Notes"}, nil
}

// ── State persistence ─────────────────────────────────────────────────────────

func (m *notesMailbox) loadState() *mailboxState {
	var st mailboxState
	m.handler.opts.Database.Read(dbTable, m.username+":state", &st)
	if st.UIDValidity == 0 {
		st.UIDValidity = uint32(time.Now().Unix())
		st.UIDNext = 1
	}
	if st.Entries == nil {
		st.Entries = []msgEntry{}
	}
	return &st
}

func (m *notesMailbox) saveState(st *mailboxState) {
	sort.Slice(st.Entries, func(i, j int) bool { return st.Entries[i].UID < st.Entries[j].UID })
	m.handler.opts.Database.Write(dbTable, m.username+":state", st)
}

// ── Reconcile: regenerate the IMAP view from the notes on disk ────────────────

// reconcile makes the message list reflect the current state of the user's
// notes. Caller must hold the user lock.
func (m *notesMailbox) reconcile(st *mailboxState) error {
	dir, err := m.handler.notesDirResolver(m.username)
	if err != nil {
		return err
	}
	meta := loadNotesMeta(dir)

	// Latest (highest-UID) entry per note: that is the entry representing
	// the current version. Older entries for the same note are versions
	// pending expunge by the client — leave them alone.
	latest := map[string]int{}
	for i, e := range st.Entries {
		if cur, ok := latest[e.NoteID]; !ok || st.Entries[cur].UID < e.UID {
			latest[e.NoteID] = i
		}
	}

	changed := false
	liveNotes := map[string]bool{}

	for _, nm := range meta.Notes {
		raw, err := os.ReadFile(filepath.Join(dir, nm.ID+".txt"))
		if err != nil {
			continue // metadata entry without a file — not served
		}
		liveNotes[nm.ID] = true
		content := string(raw)
		hash := contentHash(content)

		idx, exists := latest[nm.ID]
		if exists && st.Entries[idx].Hash == hash {
			continue // unchanged
		}

		title := nm.Title
		if title == "" {
			title = derivedTitle("", content)
		}
		updated := time.UnixMilli(nm.UpdatedAt)
		if nm.UpdatedAt <= 0 {
			updated = time.Now()
		}

		noteUUID := strings.ToUpper(uuidlib.NewV4().String())
		if exists {
			// Edited note: keep the UUID so Apple treats it as the same note,
			// and drop the superseded message.
			noteUUID = st.Entries[idx].UUID
			st.Entries = append(st.Entries[:idx], st.Entries[idx+1:]...)
			// latest indexes are invalidated by the removal; rebuild below
		}

		st.Entries = append(st.Entries, msgEntry{
			UID:          st.UIDNext,
			NoteID:       nm.ID,
			UUID:         noteUUID,
			Flags:        []string{imap.SeenFlag},
			InternalDate: updated.UnixMilli(),
			Hash:         hash,
			Raw:          buildNoteMessage(noteUUID, title, content, m.username, updated),
		})
		st.UIDNext++
		changed = true

		// Rebuild the latest index after mutation
		latest = map[string]int{}
		for i, e := range st.Entries {
			if cur, ok := latest[e.NoteID]; !ok || st.Entries[cur].UID < e.UID {
				latest[e.NoteID] = i
			}
		}
	}

	// Remove messages whose note was deleted on the arozos side
	kept := st.Entries[:0]
	for _, e := range st.Entries {
		if liveNotes[e.NoteID] {
			kept = append(kept, e)
		} else {
			changed = true
		}
	}
	st.Entries = kept

	if changed {
		m.saveState(st)
	}
	return nil
}

// ── Mailbox interface ─────────────────────────────────────────────────────────

func (m *notesMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	lock := m.handler.lockUser(m.username)
	lock.Lock()
	st := m.loadState()
	m.reconcile(st)
	lock.Unlock()

	status := imap.NewMailboxStatus("Notes", items)
	status.Flags = []string{
		imap.SeenFlag, imap.AnsweredFlag, imap.FlaggedFlag,
		imap.DeletedFlag, imap.DraftFlag,
	}
	status.PermanentFlags = []string{"\\*"}
	status.UnseenSeqNum = 0
	for i, e := range st.Entries {
		if !hasFlag(e.Flags, imap.SeenFlag) {
			status.UnseenSeqNum = uint32(i + 1)
			break
		}
	}

	for _, item := range items {
		switch item {
		case imap.StatusMessages:
			status.Messages = uint32(len(st.Entries))
		case imap.StatusUidNext:
			status.UidNext = st.UIDNext
		case imap.StatusUidValidity:
			status.UidValidity = st.UIDValidity
		case imap.StatusRecent:
			status.Recent = 0
		case imap.StatusUnseen:
			n := uint32(0)
			for _, e := range st.Entries {
				if !hasFlag(e.Flags, imap.SeenFlag) {
					n++
				}
			}
			status.Unseen = n
		}
	}
	return status, nil
}

func (m *notesMailbox) SetSubscribed(subscribed bool) error { return nil }
func (m *notesMailbox) Check() error                        { return nil }

func (m *notesMailbox) ListMessages(uid bool, seqset *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)

	lock := m.handler.lockUser(m.username)
	lock.Lock()
	st := m.loadState()
	m.reconcile(st)
	lock.Unlock()

	for i, e := range st.Entries {
		seqNum := uint32(i + 1)
		id := seqNum
		if uid {
			id = e.UID
		}
		if !seqset.Contains(id) {
			continue
		}
		msg, err := fetchEntry(&e, seqNum, items)
		if err != nil {
			continue
		}
		ch <- msg
	}
	return nil
}

func fetchEntry(e *msgEntry, seqNum uint32, items []imap.FetchItem) (*imap.Message, error) {
	fetched := imap.NewMessage(seqNum, items)
	for _, item := range items {
		switch item {
		case imap.FetchEnvelope:
			hdr, _, err := entryHeaderAndBody(e)
			if err != nil {
				return nil, err
			}
			fetched.Envelope, _ = backendutil.FetchEnvelope(hdr)
		case imap.FetchBody, imap.FetchBodyStructure:
			hdr, body, err := entryHeaderAndBody(e)
			if err != nil {
				return nil, err
			}
			fetched.BodyStructure, _ = backendutil.FetchBodyStructure(hdr, body, item == imap.FetchBodyStructure)
		case imap.FetchFlags:
			fetched.Flags = e.Flags
		case imap.FetchInternalDate:
			fetched.InternalDate = time.UnixMilli(e.InternalDate)
		case imap.FetchRFC822Size:
			fetched.Size = uint32(len(e.Raw))
		case imap.FetchUid:
			fetched.Uid = e.UID
		default:
			section, err := imap.ParseBodySectionName(item)
			if err != nil {
				break
			}
			hdr, body, err := entryHeaderAndBody(e)
			if err != nil {
				return nil, err
			}
			l, _ := backendutil.FetchBodySection(hdr, body, section)
			fetched.Body[section] = l
		}
	}
	return fetched, nil
}

func entryHeaderAndBody(e *msgEntry) (textproto.Header, io.Reader, error) {
	body := bufio.NewReader(bytes.NewReader(e.Raw))
	hdr, err := textproto.ReadHeader(body)
	return hdr, body, err
}

func (m *notesMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	lock := m.handler.lockUser(m.username)
	lock.Lock()
	st := m.loadState()
	m.reconcile(st)
	lock.Unlock()

	ids := []uint32{}
	for i, e := range st.Entries {
		seqNum := uint32(i + 1)
		ent, err := message.Read(bytes.NewReader(e.Raw))
		if err != nil && !message.IsUnknownCharset(err) {
			continue
		}
		ok, err := backendutil.Match(ent, seqNum, e.UID, time.UnixMilli(e.InternalDate), e.Flags, criteria)
		if err != nil || !ok {
			continue
		}
		if uid {
			ids = append(ids, e.UID)
		} else {
			ids = append(ids, seqNum)
		}
	}
	return ids, nil
}

// CreateMessage handles APPEND: a note created or edited on the Apple side.
func (m *notesMailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	parsed, err := parseNoteMessage(raw)
	if err != nil {
		return errors.New("unparsable message: " + err.Error())
	}
	if date.IsZero() {
		date = time.Now()
	}
	flags = withoutFlag(flags, imap.RecentFlag)
	if !hasFlag(flags, imap.SeenFlag) {
		flags = append(flags, imap.SeenFlag)
	}

	lock := m.handler.lockUser(m.username)
	lock.Lock()
	defer lock.Unlock()

	dir, err := m.handler.notesDirResolver(m.username)
	if err != nil {
		return err
	}
	st := m.loadState()
	m.reconcile(st)

	// An APPEND carrying a known UUID is an edit of that note
	noteID := ""
	if parsed.UUID != "" {
		for _, e := range st.Entries {
			if strings.EqualFold(e.UUID, parsed.UUID) {
				noteID = e.NoteID
				break
			}
		}
	}

	noteUUID := strings.ToUpper(parsed.UUID)
	if noteUUID == "" {
		noteUUID = strings.ToUpper(uuidlib.NewV4().String())
	}
	if noteID == "" {
		noteID = "apple_" + sanitizeNoteID(noteUUID)
	}

	if err := os.WriteFile(filepath.Join(dir, noteID+".txt"), []byte(parsed.Content), 0644); err != nil {
		return err
	}

	meta := loadNotesMeta(dir)
	upsertMetaEntry(meta, noteID, parsed.Title, date.UnixMilli())
	if err := saveNotesMeta(dir, meta); err != nil {
		return err
	}

	st.Entries = append(st.Entries, msgEntry{
		UID:          st.UIDNext,
		NoteID:       noteID,
		UUID:         noteUUID,
		Flags:        flags,
		InternalDate: date.UnixMilli(),
		Hash:         contentHash(parsed.Content),
		Raw:          raw,
	})
	st.UIDNext++
	m.saveState(st)
	return nil
}

func (m *notesMailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, op imap.FlagsOp, flags []string) error {
	lock := m.handler.lockUser(m.username)
	lock.Lock()
	defer lock.Unlock()

	st := m.loadState()
	for i := range st.Entries {
		seqNum := uint32(i + 1)
		id := seqNum
		if uid {
			id = st.Entries[i].UID
		}
		if !seqset.Contains(id) {
			continue
		}
		st.Entries[i].Flags = backendutil.UpdateFlags(st.Entries[i].Flags, op, flags)
	}
	m.saveState(st)
	return nil
}

func (m *notesMailbox) CopyMessages(uid bool, seqset *imap.SeqSet, dest string) error {
	return errors.New("copying notes between mailboxes is not supported")
}

// Expunge removes \Deleted messages. The underlying arozos note is only
// deleted when no remaining message references it (so the APPEND-new /
// delete-old edit flow of Apple Notes never destroys the note).
func (m *notesMailbox) Expunge() error {
	lock := m.handler.lockUser(m.username)
	lock.Lock()
	defer lock.Unlock()

	dir, err := m.handler.notesDirResolver(m.username)
	if err != nil {
		return err
	}
	st := m.loadState()

	kept := []msgEntry{}
	removed := []msgEntry{}
	for _, e := range st.Entries {
		if hasFlag(e.Flags, imap.DeletedFlag) {
			removed = append(removed, e)
		} else {
			kept = append(kept, e)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	st.Entries = kept

	stillReferenced := map[string]bool{}
	for _, e := range kept {
		stillReferenced[e.NoteID] = true
	}

	meta := loadNotesMeta(dir)
	metaChanged := false
	for _, e := range removed {
		if stillReferenced[e.NoteID] {
			continue // an edit superseded this message; the note lives on
		}
		os.Remove(filepath.Join(dir, e.NoteID+".txt"))
		if removeMetaEntry(meta, e.NoteID) {
			metaChanged = true
		}
	}
	if metaChanged {
		saveNotesMeta(dir, meta)
	}

	m.saveState(st)
	return nil
}

// ── INBOX: a permanently empty mailbox so mail clients accept the account ────

type emptyMailbox struct {
	name string
}

func (m *emptyMailbox) Name() string { return m.name }

func (m *emptyMailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{Delimiter: "/", Name: m.name}, nil
}

func (m *emptyMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	status := imap.NewMailboxStatus(m.name, items)
	status.Flags = []string{imap.SeenFlag, imap.DeletedFlag}
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

func (m *emptyMailbox) SetSubscribed(bool) error { return nil }
func (m *emptyMailbox) Check() error             { return nil }

func (m *emptyMailbox) ListMessages(uid bool, seqset *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	close(ch)
	return nil
}

func (m *emptyMailbox) SearchMessages(uid bool, criteria *imap.SearchCriteria) ([]uint32, error) {
	return []uint32{}, nil
}

func (m *emptyMailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	return errors.New(m.name + " on this server is read-only; only the Notes mailbox stores messages")
}

func (m *emptyMailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, op imap.FlagsOp, flags []string) error {
	return nil
}

func (m *emptyMailbox) CopyMessages(uid bool, seqset *imap.SeqSet, dest string) error {
	return errors.New("not supported")
}

func (m *emptyMailbox) Expunge() error { return nil }

// ── meta.json helpers ─────────────────────────────────────────────────────────

func loadNotesMeta(dir string) *notesMeta {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return &notesMeta{Theme: "dark", Notes: []noteMeta{}}
	}
	var m notesMeta
	if json.Unmarshal(data, &m) != nil {
		return &notesMeta{Theme: "dark", Notes: []noteMeta{}}
	}
	if m.Notes == nil {
		m.Notes = []noteMeta{}
	}
	return &m
}

func saveNotesMeta(dir string, m *notesMeta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), data, 0644)
}

func upsertMetaEntry(meta *notesMeta, id, title string, ts int64) {
	for i, n := range meta.Notes {
		if n.ID == id {
			meta.Notes[i].Title = title
			meta.Notes[i].UpdatedAt = ts
			return
		}
	}
	meta.Notes = append(meta.Notes, noteMeta{ID: id, Title: title, UpdatedAt: ts})
}

func removeMetaEntry(meta *notesMeta, id string) bool {
	for i, n := range meta.Notes {
		if n.ID == id {
			meta.Notes = append(meta.Notes[:i], meta.Notes[i+1:]...)
			return true
		}
	}
	return false
}

// ── misc helpers ──────────────────────────────────────────────────────────────

var noteIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeNoteID(s string) string {
	return noteIDSanitizer.ReplaceAllString(s, "")
}

func hasFlag(flags []string, flag string) bool {
	for _, f := range flags {
		if strings.EqualFold(f, flag) {
			return true
		}
	}
	return false
}

func withoutFlag(flags []string, flag string) []string {
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		if !strings.EqualFold(f, flag) {
			out = append(out, f)
		}
	}
	return out
}
