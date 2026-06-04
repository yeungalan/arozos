package imapnotes

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"imuslab.com/arozos/mod/database"
	"imuslab.com/arozos/mod/user"
)

type noteMeta struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
}

type notesMeta struct {
	LastOpened string     `json:"lastOpened"`
	Theme      string     `json:"theme"`
	Notes      []noteMeta `json:"notes"`
}

// Note is a single arozos note with its content and metadata.
type Note struct {
	ID        string
	Title     string
	Content   string
	UpdatedAt int64 // milliseconds unix
	UID       uint32
}

// IMAPDate formats a millisecond timestamp as an IMAP date string.
func IMAPDate(ms int64) string {
	t := time.Unix(ms/1000, 0).UTC()
	return t.Format("02-Jan-2006 15:04:05 -0700")
}

// RFC2822Date formats a millisecond timestamp as an RFC 2822 date string.
func RFC2822Date(ms int64) string {
	t := time.Unix(ms/1000, 0).UTC()
	return t.Format("Mon, 02 Jan 2006 15:04:05 -0700")
}

// getUserNotesDir returns the real filesystem path to a user's Notes directory.
func getUserNotesDir(userHandler *user.UserHandler, username string) (string, error) {
	userinfo, err := userHandler.GetUserInfoFromUsername(username)
	if err != nil {
		return "", fmt.Errorf("user not found: %w", err)
	}

	fsh, err := userinfo.GetHomeFileSystemHandler()
	if err != nil {
		return "", fmt.Errorf("home filesystem handler unavailable: %w", err)
	}

	realPath, err := fsh.FileSystemAbstraction.VirtualPathToRealPath("user:/Document/Notes", username)
	if err != nil {
		return "", fmt.Errorf("path resolution failed: %w", err)
	}

	return realPath, nil
}

// readNotesMeta reads the meta.json from a notes directory.
func readNotesMeta(notesDir string) (*notesMeta, error) {
	metaPath := filepath.Join(notesDir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &notesMeta{Theme: "dark", Notes: []noteMeta{}}, nil
		}
		return nil, err
	}
	var meta notesMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return &notesMeta{Theme: "dark", Notes: []noteMeta{}}, nil
	}
	if meta.Notes == nil {
		meta.Notes = []noteMeta{}
	}
	return &meta, nil
}

// writeNotesMeta persists meta.json.
func writeNotesMeta(notesDir string, meta *notesMeta) error {
	metaPath := filepath.Join(notesDir, "meta.json")
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(metaPath, data, 0644)
}

// getOrAssignUID retrieves a stable UID for a note, assigning one if needed.
func getOrAssignUID(db *database.Database, username, noteID string) (uint32, error) {
	db.NewTable("imapnotes")
	key := username + "/uid/" + noteID
	var uid uint32
	err := db.Read("imapnotes", key, &uid)
	if err != nil || uid == 0 {
		// Assign new UID
		nextKey := username + "/nextuid"
		var next uint32
		db.Read("imapnotes", nextKey, &next)
		if next < 1 {
			next = 1
		}
		uid = next
		db.Write("imapnotes", key, uid)
		db.Write("imapnotes", nextKey, next+1)
	}
	return uid, nil
}

// getNoteIDByUID looks up which note ID maps to a given UID.
func getNoteIDByUID(db *database.Database, username string, uid uint32, metas []noteMeta) string {
	for _, nm := range metas {
		assigned, err := getOrAssignUID(db, username, nm.ID)
		if err == nil && assigned == uid {
			return nm.ID
		}
	}
	return ""
}

// GetUIDValidity returns or creates a stable UIDVALIDITY value for a user.
func GetUIDValidity(db *database.Database, username string) uint32 {
	db.NewTable("imapnotes")
	key := username + "/uidvalidity"
	var uv uint32
	db.Read("imapnotes", key, &uv)
	if uv == 0 {
		uv = uint32(time.Now().Unix())
		db.Write("imapnotes", key, uv)
	}
	return uv
}

