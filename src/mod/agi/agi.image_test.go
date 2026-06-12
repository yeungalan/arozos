package agi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── pure-Go EXIF helper tests ────────────────────────────────────────────────

// TestParseExifString checks that the multi-line exif.Exif.String() output is
// split into a field map without mangling the JSON fragment values goexif emits.
func TestParseExifString(t *testing.T) {
	input := strings.Join([]string{
		`Make: "NIKON CORPORATION"`,
		`Model: "NIKON D2H"`,
		`Orientation: 1`,
		`FNumber: "45/10"`,
		`GPSLatitude: ["39/1","54/1","56/1"]`,
		``,                              // trailing blank line, must be skipped
		`MalformedLineWithoutSeparator`, // no ": ", must be skipped
	}, "\n")

	got := parseExifString(input)

	want := map[string]interface{}{
		"Make":        `"NIKON CORPORATION"`,
		"Model":       `"NIKON D2H"`,
		"Orientation": `1`,
		"FNumber":     `"45/10"`,
		"GPSLatitude": `["39/1","54/1","56/1"]`,
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d fields, got %d (%v)", len(want), len(got), got)
	}
	for field, expected := range want {
		if got[field] != expected {
			t.Errorf("field %q: expected %q, got %q", field, expected, got[field])
		}
	}
}

// TestParseExifString_Empty ensures empty / separator-less input yields an empty
// map rather than panicking or inventing fields.
func TestParseExifString_Empty(t *testing.T) {
	for _, input := range []string{"", "\n\n", "no-separators-here"} {
		if got := parseExifString(input); len(got) != 0 {
			t.Errorf("parseExifString(%q): expected empty map, got %v", input, got)
		}
	}
}

// TestParseExifString_ValueContainingColon guards the SplitN(": ", 2) behaviour:
// only the first field separator may split, so colon-bearing values (timestamps,
// captions) stay intact.
func TestParseExifString_ValueContainingColon(t *testing.T) {
	got := parseExifString(`DateTime: "2003:11:23 18:07:37"` + "\n" + `Caption: "see: notes"`)
	if got["DateTime"] != `"2003:11:23 18:07:37"` {
		t.Errorf("DateTime mis-parsed: got %q", got["DateTime"])
	}
	if got["Caption"] != `"see: notes"` {
		t.Errorf("Caption mis-parsed: got %q", got["Caption"])
	}
}

// ─── integration tests against a real EXIF image ──────────────────────────────

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "exif_sample.jpg"))
	if err != nil {
		t.Fatalf("cannot read EXIF fixture: %v", err)
	}
	return data
}

// TestDecodeExifAsJSON_RealImage decodes a genuine EXIF JPEG and confirms the
// serialised output is valid JSON whose values keep the goexif fragment shape
// (bare integer, double-quoted string, double-quoted rational).
func TestDecodeExifAsJSON_RealImage(t *testing.T) {
	jsonString, err := decodeExifAsJSON(bytes.NewReader(readFixture(t)))
	if err != nil {
		t.Fatalf("decodeExifAsJSON failed on EXIF image: %v", err)
	}

	var parsed map[string]string
	if err := json.Unmarshal([]byte(jsonString), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v (%s)", err, jsonString)
	}

	want := map[string]string{
		"ColorSpace":  `65535`,                 // integer value
		"DateTime":    `"2012:11:04 05:42:32"`, // quoted string value
		"XResolution": `"72/1"`,                // rational value
	}
	for field, expected := range want {
		if parsed[field] != expected {
			t.Errorf("field %q: expected %q, got %q", field, expected, parsed[field])
		}
	}
}

// TestReaderHasExif verifies EXIF presence detection both ways.
func TestReaderHasExif(t *testing.T) {
	if !readerHasExif(bytes.NewReader(readFixture(t))) {
		t.Error("expected EXIF image to report hasExif = true")
	}
	if readerHasExif(strings.NewReader("not an image")) {
		t.Error("expected non-image data to report hasExif = false")
	}
}

// TestDecodeExifAsJSON_NoExif ensures EXIF-less input surfaces an error instead
// of a bogus empty result, so imagelib.getExif() can report failure.
func TestDecodeExifAsJSON_NoExif(t *testing.T) {
	if _, err := decodeExifAsJSON(strings.NewReader("not an image")); err == nil {
		t.Error("expected an error when decoding non-EXIF data")
	}
}
