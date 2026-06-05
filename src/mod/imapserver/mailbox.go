package imapserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	imap "github.com/emersion/go-imap"
)

// ── shared helpers ────────────────────────────────────────────────────────────

func canonicalMailboxName(name string) string {
	return strings.ToUpper(name)
}

// ── on-disk metadata structures ──────────────────────────────────────────────

type noteMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"` // ms since epoch
}

type notesMeta struct {
	LastOpened string     `json:"lastOpened"`
	Theme      string     `json:"theme"`
	Notes      []noteMeta `json:"notes"`
}

type uidEntry struct {
	NoteID  string   `json:"id"`
	UID     uint32   `json:"uid"`
	Flags   []string `json:"flags"`
	Deleted bool     `json:"deleted"`
}

type uidStore struct {
	Validity uint32     `json:"validity"`
	Next     uint32     `json:"next"`
	Entries  []uidEntry `json:"entries"`
}

// ── INBOX mailbox (always empty) ─────────────────────────────────────────────

type inboxMailbox struct{}

func newInboxMailbox() *inboxMailbox { return &inboxMailbox{} }

func (m *inboxMailbox) Name() string { return "INBOX" }

func (m *inboxMailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{
		Attributes: []string{imap.NoInferiorsAttr},
		Delimiter:  "/",
		Name:       "INBOX",
	}, nil
}

func (m *inboxMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	s := imap.NewMailboxStatus("INBOX", items)
	s.Flags = []string{imap.SeenFlag, imap.DeletedFlag}
	s.PermanentFlags = []string{imap.SeenFlag, imap.DeletedFlag}
	s.UidValidity = 1
	s.UidNext = 1
	s.Messages = 0
	s.Recent = 0
	s.Unseen = 0
	return s, nil
}

func (m *inboxMailbox) SetSubscribed(_ bool) error { return nil }
func (m *inboxMailbox) Check() error               { return nil }

func (m *inboxMailbox) ListMessages(_ bool, _ *imap.SeqSet, _ []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)
	return nil
}

func (m *inboxMailbox) SearchMessages(_ bool, _ *imap.SearchCriteria) ([]uint32, error) {
	return nil, nil
}

func (m *inboxMailbox) CreateMessage(_ []string, _ time.Time, body imap.Literal) error {
	io.Copy(io.Discard, body) //nolint
	return nil
}

func (m *inboxMailbox) UpdateMessagesFlags(_ bool, _ *imap.SeqSet, _ imap.FlagsOp, _ []string) error {
	return nil
}

func (m *inboxMailbox) CopyMessages(_ bool, _ *imap.SeqSet, _ string) error { return nil }
func (m *inboxMailbox) Expunge() error                                       { return nil }

// ── Notes mailbox ─────────────────────────────────────────────────────────────

type notesMailbox struct {
	mu       sync.Mutex
	username string
	dir      string // absolute OS path to the Notes directory
}

func newNotesMailbox(username, dir string) *notesMailbox {
	return &notesMailbox{username: username, dir: dir}
}

func (m *notesMailbox) Name() string { return "Notes" }

func (m *notesMailbox) Info() (*imap.MailboxInfo, error) {
	return &imap.MailboxInfo{
		Attributes: []string{},
		Delimiter:  "/",
		Name:       "Notes",
	}, nil
}

func (m *notesMailbox) Status(items []imap.StatusItem) (*imap.MailboxStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	meta, _ := m.readMeta()
	uid := m.loadUID(meta)

	s := imap.NewMailboxStatus("Notes", items)
	s.Flags = []string{imap.SeenFlag, imap.DeletedFlag, imap.FlaggedFlag}
	s.PermanentFlags = []string{imap.SeenFlag, imap.DeletedFlag, imap.FlaggedFlag}
	s.UidValidity = uid.Validity
	s.UidNext = uid.Next

	active := activeEntries(uid)
	s.Messages = uint32(len(active))
	s.Recent = 0

	var unseen uint32
	for _, e := range active {
		if !hasFlag(e.Flags, imap.SeenFlag) {
			unseen++
		}
	}
	s.Unseen = unseen
	return s, nil
}

func (m *notesMailbox) SetSubscribed(_ bool) error { return nil }
func (m *notesMailbox) Check() error               { return nil }