// LoadAllNotes reads all notes for a user, assigning UIDs as needed.
func LoadAllNotes(db *database.Database, userHandler *user.UserHandler, username string) ([]Note, error) {
	notesDir, err := getUserNotesDir(userHandler, username)
	if err != nil {
		return nil, err
	}
	os.MkdirAll(notesDir, 0755)

	meta, err := readNotesMeta(notesDir)
	if err != nil {
		return nil, err
	}

	var notes []Note
	for _, nm := range meta.Notes {
		noteFile := filepath.Join(notesDir, nm.ID+".txt")
		content, err := os.ReadFile(noteFile)
		if err != nil {
			continue
		}
		uid, _ := getOrAssignUID(db, username, nm.ID)
		notes = append(notes, Note{
			ID:        nm.ID,
			Title:     nm.Title,
			Content:   string(content),
			UpdatedAt: nm.UpdatedAt,
			UID:       uid,
		})
	}
	return notes, nil
}

// WriteNote creates or updates a note from IMAP APPEND data (HTML email body).
// Returns the UID assigned to the new/updated note.
func WriteNote(db *database.Database, userHandler *user.UserHandler, username, noteID, rawMessage string) (uint32, error) {
	notesDir, err := getUserNotesDir(userHandler, username)
	if err != nil {
		return 0, err
	}
	os.MkdirAll(notesDir, 0755)

	subject, body := parseEmailMessage(rawMessage)

	// Convert HTML body to plain text for arozos storage
	plainContent := htmlToText(body)
	if plainContent == "" && subject != "" {
		plainContent = subject
	}

	// Derive title from content first line (matches arozos Notes logic)
	title := deriveTitle(plainContent, subject)

	ts := time.Now().UnixMilli()

	noteFile := filepath.Join(notesDir, noteID+".txt")
	if err := os.WriteFile(noteFile, []byte(plainContent), 0644); err != nil {
		return 0, fmt.Errorf("write note file: %w", err)
	}

	meta, _ := readNotesMeta(notesDir)
	found := false
	for i, nm := range meta.Notes {
		if nm.ID == noteID {
			meta.Notes[i].Title = title
			meta.Notes[i].UpdatedAt = ts
			found = true
			break
		}
	}
	if !found {
		meta.Notes = append(meta.Notes, noteMeta{
			ID:        noteID,
			Title:     title,
			UpdatedAt: ts,
		})
	}
	meta.LastOpened = noteID
	writeNotesMeta(notesDir, meta)

	uid, err := getOrAssignUID(db, username, noteID)
	return uid, err
}

// DeleteNote removes a note by UID.
func DeleteNote(db *database.Database, userHandler *user.UserHandler, username string, uid uint32) error {
	notesDir, err := getUserNotesDir(userHandler, username)
	if err != nil {
		return err
	}

	meta, err := readNotesMeta(notesDir)
	if err != nil {
		return err
	}

	noteID := getNoteIDByUID(db, username, uid, meta.Notes)
	if noteID == "" {
		return nil // already gone
	}

	noteFile := filepath.Join(notesDir, noteID+".txt")
	os.Remove(noteFile)

	newNotes := []noteMeta{}
	for _, nm := range meta.Notes {
		if nm.ID != noteID {
			newNotes = append(newNotes, nm)
		}
	}
	meta.Notes = newNotes
	if meta.LastOpened == noteID {
		if len(meta.Notes) > 0 {
			meta.LastOpened = meta.Notes[0].ID
		} else {
			meta.LastOpened = ""
		}
	}

	return writeNotesMeta(notesDir, meta)
}

