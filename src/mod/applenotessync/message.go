package applenotessync

/*
	Conversion between arozos notes (plain text .txt files) and the
	RFC 2822 messages Apple Notes exchanges over IMAP.
*/

import (
	"crypto/md5"
	"encoding/hex"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// buildNoteMessage renders an arozos note as a raw Apple-Notes-compatible
// IMAP message (CRLF line endings, quoted-printable HTML body).
func buildNoteMessage(uuid, title, content, username string, updatedAt time.Time) []byte {
	var qp strings.Builder
	w := quotedprintable.NewWriter(&qp)
	w.Write([]byte(wrapInAppleHTML(content)))
	w.Close()

	encodedSubject := mime.QEncoding.Encode("utf-8", title)
	date := updatedAt.Format(time.RFC1123Z)

	var sb strings.Builder
	sb.WriteString("Date: " + date + "\r\n")
	sb.WriteString("From: " + username + "@arozos.local\r\n")
	sb.WriteString("Message-Id: <" + uuid + "@arozos>\r\n")
	sb.WriteString("Subject: " + encodedSubject + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("X-Uniform-Type-Identifier: com.apple.mail-note\r\n")
	sb.WriteString("X-Universally-Unique-Identifier: " + uuid + "\r\n")
	sb.WriteString("X-Mail-Created-Date: " + date + "\r\n")
	sb.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	sb.WriteString("\r\n")
	// Normalize line endings to CRLF without doubling the CRs the
	// quoted-printable writer already emits
	qpBody := strings.ReplaceAll(qp.String(), "\r\n", "\n")
	sb.WriteString(strings.ReplaceAll(qpBody, "\n", "\r\n"))
	return []byte(sb.String())
}

// parsedNote is the result of decoding a message APPENDed by an Apple device.
type parsedNote struct {
	UUID    string
	Title   string
	Content string // plain text
}

// parseNoteMessage decodes a raw message into note fields.
func parseNoteMessage(raw []byte) (*parsedNote, error) {
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}

	uuid := strings.TrimSpace(msg.Header.Get("X-Universally-Unique-Identifier"))

	dec := new(mime.WordDecoder)
	subject := msg.Header.Get("Subject")
	if decoded, err := dec.DecodeHeader(subject); err == nil {
		subject = decoded
	}

	content, err := extractTextFromMessage(msg)
	if err != nil {
		return nil, err
	}

	title := derivedTitle(subject, content)
	return &parsedNote{UUID: uuid, Title: title, Content: content}, nil
}

// extractTextFromMessage returns the plain-text content of a parsed message,
// decoding quoted-printable / base64 and stripping HTML where needed.
func extractTextFromMessage(msg *mail.Message) (string, error) {
	ct := msg.Header.Get("Content-Type")
	cte := strings.ToLower(msg.Header.Get("Content-Transfer-Encoding"))
	mediaType, params, _ := mime.ParseMediaType(ct)

	decode := func(body io.Reader, enc string) io.Reader {
		if enc == "quoted-printable" {
			return quotedprintable.NewReader(body)
		}
		return body
	}

	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		mr := multipart.NewReader(msg.Body, params["boundary"])
		htmlPart, textPart := "", ""
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}
			partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			partCTE := strings.ToLower(part.Header.Get("Content-Transfer-Encoding"))
			data, _ := io.ReadAll(decode(part, partCTE))
			switch {
			case strings.EqualFold(partType, "text/html"):
				htmlPart = stripHTML(string(data))
			case strings.EqualFold(partType, "text/plain"):
				textPart = string(data)
			}
		}
		if htmlPart != "" {
			return htmlPart, nil
		}
		return textPart, nil

	case strings.EqualFold(mediaType, "text/html") || mediaType == "":
		data, _ := io.ReadAll(decode(msg.Body, cte))
		if strings.EqualFold(mediaType, "text/html") {
			return stripHTML(string(data)), nil
		}
		return string(data), nil

	default:
		data, _ := io.ReadAll(decode(msg.Body, cte))
		return string(data), nil
	}
}

var (
	tagRE      = regexp.MustCompile(`<[^>]+>`)
	brDivRE    = regexp.MustCompile(`(?i)<br\s*/?\s*></div\s*>`)
	brRE       = regexp.MustCompile(`(?i)<br\s*/?>`)
	closeTagRE = regexp.MustCompile(`(?i)</(?:p|div|li|h[1-6]|tr)\s*>`)
)

// stripHTML converts an HTML note body to plain text. Apple Notes wraps each
// line in <div>…</div> and renders empty lines as <div><br></div>, so that
// pair must collapse to a single newline for the conversion to round-trip.
func stripHTML(h string) string {
	h = strings.ReplaceAll(h, "\r\n", "\n")
	h = brDivRE.ReplaceAllString(h, "\n")
	h = brRE.ReplaceAllString(h, "\n")
	h = closeTagRE.ReplaceAllString(h, "\n")
	h = tagRE.ReplaceAllString(h, "")
	replacer := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&nbsp;", " ", "&quot;", `"`, "&#x27;", "'", "&#39;", "'",
	)
	h = replacer.Replace(h)
	return strings.TrimSpace(h)
}

func wrapInAppleHTML(text string) string {
	var sb strings.Builder
	sb.WriteString(`<html><head><meta http-equiv="Content-Type" content="text/html; charset=utf-8" /></head>`)
	sb.WriteString(`<body style="word-wrap: break-word; -webkit-nbsp-mode: space; line-break: after-white-space;">`)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(line)
		if esc == "" {
			sb.WriteString("<div><br></div>")
		} else {
			sb.WriteString("<div>" + esc + "</div>")
		}
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

func derivedTitle(subject, content string) string {
	if s := strings.TrimSpace(subject); s != "" {
		return truncate(s, 60)
	}
	for _, line := range strings.Split(content, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return truncate(line, 60)
		}
	}
	return "New Note"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func contentHash(content string) string {
	sum := md5.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}
