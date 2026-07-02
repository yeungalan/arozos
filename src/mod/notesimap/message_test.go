package notesimap

import (
	"strings"
	"testing"
	"time"
)

func TestTextToHTMLAndBackRoundTrip(t *testing.T) {
	cases := []string{
		"Hello world",
		"Line one\nLine two\n\nLine four",
		"Special <chars> & \"quotes\" 'here'",
	}
	for _, content := range cases {
		htmlBody := textToHTML(content)
		if !strings.Contains(htmlBody, "<html>") {
			t.Errorf("textToHTML(%q) missing <html> wrapper: %q", content, htmlBody)
		}
		got := htmlToText(htmlBody)
		if got != content {
			t.Errorf("round trip mismatch: content=%q htmlToText(textToHTML(content))=%q", content, got)
		}
	}
}

func TestBuildRawMessageAndParseIncomingRoundTrip(t *testing.T) {
	entry := noteEntry{ID: "note123", Title: "My Note", UpdatedAt: 1700000000000}
	raw := buildRawMessage(entry, "Hello\nWorld", time.UnixMilli(entry.UpdatedAt))

	if !strings.Contains(string(raw), appleMailNoteType) {
		t.Fatalf("raw message missing X-Uniform-Type-Identifier marker:\n%s", raw)
	}

	parsed, err := parseIncomingMessage(raw)
	if err != nil {
		t.Fatalf("parseIncomingMessage: %v", err)
	}
	if parsed.UUID != "note123" {
		t.Errorf("UUID = %q, want note123", parsed.UUID)
	}
	if parsed.Title != "My Note" {
		t.Errorf("Title = %q, want %q", parsed.Title, "My Note")
	}
	if parsed.Content != "Hello\nWorld" {
		t.Errorf("Content = %q, want %q", parsed.Content, "Hello\nWorld")
	}
}

func TestParseIncomingMessagePlainText(t *testing.T) {
	raw := []byte("Subject: Plain note\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"just plain text")

	parsed, err := parseIncomingMessage(raw)
	if err != nil {
		t.Fatalf("parseIncomingMessage: %v", err)
	}
	if parsed.Content != "just plain text" {
		t.Errorf("Content = %q, want %q", parsed.Content, "just plain text")
	}
	if parsed.Title != "Plain note" {
		t.Errorf("Title = %q, want %q", parsed.Title, "Plain note")
	}
	if parsed.UUID != "" {
		t.Errorf("UUID = %q, want empty", parsed.UUID)
	}
}

func TestParseIncomingMessageMissingSubjectDerivesTitle(t *testing.T) {
	raw := []byte("Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<div>Derived Title</div><div>second line</div>")

	parsed, err := parseIncomingMessage(raw)
	if err != nil {
		t.Fatalf("parseIncomingMessage: %v", err)
	}
	if parsed.Title != "Derived Title" {
		t.Errorf("Title = %q, want %q", parsed.Title, "Derived Title")
	}
}