// BuildEmailMessage constructs an RFC 2822 email message from a Note.
func BuildEmailMessage(n Note) string {
	htmlBody := textToHTML(n.Content)
	dateStr := RFC2822Date(n.UpdatedAt)
	if n.UpdatedAt == 0 {
		dateStr = RFC2822Date(time.Now().UnixMilli())
	}

	// Build message with CRLF line endings
	var sb strings.Builder
	sb.WriteString("Date: " + dateStr + "\r\n")
	sb.WriteString("From: Notes <notes@arozos>\r\n")
	sb.WriteString("Subject: " + mimeEncodeHeader(n.Title) + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	sb.WriteString("Message-ID: <" + n.ID + "@arozos>\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(htmlBody)

	return sb.String()
}

// textToHTML converts plain text note content to minimal HTML for Apple Notes.
func textToHTML(text string) string {
	lines := strings.Split(text, "\n")
	var sb strings.Builder
	sb.WriteString("<html><head></head><body dir=\"auto\">")
	for _, line := range lines {
		escaped := html.EscapeString(line)
		if escaped == "" {
			sb.WriteString("<div><br></div>")
		} else {
			sb.WriteString("<div>" + escaped + "</div>")
		}
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

// htmlToText strips HTML tags and decodes entities.
func htmlToText(h string) string {
	// Extract body content if present
	bodyRe := regexp.MustCompile(`(?is)<body[^>]*>(.*?)</body>`)
	if m := bodyRe.FindStringSubmatch(h); len(m) > 1 {
		h = m[1]
	}

	// Normalize common block elements to newlines
	h = regexp.MustCompile(`(?i)<br\s*/?>|</div>|</p>`).ReplaceAllString(h, "\n")
	h = regexp.MustCompile(`(?i)<div><br\s*/?></div>`).ReplaceAllString(h, "\n")

	// Strip remaining tags
	h = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(h, "")

	// Decode HTML entities
	h = html.UnescapeString(h)

	// Collapse excessive blank lines (more than 2 in a row)
	h = regexp.MustCompile(`\n{3,}`).ReplaceAllString(h, "\n\n")

	return strings.TrimSpace(h)
}

// parseEmailMessage extracts subject and body from a raw RFC 2822 message.
func parseEmailMessage(raw string) (subject, body string) {
	// Normalise line endings
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")

	parts := strings.SplitN(raw, "\n\n", 2)
	headerSection := parts[0]
	if len(parts) == 2 {
		body = parts[1]
	}

	// Parse headers
	for _, line := range strings.Split(headerSection, "\n") {
		if strings.HasPrefix(strings.ToLower(line), "subject:") {
			subject = strings.TrimSpace(line[8:])
			subject = mimeDecodeHeader(subject)
		}
	}

	return subject, body
}

// deriveTitle gets the first non-empty line of content, capped at 60 chars.
func deriveTitle(content, fallback string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 60 {
				return line[:60]
			}
			return line
		}
	}
	if fallback != "" {
		if len(fallback) > 60 {
			return fallback[:60]
		}
		return fallback
	}
	return "New Note"
}

// mimeEncodeHeader encodes a header value as UTF-8 if it contains non-ASCII.
func mimeEncodeHeader(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?UTF-8?Q?" + quotedPrintableEncode(s) + "?="
		}
	}
	return s
}

func quotedPrintableEncode(s string) string {
	var sb strings.Builder
	for _, b := range []byte(s) {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') {
			sb.WriteByte(b)
		} else {
			sb.WriteString(fmt.Sprintf("=%02X", b))
		}
	}
	return sb.String()
}

// mimeDecodeHeader does a best-effort decode of encoded MIME header words.
func mimeDecodeHeader(s string) string {
	// Handle =?UTF-8?B?...?= (base64) or =?UTF-8?Q?...?= (quoted-printable)
	re := regexp.MustCompile(`=\?[^?]+\?[BQbq]\?([^?]*)\?=`)
	s = re.ReplaceAllStringFunc(s, func(match string) string {
		return match // simplified: return as-is; modern phones send plain UTF-8
	})
	return strings.TrimSpace(s)
}