func (m *notesMailbox) ListMessages(uid bool, seqset *imap.SeqSet, items []imap.FetchItem, ch chan<- *imap.Message) error {
	defer close(ch)

	m.mu.Lock()
	defer m.mu.Unlock()

	meta, _ := m.readMeta()
	uidst := m.loadUID(meta)
	active := activeEntries(uidst)

	for i, entry := range active {
		seqNum := uint32(i + 1)
		id := seqNum
		if uid {
			id = entry.UID
		}
		if !seqset.Contains(id) {
			continue
		}

		nm := findNoteMeta(meta.Notes, entry.NoteID)
		title := "Note"
		var noteDate time.Time
		if nm != nil {
			title = nm.Title
			noteDate = time.UnixMilli(nm.UpdatedAt)
		}
		if noteDate.IsZero() {
			noteDate = time.Now()
		}

		content, _ := m.readNote(entry.NoteID)
		rawMsg := buildRawMessage(m.username, title, content, noteDate)

		msg := imap.NewMessage(seqNum, items)
		for _, item := range items {
			switch item {
			case imap.FetchEnvelope:
				addr := &imap.Address{
					MailboxName: m.username,
					HostName:    "arozos.local",
				}
				msg.Envelope = &imap.Envelope{
					Date:    noteDate,
					Subject: title,
					From:    []*imap.Address{addr},
					To:      []*imap.Address{addr},
				}
			case imap.FetchFlags:
				msg.Flags = entry.Flags
				if len(msg.Flags) == 0 {
					msg.Flags = []string{imap.SeenFlag}
				}
			case imap.FetchInternalDate:
				msg.InternalDate = noteDate
			case imap.FetchRFC822Size:
				msg.Size = uint32(len(rawMsg))
			case imap.FetchUid:
				msg.Uid = entry.UID
			default:
				section, err := imap.ParseBodySectionName(item)
				if err != nil {
					continue
				}
				msg.Body[section] = bytes.NewReader(extractSection(rawMsg, section))
			}
		}
		ch <- msg
	}
	return nil
}

func (m *notesMailbox) SearchMessages(uid bool, _ *imap.SearchCriteria) ([]uint32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	meta, _ := m.readMeta()
	uidst := m.loadUID(meta)
	active := activeEntries(uidst)

	result := make([]uint32, len(active))
	for i, e := range active {
		if uid {
			result[i] = e.UID
		} else {
			result[i] = uint32(i + 1)
		}
	}
	return result, nil
}

// CreateMessage is called by Apple Notes when the user creates a new note.
func (m *notesMailbox) CreateMessage(flags []string, date time.Time, body imap.Literal) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	title, content, msgDate := parseRawMessage(raw)
	if date.IsZero() {
		date = msgDate
	}
	if date.IsZero() {
		date = time.Now()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	noteID := fmt.Sprintf("apple_%d", time.Now().UnixNano())

	if err := os.MkdirAll(m.dir, 0755); err != nil {
		return err
	}
	if err := m.writeNote(noteID, content); err != nil {
		return err
	}

	meta, _ := m.readMeta()
	meta.Notes = append(meta.Notes, noteMeta{
		ID:        noteID,
		Title:     titleFrom(content, title),
		UpdatedAt: date.UnixMilli(),
	})
	_ = m.writeMeta(meta)

	uidst := m.loadUID(meta)
	uidst.Entries = append(uidst.Entries, uidEntry{
		NoteID:  noteID,
		UID:     uidst.Next,
		Flags:   append([]string{imap.SeenFlag}, flags...),
		Deleted: false,
	})
	uidst.Next++
	_ = m.saveUID(uidst)
	return nil
}

// UpdateMessagesFlags handles \Deleted and \Seen flag changes.
func (m *notesMailbox) UpdateMessagesFlags(uid bool, seqset *imap.SeqSet, op imap.FlagsOp, flags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	meta, _ := m.readMeta()
	uidst := m.loadUID(meta)
	active := activeEntries(uidst)

	changed := false
	for i := range active {
		seqNum := uint32(i + 1)
		id := seqNum
		if uid {
			id = active[i].UID
		}
		if !seqset.Contains(id) {
			continue
		}
		idx := uidIndexOf(uidst, active[i].UID)
		if idx < 0 {
			continue
		}
		switch op {
		case imap.SetFlags:
			uidst.Entries[idx].Flags = flags
		case imap.AddFlags:
			uidst.Entries[idx].Flags = addFlags(uidst.Entries[idx].Flags, flags)
		case imap.RemoveFlags:
			uidst.Entries[idx].Flags = removeFlags(uidst.Entries[idx].Flags, flags)
		}
		if hasFlag(flags, imap.DeletedFlag) && (op == imap.SetFlags || op == imap.AddFlags) {
			uidst.Entries[idx].Deleted = true
		}
		if !hasFlag(uidst.Entries[idx].Flags, imap.DeletedFlag) {
			uidst.Entries[idx].Deleted = false
		}
		changed = true
	}
	if changed {
		return m.saveUID(uidst)
	}
	return nil
}

