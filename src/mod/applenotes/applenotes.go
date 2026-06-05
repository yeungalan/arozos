package applenotes

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultIMAPServer = "imap.mail.me.com"
	DefaultIMAPPort   = 993
	DefaultIMAPFolder = "Notes"
)

// SyncConfig holds IMAP credentials and connection settings.
type SyncConfig struct {
	Email    string `json:"email"`
	Password string `json:"password"` // app-specific password
	Server   string `json:"server"`
	Port     int    `json:"port"`
	Folder   string `json:"folder"`
	Enabled  bool   `json:"enabled"`
}

// NoteMapping tracks the relationship between an arozos note and an IMAP message.
type NoteMapping struct {
	LocalID      string    `json:"localId"`
	IMAPUID      uint32    `json:"imapUid"`
	LocalUpdated int64     `json:"localUpdatedAt"` // ms since epoch
	IMAPDate     time.Time `json:"imapDate"`
}

// SyncState is persisted between syncs to track what has been synced.
type SyncState struct {
	LastSync time.Time     `json:"lastSync"`
	Mappings []NoteMapping `json:"mappings"`
}

// SyncResult summarises the outcome of one sync run.
type SyncResult struct {
	Timestamp    time.Time `json:"timestamp"`
	Success      bool      `json:"success"`
	Message      string    `json:"message"`
	Added        int       `json:"added"`
	Updated      int       `json:"updated"`
	Deleted      int       `json:"deleted"`
	AppleAdded   int       `json:"appleAdded"`
	AppleUpdated int       `json:"appleUpdated"`
	AppleDeleted int       `json:"appleDeleted"`
}

// LocalNote mirrors the note entry stored in arozos meta.json.
type LocalNote struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"` // ms since epoch
	Content   string `json:"-"`
}

// AppleNote is a note retrieved from (or to be written to) Apple's IMAP store.
type AppleNote struct {
	UID      uint32
	Subject  string
	HTMLBody string
	Date     time.Time
	Deleted  bool
}

// PlainText extracts readable plain text from an Apple Note HTML body.
func (n *AppleNote) PlainText() string {
	return htmlToPlainText(n.HTMLBody)
}

// DefaultConfig returns a SyncConfig with Apple's iCloud IMAP defaults.
func DefaultConfig() SyncConfig {
	return SyncConfig{
		Server:  DefaultIMAPServer,
		Port:    DefaultIMAPPort,
		Folder:  DefaultIMAPFolder,
		Enabled: false,
	}
}

// MarshalSyncState serialises SyncState to JSON.
func MarshalSyncState(s *SyncState) (string, error) {
	b, err := json.Marshal(s)
	return string(b), err
}

// UnmarshalSyncState deserialises SyncState from JSON.
func UnmarshalSyncState(raw string) (*SyncState, error) {
	var s SyncState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// htmlToPlainText converts Apple Notes HTML to plain text.
// It preserves line structure by treating </div>, <br>, <p> as newlines.
func htmlToPlainText(html string) string {
	// Normalize block-level endings to newlines
	reBlock := regexp.MustCompile(`(?i)</(div|p|li)>`)
	text := reBlock.ReplaceAllString(html, "\n")

	reBR := regexp.MustCompile(`(?i)<br\s*/?>`)
	text = reBR.ReplaceAllString(text, "\n")

	// Strip remaining HTML tags
	reTag := regexp.MustCompile(`<[^>]+>`)
	text = reTag.ReplaceAllString(text, "")

	// Decode common HTML entities
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&#x27;", "'")
	text = strings.ReplaceAll(text, "&#x2F;", "/")

	// Collapse 3+ consecutive newlines to 2
	reMultiNL := regexp.MustCompile(`\n{3,}`)
	text = reMultiNL.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}

// PlainTextToHTML wraps plain-text note content in Apple-Notes-compatible HTML.
func PlainTextToHTML(text string) string {
	lines := strings.Split(text, "\n")
	var sb strings.Builder
	sb.WriteString(`<html><head><meta charset="utf-8"></head>`)
	sb.WriteString(`<body style="word-wrap: break-word; -webkit-nbsp-mode: space; line-break: after-white-space;">`)
	for _, line := range lines {
		escaped := htmlEscape(line)
		if escaped == "" {
			sb.WriteString("<div><br></div>")
		} else {
			sb.WriteString("<div>")
			sb.WriteString(escaped)
			sb.WriteString("</div>")
		}
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
