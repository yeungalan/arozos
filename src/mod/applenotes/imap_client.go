package applenotes

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
)

// IMAPClient wraps a go-imap connection to Apple's Notes IMAP folder.
type IMAPClient struct {
	c      *client.Client
	folder string
	email  string
	mbox   *imap.MailboxStatus
}

// Connect establishes a TLS connection and logs in.
func Connect(cfg SyncConfig) (*IMAPClient, error) {
	addr := fmt.Sprintf("%s:%d", cfg.Server, cfg.Port)
	tlsCfg := &tls.Config{ServerName: cfg.Server}
	c, err := client.DialTLS(addr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("imap dial: %w", err)
	}

	if err := c.Login(cfg.Email, cfg.Password); err != nil {
		_ = c.Logout()
		return nil, fmt.Errorf("imap login: %w", err)
	}

	folder := cfg.Folder
	if folder == "" {
		folder = DefaultIMAPFolder
	}

	mbox, err := c.Select(folder, false)
	if err != nil {
		_ = c.Logout()
		return nil, fmt.Errorf("imap select %q: %w", folder, err)
	}

	return &IMAPClient{c: c, folder: folder, email: cfg.Email, mbox: mbox}, nil
}

// Close logs out of IMAP.
func (ic *IMAPClient) Close() {
	_ = ic.c.Logout()
}

// ListNotes returns all notes in the Notes mailbox.
func (ic *IMAPClient) ListNotes() ([]*AppleNote, error) {
	if ic.mbox.Messages == 0 {
		return nil, nil
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddRange(1, ic.mbox.Messages)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{
		imap.FetchUid,
		imap.FetchEnvelope,
		imap.FetchInternalDate,
		section.FetchItem(),
	}

	ch := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() { done <- ic.c.Fetch(seqSet, items, ch) }()

	var notes []*AppleNote
	for msg := range ch {
		note, err := messageToAppleNote(msg, section)
		if err != nil {
			continue
		}
		notes = append(notes, note)
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("imap fetch: %w", err)
	}
	return notes, nil
}

// GetNoteByUID fetches a single note by its IMAP UID.
func (ic *IMAPClient) GetNoteByUID(uid uint32) (*AppleNote, error) {
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{
		imap.FetchUid,
		imap.FetchEnvelope,
		imap.FetchInternalDate,
		section.FetchItem(),
	}

	ch := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- ic.c.UidFetch(seqSet, items, ch) }()

	var result *AppleNote
	for msg := range ch {
		note, err := messageToAppleNote(msg, section)
		if err == nil {
			result = note
		}
	}
	if err := <-done; err != nil {
		return nil, fmt.Errorf("imap uid fetch: %w", err)
	}
	return result, nil
}

// CreateNote appends a new note to the Apple Notes folder and returns its UID.
func (ic *IMAPClient) CreateNote(note *AppleNote) (uint32, error) {
	raw := buildRawMessage(ic.email, note)
	date := note.Date
	if date.IsZero() {
		date = time.Now()
	}
	flags := []string{imap.SeenFlag}
	if err := ic.c.Append(ic.folder, flags, date, bytes.NewReader([]byte(raw))); err != nil {
		return 0, fmt.Errorf("imap append: %w", err)
	}

	// Re-select to refresh status and find the new UID.
	mbox, err := ic.c.Select(ic.folder, false)
	if err != nil {
		return 0, nil
	}
	ic.mbox = mbox

	// The newly appended message is the last one.
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(mbox.Messages)
	items := []imap.FetchItem{imap.FetchUid}
	ch := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() { done <- ic.c.Fetch(seqSet, items, ch) }()
	var newUID uint32
	for msg := range ch {
		newUID = msg.Uid
	}
	<-done
	return newUID, nil
}

// UpdateNote replaces an existing note (delete + append) and returns the new UID.
func (ic *IMAPClient) UpdateNote(uid uint32, note *AppleNote) (uint32, error) {
	if err := ic.DeleteNote(uid); err != nil {
		return 0, err
	}
	return ic.CreateNote(note)
}

// DeleteNote marks an IMAP message as deleted and expunges it.
func (ic *IMAPClient) DeleteNote(uid uint32) error {
	seqSet := new(imap.SeqSet)
	seqSet.AddNum(uid)

	item := imap.FormatFlagsOp(imap.SetFlags, true)
	flags := []interface{}{imap.DeletedFlag}
	if err := ic.c.UidStore(seqSet, item, flags, nil); err != nil {
		return fmt.Errorf("imap store deleted: %w", err)
	}
	return ic.c.Expunge(nil)
}

// messageToAppleNote converts a raw IMAP message into an AppleNote.
func messageToAppleNote(msg *imap.Message, section *imap.BodySectionName) (*AppleNote, error) {
	if msg == nil {
		return nil, fmt.Errorf("nil message")
	}

	note := &AppleNote{
		UID:  msg.Uid,
		Date: msg.InternalDate,
	}

	if msg.Envelope != nil {
		note.Subject = msg.Envelope.Subject
	}

	r := msg.GetBody(section)
	if r == nil {
		return note, nil
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return note, nil
	}

	// Parse as RFC 2822 message to extract the HTML body.
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return note, nil
	}
	body, _ := io.ReadAll(m.Body)
	ct := m.Header.Get("Content-Type")
	if strings.Contains(strings.ToLower(ct), "text/html") ||
		strings.Contains(strings.ToLower(ct), "text/plain") {
		note.HTMLBody = string(body)
	} else {
		note.HTMLBody = string(body)
	}

	return note, nil
}

// buildRawMessage creates a raw RFC 2822 email suitable for Apple Notes.
func buildRawMessage(fromTo string, note *AppleNote) string {
	date := note.Date
	if date.IsZero() {
		date = time.Now()
	}

	htmlBody := note.HTMLBody
	if htmlBody == "" {
		htmlBody = PlainTextToHTML(note.Subject)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("From: %s\r\n", fromTo))
	sb.WriteString(fmt.Sprintf("To: %s\r\n", fromTo))
	sb.WriteString(fmt.Sprintf("Subject: %s\r\n", note.Subject))
	sb.WriteString(fmt.Sprintf("Date: %s\r\n", date.Format(time.RFC1123Z)))
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	sb.WriteString("X-Uniform-Type-Identifier: com.apple.mail-note\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(htmlBody)
	return sb.String()
}
