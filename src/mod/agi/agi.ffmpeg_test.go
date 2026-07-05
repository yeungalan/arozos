package agi

import (
	"os/exec"
	"testing"
)

// TestFFmpegLibRegisterFollowsPath verifies the ffmpeg AGI library registers
// exactly when ffmpeg is reachable on the host PATH, independent of any OS
// package-manager probe. This guards the regression where Cine Studio MP4
// export was disabled on hosts (notably Windows) that had ffmpeg installed
// but were misreported by the removed apt.PackageExists gate.
func TestFFmpegLibRegisterFollowsPath(t *testing.T) {
	g := &Gateway{
		LoadedAGILibrary: map[string]AgiLibInjectionIntergface{},
	}

	g.FFmpegLibRegister()

	_, registered := g.LoadedAGILibrary["ffmpeg"]
	_, lookErr := exec.LookPath("ffmpeg")
	ffmpegOnPath := lookErr == nil

	switch {
	case ffmpegOnPath && !registered:
		t.Fatalf("ffmpeg is on PATH but the ffmpeg lib was not registered")
	case !ffmpegOnPath && registered:
		t.Fatalf("ffmpeg is absent from PATH but the ffmpeg lib was registered")
	default:
		t.Logf("ffmpeg on PATH=%v, registered=%v (consistent)", ffmpegOnPath, registered)
	}
}

// TestFFmpegLibRegisterIsIdempotent ensures a second registration attempt does
// not panic or double-register (RegisterLib refuses duplicates).
func TestFFmpegLibRegisterIsIdempotent(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH; nothing to register")
	}

	g := &Gateway{
		LoadedAGILibrary: map[string]AgiLibInjectionIntergface{},
	}
	g.FFmpegLibRegister()
	g.FFmpegLibRegister()

	if _, ok := g.LoadedAGILibrary["ffmpeg"]; !ok {
		t.Fatalf("ffmpeg lib should remain registered after a repeated call")
	}
}