// Expunge deletes notes that have \Deleted set.
func (m *notesMailbox) Expunge() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	meta, _ := m.readMeta()
	uidst := m.loadUID(meta)

	var surviving []uidEntry
	for _, e := range uidst.Entries {
		if e.Deleted {
			_ = m.deleteNote(e.NoteID)
			meta.Notes = removeNoteMeta(meta.Notes, e.NoteID)
		} else {
			surviving = append(surviving, e)
		}
	}
	uidst.Entries = surviving
	_ = m.writeMeta(meta)
	return m.saveUID(uidst)
}

func (m *notesMailbox) CopyMessages(_ bool, _ *imap.SeqSet, _ string) error { return nil }

// ── file I/O ─────────────────────────────────────────────────────────────────

func (m *notesMailbox) readMeta() (*notesMeta, error) {
	data, err := os.ReadFile(filepath.Join(m.dir, "meta.json"))
	if os.IsNotExist(err) {
		return &notesMeta{Theme: "dark", Notes: []noteMeta{}}, nil
	}
	if err != nil {
		return &notesMeta{Theme: "dark", Notes: []noteMeta{}}, err
	}
	var nm notesMeta
	if e := json.Unmarshal(data, &nm); e != nil {
		return &notesMeta{Theme: "dark", Notes: []noteMeta{}}, e
	}
	if nm.Notes == nil {
		nm.Notes = []noteMeta{}
	}
	return &nm, nil
}

func (m *notesMailbox) writeMeta(nm *notesMeta) error {
	data, err := json.Marshal(nm)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.dir, "meta.json"), data, 0644)
}

func (m *notesMailbox) readNote(id string) (string, error) {
	data, err := os.ReadFile(filepath.Join(m.dir, id+".txt"))
	return string(data), err
}

func (m *notesMailbox) writeNote(id, content string) error {
	return os.WriteFile(filepath.Join(m.dir, id+".txt"), []byte(content), 0644)
}

func (m *notesMailbox) deleteNote(id string) error {
	return os.Remove(filepath.Join(m.dir, id+".txt"))
}

// ── UID store ─────────────────────────────────────────────────────────────────

func (m *notesMailbox) uidPath() string {
	return filepath.Join(m.dir, ".imap_uid.json")
}

func (m *notesMailbox) loadUID(meta *notesMeta) *uidStore {
	data, err := os.ReadFile(m.uidPath())
	var st uidStore
	if err == nil {
		json.Unmarshal(data, &st) //nolint
	}
	if st.Validity == 0 {
		st.Validity = uint32(time.Now().Unix())
	}
	if st.Next == 0 {
		st.Next = 1
	}

	// Ensure every note in meta.json has a UID entry.
	known := make(map[string]bool, len(st.Entries))
	for _, e := range st.Entries {
		known[e.NoteID] = true
	}
	for _, nm := range meta.Notes {
		if !known[nm.ID] {
			st.Entries = append(st.Entries, uidEntry{
				NoteID:  nm.ID,
				UID:     st.Next,
				Flags:   []string{imap.SeenFlag},
				Deleted: false,
			})
			st.Next++
		}
	}
	return &st
}

func (m *notesMailbox) saveUID(st *uidStore) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(m.uidPath(), data, 0600)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func activeEntries(st *uidStore) []uidEntry {
	var out []uidEntry
	for _, e := range st.Entries {
		if !e.Deleted {
			out = append(out, e)
		}
	}
	return out
}

func uidIndexOf(st *uidStore, uid uint32) int {
	for i, e := range st.Entries {
		if e.UID == uid {
			return i
		}
	}
	return -1
}

func findNoteMeta(notes []noteMeta, id string) *noteMeta {
	for i := range notes {
		if notes[i].ID == id {
			return &notes[i]
		}
	}
	return nil
}

