package main

import (
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// version is the subservice version, surfaced in ModuleInfo and the RTSP
// User-Agent.
const version = "0.1.0"

//go:embed web
var webAssets embed.FS

func main() {
	info := flag.Bool("info", false, "Print module info as JSON and exit")
	port := flag.String("port", ":12810", "Listen address assigned by ArozOS (e.g. :12810)")
	rpt := flag.String("rpt", "", "ArozOS AGI gateway endpoint for callbacks")
	flag.Parse()

	// Handshake: ArozOS probes the binary with -info before launching it.
	if *info {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(moduleInfo()); err != nil {
			logError("Surveillance", "failed to encode module info", err)
			os.Exit(1)
		}
		return
	}

	if *rpt != "" {
		logInfo("Surveillance", "AGI callback endpoint: "+*rpt)
	}

	// Persist camera config next to the binary so it survives restarts. Falls
	// back to the OS temp dir if the executable path cannot be resolved.
	dataDir := dataDirectory()
	store, err := NewStore(filepath.Join(dataDir, "cameras.json"))
	if err != nil {
		logError("Surveillance", "failed to open camera store", err)
		os.Exit(1)
	}
	logInfo("Surveillance", "camera store ready at "+filepath.Join(dataDir, "cameras.json"))

	// The front-end is embedded under web/; serve it beneath the proxy prefix.
	sub, err := fs.Sub(webAssets, "web")
	if err != nil {
		logError("Surveillance", "failed to mount embedded web assets", err)
		os.Exit(1)
	}
	webHandler := http.StripPrefix(routePrefix+"/", http.FileServer(http.FS(sub)))

	api := &API{store: store}
	handler := api.Handler(webHandler)

	addr := normaliseAddr(*port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	logInfo("Surveillance", "listening on "+addr)
	if err := srv.ListenAndServe(); err != nil {
		logError("Surveillance", "server stopped", err)
		os.Exit(1)
	}
}

// dataDirectory returns a writable directory for persisted state, preferring a
// "data" folder beside the executable and falling back to the OS temp dir.
func dataDirectory() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "data")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			return dir
		}
	}
	return filepath.Join(os.TempDir(), "arozos-surveillance")
}

// normaliseAddr accepts either ":12810" or "12810" (the ArozOS .intport
// convention) and returns a valid listen address.
func normaliseAddr(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ":12810"
	}
	if !strings.Contains(p, ":") {
		return ":" + p
	}
	return p
}
