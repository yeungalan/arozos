package applenotessync

import (
	"bufio"
	"bytes"
	"mime"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/backend/backendutil"
	"github.com/emersion/go-message/textproto"
)

func TestBuildParseRoundTrip(t *testing.T) {
	uuid := "0F9A1B2C-3D4E-5F60-7182-93A4B5C6D7E8"
	content := "Shopping list\n\nMilk & eggs\n<tags> stay literal\n日本語もOK"
	raw := buildNoteMessage(uuid, "Shopping list", content, "alice", time.Now())

	parsed, err := parseNoteMessage(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.UUID != uuid {
		t.Errorf("UUID mismatch: got %q", parsed.UUID)
	}
	if parsed.Title != "Shopping list" {
		t.Errorf("title mismatch: got %q", parsed.Title)
	}
	if parsed.Content != content {
		t.Errorf("content mismatch:\n got: %q\nwant: %q", parsed.Content, content)
	}
}

func TestGeneratedMessageFetchable(t *testing.T) {
	raw := buildNoteMessage("ABC-123", "Méeting notes", "Méeting notes\nline two", "bob", time.Now())

	// The IMAP fetch path must be able to read the header and envelope
	body := bufio.NewReader(bytes.NewReader(raw))
	hdr, err := textproto.ReadHeader(body)
	if err != nil {
		t.Fatalf("header unreadable: %v", err)
	}
	env, err := backendutil.FetchEnvelope(hdr)
	if err != nil {
		t.Fatalf("envelope fetch failed: %v", err)
	}
	// Envelope subjects carry RFC 2047 encoded words; the client decodes them
	dec := new(mime.WordDecoder)
	subject, err := dec.DecodeHeader(env.Subject)
	if err != nil {
		t.Fatalf("subject not RFC 2047 decodable: %v", err)
	}
	if subject != "Méeting notes" {
		t.Errorf("subject mismatch: got %q", subject)
	}
}

func TestParseAppleStyleMessage(t *testing.T) {
	// Shape of a message as APPENDed by an Apple device
	raw := strings.Join([]string{
		"X-Uniform-Type-Identifier: com.apple.mail-note",
		"X-Universally-Unique-Identifier: 11112222-3333-4444-5555-666677778888",
		"Subject: From my iPhone",
		"Content-Type: text/html; charset=utf-8",
		"Content-Transfer-Encoding: quoted-printable",
		"MIME-Version: 1.0",
		"Date: Mon, 01 Jun 2026 10:00:00 +0000",
		"",
		"<html><body><div>From my iPhone</div><div><br></div><div>Second line=20</div></body></html>",
	}, "\r\n")

	parsed, err := parseNoteMessage([]byte(raw))
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.UUID != "11112222-3333-4444-5555-666677778888" {
		t.Errorf("UUID mismatch: got %q", parsed.UUID)
	}
	if !strings.HasPrefix(parsed.Content, "From my iPhone") {
		t.Errorf("content should start with first line, got %q", parsed.Content)
	}
	if !strings.Contains(parsed.Content, "Second line") {
		t.Errorf("content missing second line: %q", parsed.Content)
	}
}

func TestStripHTMLEntities(t *testing.T) {
	got := stripHTML("<div>a &amp; b</div><div>&lt;tag&gt;</div>")
	want := "a & b\n<tag>"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
