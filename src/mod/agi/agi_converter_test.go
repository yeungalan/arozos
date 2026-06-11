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
// directory, with a usable local buffer path so BufferRemoteToLocal works.
func newConverterTestFSH(t *testing.T) (*filesystem.FileSystemHandler, string) {
	t.Helper()
	dir := t.TempDir()
	bufdir := filepath.Join(dir, ".buffer")
	if err := os.MkdirAll(bufdir, 0755); err != nil {
		t.Fatalf("mkdir buffer: %v", err)
	}
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
	fsh.RuntimePersistenceConfig.LocalBufferPath = bufdir
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

	methods := []string{
		"rawToJpg", "pdfToJpg", "toJpg",
		"isRawFile", "isPdfFile", "pdfEngineAvailable", "supportedRawFormats",
	}
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

func TestConverter_IsPdfFileJS(t *testing.T) {
	g := minimalGateway()
	vm := otto.New()
	g.injectConverterFunctions(&static.AgiLibInjectionPayload{VM: vm, User: stubUser("alice")})

	cases := map[string]bool{
		`converter.isPdfFile("user:/doc.pdf")`: true,
		`converter.isPdfFile("user:/doc.PDF")`: true,
		`converter.isPdfFile("user:/doc.jpg")`: false,
		`converter.isPdfFile("user:/doc.arw")`: false,
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

func TestConverter_PdfEngineAvailableJS(t *testing.T) {
	g := minimalGateway()
	vm := otto.New()
	g.injectConverterFunctions(&static.AgiLibInjectionPayload{VM: vm, User: stubUser("alice")})

	val, err := vm.Run(`converter.pdfEngineAvailable()`)
	if err != nil {
		t.Fatalf("pdfEngineAvailable() errored: %v", err)
	}
	// The value depends on the host; we only require that it is a boolean
	// and matches the Go-side detector.
	if !val.IsBoolean() {
		t.Fatalf("pdfEngineAvailable() should return a boolean, got %q", val.Class())
	}
	got, _ := val.ToBoolean()
	if want := findPdfRasterizer() != ""; got != want {
		t.Errorf("pdfEngineAvailable() = %v, want %v", got, want)
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

// ─── pure helpers: isPdfFile / isJpegOutput ────────────────────────────────────

func TestIsPdfFile(t *testing.T) {
	cases := map[string]bool{
		"a.pdf": true, "a.PDF": true, "dir/b.Pdf": true,
		"a.jpg": false, "a.arw": false, "noext": false, "": false,
	}
	for in, want := range cases {
		if got := isPdfFile(in); got != want {
			t.Errorf("isPdfFile(%q) = %v, want %v", in, got, want)
		}
	}
}

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

// ─── pdfFileToJpeg ──────────────────────────────────────────────────────────────

// When no host rasterizer is available (or when the host tool fails on our
// synthetic, non-standard PDF) the pure-Go embedded-JPEG fallback should still
// produce a valid JPEG for an image-based PDF.
func TestPdfFileToJpeg_EmbeddedFallback(t *testing.T) {
	fsh, dir := newConverterTestFSH(t)
	src := filepath.Join(dir, "scan.pdf")
	pdf := append([]byte("%PDF-1.4\n% image-based pdf\n"), makeJPEGBytes(t)...)
	if err := os.WriteFile(src, pdf, 0644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	dest := filepath.Join(dir, "scan.jpg")

	if err := pdfFileToJpeg(fsh, src, fsh, dest, 1, 150); err != nil {
		t.Fatalf("pdfFileToJpeg failed: %v", err)
	}
	out, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("output not written: %v", err)
	}
	assertValidJPEG(t, out)
}

func TestPdfFileToJpeg_NotPdfExtension(t *testing.T) {
	fsh, dir := newConverterTestFSH(t)
	src := filepath.Join(dir, "note.txt")
	os.WriteFile(src, []byte("hello"), 0644)
	err := pdfFileToJpeg(fsh, src, fsh, filepath.Join(dir, "out.jpg"), 1, 150)
	if err == nil {
		t.Error("expected error for non-PDF source extension")
	}
}

func TestPdfFileToJpeg_BadDestExtension(t *testing.T) {
	fsh, dir := newConverterTestFSH(t)
	src := filepath.Join(dir, "scan.pdf")
	os.WriteFile(src, append([]byte("%PDF-1.4\n"), makeJPEGBytes(t)...), 0644)
	err := pdfFileToJpeg(fsh, src, fsh, filepath.Join(dir, "out.gif"), 1, 150)
	if err == nil {
		t.Error("expected error for non-jpeg destination extension")
	}
}

func TestPdfFileToJpeg_NoImageNoEngine(t *testing.T) {
	if findPdfRasterizer() != "" {
		t.Skip("a host PDF rasterizer is installed; the no-engine fallback path cannot be exercised")
	}
	fsh, dir := newConverterTestFSH(t)
	src := filepath.Join(dir, "text.pdf")
	// Plain text, no embedded JPEG markers.
	os.WriteFile(src, []byte("%PDF-1.4 text only document with no images"), 0644)
	err := pdfFileToJpeg(fsh, src, fsh, filepath.Join(dir, "out.jpg"), 1, 150)
	if err == nil {
		t.Error("expected error when no engine and no embedded image are available")
	}
}

// ─── findPdfRasterizer ────────────────────────────────────────────────────────

func TestFindPdfRasterizer_ReturnsKnownOrEmpty(t *testing.T) {
	got := findPdfRasterizer()
	if got == "" {
		return // none installed — valid
	}
	found := false
	for _, tool := range pdfRasterizers {
		if got == tool {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("findPdfRasterizer returned %q which is not in the known list %v", got, pdfRasterizers)
	}
}

// ─── buildPdfRasterizeArgs ──────────────────────────────────────────────────────

func TestBuildPdfRasterizeArgs_Poppler(t *testing.T) {
	for _, tool := range []string{"pdftoppm", "pdftocairo"} {
		cmd, args, out, err := buildPdfRasterizeArgs(tool, "/tmp/in.pdf", "/tmp", "stem", 3, 200)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tool, err)
		}
		if cmd != tool {
			t.Errorf("%s: cmd = %q", tool, cmd)
		}
		joined := strings.Join(args, " ")
		for _, want := range []string{"-jpeg", "-r 200", "-f 3", "-l 3", "-singlefile", "/tmp/in.pdf"} {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: args %q missing %q", tool, joined, want)
			}
		}
		if !strings.HasSuffix(out, ".jpg") {
			t.Errorf("%s: expected .jpg output path, got %q", tool, out)
		}
	}
}

func TestBuildPdfRasterizeArgs_Ghostscript(t *testing.T) {
	cmd, args, out, err := buildPdfRasterizeArgs("gs", "/tmp/in.pdf", "/tmp", "stem", 2, 120)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "gs" {
		t.Errorf("cmd = %q", cmd)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-sDEVICE=jpeg", "-r120", "-dFirstPage=2", "-dLastPage=2", "-sOutputFile=" + out} {
		if !strings.Contains(joined, want) {
			t.Errorf("gs args %q missing %q", joined, want)
		}
	}
	if !strings.HasSuffix(out, ".jpg") {
		t.Errorf("expected .jpg output path, got %q", out)
	}
}

func TestBuildPdfRasterizeArgs_Mutool(t *testing.T) {
	cmd, args, out, err := buildPdfRasterizeArgs("mutool", "/tmp/in.pdf", "/tmp", "stem", 5, 96)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "mutool" {
		t.Errorf("cmd = %q", cmd)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"draw", "-o " + out, "-r 96", "/tmp/in.pdf", " 5"} {
		if !strings.Contains(joined, want) {
			t.Errorf("mutool args %q missing %q", joined, want)
		}
	}
}

func TestBuildPdfRasterizeArgs_Defaults(t *testing.T) {
	// page < 1 and dpi < 1 should be clamped to 1 and 150 respectively.
	_, args, _, err := buildPdfRasterizeArgs("pdftoppm", "/tmp/in.pdf", "/tmp", "stem", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-r 150") {
		t.Errorf("expected default dpi 150, got args %q", joined)
	}
	if !strings.Contains(joined, "-f 1") || !strings.Contains(joined, "-l 1") {
		t.Errorf("expected default page 1, got args %q", joined)
	}
}

func TestBuildPdfRasterizeArgs_Unsupported(t *testing.T) {
	_, _, _, err := buildPdfRasterizeArgs("notatool", "/tmp/in.pdf", "/tmp", "stem", 1, 150)
	if err == nil {
		t.Error("expected error for unsupported rasterizer tool")
	}
}
