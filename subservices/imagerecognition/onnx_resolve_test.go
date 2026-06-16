//go:build onnx

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolveORTLibPathPrefersOSLibrary checks that when both a Linux .so and a
// Windows .dll are bundled (as in this repo), the loader picks the one matching
// the current OS — even when model.json points at the other platform's path.
func TestResolveORTLibPathPrefersOSLibrary(t *testing.T) {
	t.Setenv("ONNXRUNTIME_LIB", "") //ignore any ambient override

	dir := t.TempDir()
	linuxLib := filepath.Join(dir, "onnxruntime-linux-x64-1.26.0", "lib")
	winLib := filepath.Join(dir, "onnxruntime-win-x64-1.26.0", "lib")
	if err := os.MkdirAll(linuxLib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(winLib, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(linuxLib, "libonnxruntime.so.1.26.0"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(winLib, "onnxruntime.dll"), []byte("x"), 0o644)

	//model.json (as committed) points at the Linux path.
	got := resolveORTLibPath(dir, "onnxruntime-linux-x64-1.26.0/lib/libonnxruntime.so.1.26.0")

	wantExt := ".so"
	if runtime.GOOS == "windows" {
		wantExt = ".dll"
	} else if runtime.GOOS == "darwin" {
		wantExt = ".dylib"
	}
	if !strings.Contains(strings.ToLower(got), wantExt) {
		t.Errorf("resolveORTLibPath = %q, want a %s library for %s", got, wantExt, runtime.GOOS)
	}
}

func TestLibMatchesOS(t *testing.T) {
	got := libMatchesOS("/x/onnxruntime.dll")
	if want := runtime.GOOS == "windows"; got != want {
		t.Errorf("libMatchesOS(.dll) = %v, want %v on %s", got, want, runtime.GOOS)
	}
	got = libMatchesOS("/x/libonnxruntime.so.1.26.0")
	if want := runtime.GOOS != "windows" && runtime.GOOS != "darwin"; got != want {
		t.Errorf("libMatchesOS(.so) = %v, want %v on %s", got, want, runtime.GOOS)
	}
}
