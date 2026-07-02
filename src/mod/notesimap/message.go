package notesimap

/*
	message.go - converts between the Notes web-app's plain-text notes and the
	MIME "Notes over IMAP" message format Apple Notes expects: a single
	text/html part tagged with X-Uniform-Type-Identifier: com.apple.mail-note,
	the same convention Apple Notes itself uses when syncing against a
	non-iCloud IMAP account (Gmail, Fastmail, ...).
*/

import (
	"bytes"
	"html"
	"io"
	"mime"
	"regexp"
	"strings"
	"time"

	message "github.com/emersion/go-message"
)

const appleMailNoteType = "com.apple.mail-note"

// brBeforeClosePattern collapses "<br></div>" (an empty paragraph) into a
// single line break instead of two, so it round-trips as one blank line.
var brBeforeClosePattern = regexp.MustCompile(`(?i)<br\s*/?>\s*(</div>|</p>|</li>)`)
var blockBoundaryPattern = regexp.MustCompile(`(?i)<\s*(br|/div|/p|/li)\s*/?\s*>`)
var tagPattern = regexp.MustCompile(`<[^>]*>`)

// buildRawMessage renders a note as an RFC 5322 message carrying a text/html
// body, suitable for an IMAP FETCH response or APPEND round-trip.
func buildRawMessage(entry noteEntry, content string, created time.Time) []byte {
	var buf bytes.Buffer
	buf.WriteString("Date: " + created.Format(time.RFC1123Z) + "\r\n")
	buf.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", entry.Title) + "\r\n")
	buf.WriteString("Message-Id: <" + entry.ID + "@notes.arozos>\r\n")
	buf.WriteString("X-Universally-Unique-Identifier: " + entry.ID + "\r\n")
	buf.WriteString("X-Uniform-Type-Identifier: " + appleMailNoteType + "\r\n")
	buf.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(textToHTML(content))
	return buf.Bytes()
}

// parsedNote is the result of decoding an incoming APPEND payload.
type parsedNote struct {
	UUID    string // X-Universally-Unique-Identifier, empty if absent
	Title   string
	Content string
}

// parseIncomingMessage decodes a raw RFC 5322 message (as sent by Apple Notes
// via APPEND) back into a title/content pair, preferring the first text/html
// part and falling back to text/plain.
func parseIncomingMessage(raw []byte) (*parsedNote, error) {
	ent, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil, err
	}

	result := &parsedNote{
		UUID: strings.TrimSpace(ent.Header.Get("X-Universally-Unique-Identifier")),
	}
	if subject := ent.Header.Get("Subject"); subject != "" {
		if decoded, derr := (&mime.WordDecoder{}).DecodeHeader(subject); derr == nil {
			result.Title = decoded
		} else {
			result.Title = subject
		}
	}

	var htmlBody, plainBody string
	ent.Walk(func(_ []int, part *message.Entity, walkErr error) error {
		if part == nil || htmlBody != "" {
			return nil
		}
		mt, _, _ := part.Header.ContentType()
		if strings.HasPrefix(mt, "multipart/") {
			return nil
		}
		b, rerr := io.ReadAll(part.Body)
		if rerr != nil {
			return nil
		}
		switch mt {
		case "text/html", "":
			htmlBody = string(b)
		case "text/plain":
			if plainBody == "" {
				plainBody = string(b)
			}
		}
		return nil
	})

	if htmlBody != "" {
		result.Content = htmlToText(htmlBody)
	} else {
		result.Content = plainBody
	}
	if result.Title == "" {
		result.Title = deriveTitle(result.Content)
	}
	return result, nil
}

// textToHTML wraps each line of plain text in a <div>, mirroring the markup
// Apple Notes itself produces for a plain paragraph-per-line note.
func textToHTML(content string) string {
	var sb strings.Builder
	sb.WriteString("<html><body>")
	for _, line := range strings.Split(content, "\n") {
		esc := html.EscapeString(line)
		if esc == "" {
			sb.WriteString("<div><br></div>")
		} else {
			sb.WriteString("<div>" + esc + "</div>")
		}
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

// htmlToText is a best-effort HTML to plain-text conversion, matching the
// Notes web-app's plain-text storage model. It is not a full HTML parser -
// it handles the simple block markup Apple Notes and textToHTML produce.
func htmlToText(body string) string {
	body = brBeforeClosePattern.ReplaceAllString(body, "\n")
	body = blockBoundaryPattern.ReplaceAllString(body, "\n")
	body = tagPattern.ReplaceAllString(body, "")
	body = html.UnescapeString(body)
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}
