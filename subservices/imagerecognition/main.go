package main

import (
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
)

/*
	ArozOS Image Recognition Subservice
	===================================

	A self-contained ArozOS subservice that provides:

	  - Image tagging        – descriptive scene/colour tags, plus YOLO object
	                           tags when the optional ONNX backend is enabled.
	  - Face detection       – locate faces in a photo (pure-Go, via pigo).
	  - Face recognition     – group the same person across photos under a
	                           stable UUID and return it to the caller.

	It speaks the standard ArozOS subservice protocol (see aroz.go) so the host
	discovers it with -info and reverse-proxies user requests to it. ArozOS
	exposes the capability to AGI scripts through the "imagerecognition"
	library once an administrator enables it in
	System Settings > AI Integration > Photo Recognition.
*/

const serviceVersion = "1.0.0"

func main() {
	handler := HandleFlagParse(ServiceInfo{
		Name:         "Image Recognition",
		Desc:         "AI photo tagging and face recognition for ArozOS",
		Group:        "Utilities",
		IconPath:     "imagerecognition/img/icon.png",
		Version:      serviceVersion,
		StartDir:     "imagerecognition/index.html",
		SupportFW:    true,
		LaunchFWDir:  "imagerecognition/index.html",
		SupportEmb:   false,
		InitFWSize:   []int{840, 600},
		SupportedExt: []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp"},
	})

	lg := newSvcLogger("[ImageRecognition]")

	dataDir := resolveDataDir()
	if err := os.MkdirAll(dataDir, 0775); err != nil {
		lg.Err("could not create data directory "+dataDir, err)
		os.Exit(1)
	}
	lg.logf("data directory: %s", dataDir)

	recognizer, err := NewRecognizer(dataDir, lg)
	if err != nil {
		lg.Err("failed to initialise recognizer", err)
		os.Exit(1)
	}
	defer recognizer.Close()

	server := newServer(recognizer, ServiceInfo{Name: "Image Recognition"}, serviceVersion, lg)

	mux := http.NewServeMux()
	server.routes(mux)
	//Serve the subservice's own web UI (and icon) at the proxy root.
	mux.Handle("/", http.FileServer(http.Dir(webRoot())))

	setupSignalHandler(lg)

	lg.logf("Image Recognition subservice %s listening on %s", serviceVersion, handler.Port)
	if err := http.ListenAndServe(handler.Port, mux); err != nil {
		lg.Err("http server stopped", err)
		os.Exit(1)
	}
}

// resolveDataDir picks where to persist the people gallery, preferring the
// IMGRECOG_DATA environment variable and otherwise a "data" folder next to the
// executable. Paths are built with filepath.Join so the service stays portable.
func resolveDataDir() string {
	if env := os.Getenv("IMGRECOG_DATA"); env != "" {
		return env
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "data")
	}
	return filepath.Join(".", "data")
}

// webRoot resolves the static web asset directory next to the executable,
// falling back to ./web for development runs.
func webRoot() string {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "web")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(".", "web")
}

// setupSignalHandler exits cleanly on Ctrl-C for standalone runs. (ArozOS stops
// subservices by killing the process, which needs no handler.)
func setupSignalHandler(lg *svcLogger) {
	c := make(chan os.Signal, 2)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		lg.Info("shutting down")
		os.Exit(0)
	}()
}
