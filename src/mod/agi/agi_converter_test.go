package agi

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/robertkrimen/otto"
	"imuslab.com/arozos/mod/agi/static"
	"imuslab.com/arozos/mod/filesystem"
	"imuslab.com/arozos/mod/filesystem/abstractions/localfs"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// newConverterTestFSH creates a FileSystemHandler backed by a temporary local
// directory.
func newConverterTestFSH(t *testing.T) (*filesystem.FileSystemHandler, string) {
	t.Helper()
	dir := t.TempDir()
	abs := localfs.NewLocalFileSystemAbstraction("TEST", dir+"/", "public", false)
	fsh := &filesystem.FileSystemHandler{
		Name:                  "test",
		UUID:                  "TEST",
		Path:                  dir + "/",
		ReadOnly:              false,
		Hierarchy:             "public",
		InitiationTime:        time.Now().Unix(),
		FileSystemAbstraction: abs,
		Filesystem:            "ext4",
	}
	return fsh, dir
}

// makeJPEGBytes encodes a small valid JPEG and returns its bytes.
func makeJPEGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

// assertValidJPEG fails the test if data is not a decodable JPEG.
func assertValidJPEG(t *testing.T, data []byte) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output is not a decodable image: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("expected jpeg output, got %q", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		t.Fatalf("decoded image has invalid dimensions %dx%d", cfg.Width, cfg.Height)
	}
}

// ─── RegisterConverterLib ─────────────────────────────────────────────────────

func TestRegisterConverterLib_AddsToLoadedLibs(t *testing.T) {
	g := minimalGateway()
	g.ConverterLibRegister()

	if _, ok := g.LoadedAGILibrary["converter"]; !ok {
		t.Error("expected 'converter' in LoadedAGILibrary after ConverterLibRegister")
	}
}

func TestRegisterConverterLib_IdempotentDoesNotPanic(t *testing.T) {
	g := minimalGateway()
	// Second call should log a warning but not panic.
	g.ConverterLibRegister()
	g.ConverterLibRegister()
}

// ─── JS object wiring ─────────────────────────────────────────────────────────

func TestInjectConverterLib_JSObjectExposed(t *testing.T) {
	g := minimalGateway()
	vm := otto.New()
	payload := &static.AgiLibInjectionPayload{VM: vm, User: stubUser("alice")}
	g.injectConverterFunctions(payload)

	methods := []string{"rawToJpg", "isRawFile", "supportedRawFormats"}
	for _, m := range methods {
		val, err := vm.Run(`typeof converter.` + m)
		if err != nil {
			t.Fatalf("evaluating converter.%s: %v", m, err)
		}
		s, _ := val.ToString()
		if s != "function" {
			t.Errorf("converter.%s should be a function, got %q", m, s)
		}
	}
}

func TestConverter_IsRawFileJS(t *testing.T) {
	g := minimalGateway()
	vm := otto.New()
	g.injectConverterFunctions(&static.AgiLibInjectionPayload{VM: vm, User: stubUser("alice")})

	cases := map[string]bool{
		`converter.isRawFile("user:/a.arw")`: true,
		`converter.isRawFile("user:/a.CR2")`: true, // case-insensitive
		`converter.isRawFile("user:/a.dng")`: true,
		`converter.isRawFile("user:/a.jpg")`: false,
		`converter.isRawFile("user:/a.pdf")`: false,
	}
	for expr, want := range cases {
		val, err := vm.Run(expr)
		if err != nil {
			t.Fatalf("%s errored: %v", expr, err)
		}
		got, _ := val.ToBoolean()
		if got != want {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
}

func TestConverter_SupportedRawFormatsJS(t *testing.T) {
	g := minimalGateway()
	vm := otto.New()
	g.injectConverterFunctions(&static.AgiLibInjectionPayload{VM: vm, User: stubUser("alice")})

	val, err := vm.Run(`converter.supportedRawFormats().join(",")`)
	if err != nil {
		t.Fatalf("supportedRawFormats() errored: %v", err)
	}
	s, _ := val.ToString()
	if !strings.Contains(s, ".arw") || !strings.Contains(s, ".dng") {
		t.Errorf("expected supportedRawFormats to contain .arw and .dng, got %q", s)
	}
}

// ─── pure helper: isJpegOutput ─────────────────────────────────────────────────

func TestIsJpegOutput(t *testing.T) {
	cases := map[string]bool{
		"a.jpg": true, "a.JPG": true, "a.jpeg": true, "a.JPEG": true,
		"a.png": false, "a.pdf": false, "noext": false,
	}
	for in, want := range cases {
		if got := isJpegOutput(in); got != want {
			t.Errorf("isJpegOutput(%q) = %v, want %v", in, got, want)
		}
	}
}

// ─── rawFileToJpeg ──────────────────────────────────────────────────────────────

func TestRawFileToJpeg_Success(t *testing.T) {
	fsh, dir := newConverterTestFSH(t)

	// Synthesize a RAW file: a non-FF header followed by a real embedded JPEG.
	src := filepath.Join(dir, "photo.arw")
	raw := append([]byte("RAWHEADER\x00\x01\x02"), makeJPEGBytes(t)...)
	if err := os.WriteFile(src, raw, 0644); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	dest := filepath.Join(dir, "photo.jpg")

	if err := rawFileToJpeg(fsh, src, fsh, dest); err != nil {
		t.Fatalf("rawFileToJpeg failed: %v", err)
	}

	out, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("output not written: %v", err)
	}
	assertValidJPEG(t, out)
}

func TestRawFileToJpeg_SourceNotExist(t *testing.T) {
	fsh, dir := newConverterTestFSH(t)
	err := rawFileToJpeg(fsh, filepath.Join(dir, "ghost.arw"), fsh, filepath.Join(dir, "out.jpg"))
	if err == nil {
		t.Error("expected error for missing source file")
	}
}

func TestRawFileToJpeg_NotRawExtension(t *testing.T) {
	fsh, dir := newConverterTestFSH(t)
	src := filepath.Join(dir, "note.txt")
	os.WriteFile(src, []byte("hello"), 0644)
	err := rawFileToJpeg(fsh, src, fsh, filepath.Join(dir, "out.jpg"))
	if err == nil {
		t.Error("expected error for non-RAW source extension")
	}
}

func TestRawFileToJpeg_BadDestExtension(t *testing.T) {
	fsh, dir := newConverterTestFSH(t)
	src := filepath.Join(dir, "photo.arw")
	os.WriteFile(src, append([]byte("HEAD"), makeJPEGBytes(t)...), 0644)
	err := rawFileToJpeg(fsh, src, fsh, filepath.Join(dir, "out.png"))
	if err == nil {
		t.Error("expected error for non-jpeg destination extension")
	}
}

func TestRawFileToJpeg_NoEmbeddedJpeg(t *testing.T) {
	fsh, dir := newConverterTestFSH(t)
	src := filepath.Join(dir, "broken.arw")
	// No JPEG markers at all → RenderRAWImage should fail.
	os.WriteFile(src, []byte("this raw file has no embedded jpeg"), 0644)
	err := rawFileToJpeg(fsh, src, fsh, filepath.Join(dir, "out.jpg"))
	if err == nil {
		t.Error("expected error when RAW file has no embedded JPEG")
	}
}