func removeNoteMeta(notes []noteMeta, id string) []noteMeta {
	for i, n := range notes {
		if n.ID == id {
			return append(notes[:i], notes[i+1:]...)
		}
	}
	return notes
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

func addFlags(current, add []string) []string {
	for _, f := range add {
		if !hasFlag(current, f) {
			current = append(current, f)
		}
	}
	return current
}

func removeFlags(current, remove []string) []string {
	var out []string
	for _, f := range current {
		if !hasFlag(remove, f) {
			out = append(out, f)
		}
	}
	return out
}

func titleFrom(content, fallback string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > 60 {
			runes := []rune(line)
			return string(runes[:60])
		}
		return line
	}
	if fallback != "" {
		return fallback
	}
	return "New Note"
}

// ── RFC 2822 message builders ─────────────────────────────────────────────────

// buildRawMessage creates an RFC 2822 message from a note, in Apple Notes format.
func buildRawMessage(username, title, content string, date time.Time) []byte {
	var sb strings.Builder
	addr := username + "@arozos.local"
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Date: " + date.Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("Subject: " + encodeHeader(title) + "\r\n")
	sb.WriteString("From: " + addr + "\r\n")
	sb.WriteString("To: " + addr + "\r\n")
	sb.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	sb.WriteString("X-Uniform-Type-Identifier: com.apple.mail-note\r\n")
	sb.WriteString("X-Mail-Created-Date: " + date.Format(time.RFC1123Z) + "\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(plainToHTML(content))
	return []byte(sb.String())
}

// extractSection returns the appropriate part of the raw message for a body section.
func extractSection(raw []byte, section *imap.BodySectionName) []byte {
	if len(section.Path) > 0 {
		// Multipart path not supported; return whole body.
		return raw
	}
	switch section.Specifier {
	case imap.HeaderSpecifier:
		if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
			return raw[:i+4]
		}
		return raw
	case imap.TextSpecifier:
		if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
			return raw[i+4:]
		}
		return raw
	default:
		return raw // imap.EntireSpecifier or empty
	}
}

// parseRawMessage extracts title, plain-text content and date from an RFC 2822 message.
func parseRawMessage(raw []byte) (title, content string, date time.Time) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "New Note", string(raw), time.Now()
	}
	title = m.Header.Get("Subject")
	if d, err := m.Header.Date(); err == nil {
		date = d
	}
	body, _ := io.ReadAll(m.Body)
	ct := m.Header.Get("Content-Type")
	if strings.Contains(strings.ToLower(ct), "text/html") {
		content = htmlToPlain(string(body))
	} else {
		content = string(body)
	}
	content = strings.TrimSpace(content)
	return
}

// plainToHTML wraps plain-text content in Apple-Notes-compatible HTML.
func plainToHTML(text string) string {
	lines := strings.Split(text, "\n")
	var sb strings.Builder
	sb.WriteString(`<html><head><meta charset="utf-8"></head>`)
	sb.WriteString(`<body style="word-wrap: break-word; -webkit-nbsp-mode: space; line-break: after-white-space;">`)
	for _, line := range lines {
		esc := htmlEscapeString(line)
		if esc == "" {
			sb.WriteString("<div><br></div>")
		} else {
			sb.WriteString("<div>" + esc + "</div>")
		}
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

var reBlockEnd = regexp.MustCompile(`(?i)</(div|p|li|tr|blockquote)>`)
var reBR = regexp.MustCompile(`(?i)<br\s*/?>`)
var reTag = regexp.MustCompile(`<[^>]+>`)

// htmlToPlain strips HTML tags and returns readable plain text.
func htmlToPlain(html string) string {
	s := reBlockEnd.ReplaceAllString(html, "\n")
	s = reBR.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	reMultiNL := regexp.MustCompile(`\n{3,}`)
	s = reMultiNL.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func htmlEscapeString(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

func encodeHeader(s string) string {
	// Simple ASCII check — non-ASCII headers should be encoded, but for
	// note titles that are usually short, this is sufficient.
	for _, r := range s {
		if r > 127 {
			return fmt.Sprintf("=?utf-8?q?%s?=", qEncodeString(s))
		}
	}
	return s
}

func qEncodeString(s string) string {
	var sb strings.Builder
	for _, b := range []byte(s) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') {
			sb.WriteByte(b)
		} else if b == ' ' {
			sb.WriteByte('_')
		} else {
			fmt.Fprintf(&sb, "=%02X", b)
		}
	}
	return sb.String()
}
