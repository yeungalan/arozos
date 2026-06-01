package main

/*
	OfficeViewer - Server-side document conversion to PDF using LibreOffice

	Converts office documents (docx, pptx, xlsx, odt, etc.) to PDF on the server
	using LibreOffice headless, then serves the PDF to the browser for native rendering.

	API:
	  GET /OfficeViewer/api/convert?file=<virtual_path>
	      Returns application/pdf — either the original file (if already PDF)
	      or a LibreOffice-converted PDF.
*/

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	prout "imuslab.com/arozos/mod/prouter"
	"imuslab.com/arozos/mod/utils"
)

var (
	ovMu       sync.Mutex // serialise LibreOffice — only one instance at a time
	ovCacheDir string
)

// Formats LibreOffice can convert to PDF
var ovSupportedExts = map[string]bool{
	"doc": true, "docx": true, "odt": true, "fodt": true, "rtf": true,
	"xls": true, "xlsx": true, "ods": true, "fods": true, "csv": true,
	"ppt": true, "pptx": true, "odp": true, "fodp": true,
	"odg": true,
}

func OfficeViewer_init() {
	ovCacheDir = filepath.Join(filepath.Clean(*tmp_directory), "ov_cache")
	if err := os.MkdirAll(ovCacheDir, 0755); err != nil {
		log.Println("[OfficeViewer] Cannot create cache dir:", err)
	}

	router := prout.NewModuleRouter(prout.RouterOption{
		ModuleName:  "OfficeViewer",
		AdminOnly:   false,
		UserHandler: userHandler,
		DeniedHandler: func(w http.ResponseWriter, r *http.Request) {
			utils.SendErrorResponse(w, "Permission denied")
		},
	})
	router.HandleFunc("/OfficeViewer/api/convert", OfficeViewer_handleConvert)
}

// OfficeViewer_handleConvert resolves the requested virtual path, converts the
// document to PDF if necessary, and streams the PDF to the client.
func OfficeViewer_handleConvert(w http.ResponseWriter, r *http.Request) {
	userinfo, err := userHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "User not authenticated")
		return
	}

	vfile, err := utils.GetPara(r, "file")
	if err != nil {
		utils.SendErrorResponse(w, "Missing 'file' parameter")
		return
	}

	// Resolve virtual path → real disk path
	fsh, subpath, err := GetFSHandlerSubpathFromVpath(vfile)
	if err != nil {
		utils.SendErrorResponse(w, "Invalid path: "+err.Error())
		return
	}
	realpath, err := fsh.FileSystemAbstraction.VirtualPathToRealPath(subpath, userinfo.Username)
	if err != nil {
		utils.SendErrorResponse(w, "Path resolution failed: "+err.Error())
		return
	}

	stat, err := os.Stat(realpath)
	if err != nil || stat.IsDir() {
		utils.SendErrorResponse(w, "File not found")
		return
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(realpath), "."))

	// PDFs are served directly — no conversion needed
	if ext == "pdf" {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Cache-Control", "max-age=3600, private")
		http.ServeFile(w, r, realpath)
		return
	}

	if !ovSupportedExts[ext] {
		utils.SendErrorResponse(w, "Unsupported format: ."+ext)
		return
	}

	// Cache keyed on real path + modification time so stale entries are
	// automatically replaced when the source file changes.
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(realpath+stat.ModTime().String())))
	cached := filepath.Join(ovCacheDir, key+".pdf")

	if _, err := os.Stat(cached); os.IsNotExist(err) {
		if err := ovConvert(realpath, cached); err != nil {
			log.Printf("[OfficeViewer] conversion of %q failed: %v", realpath, err)
			utils.SendErrorResponse(w, "Conversion failed: "+err.Error())
			return
		}
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "max-age=3600, private")
	http.ServeFile(w, r, cached)
}

// ovConvert runs LibreOffice headless to convert inputPath to a PDF stored at
// outputPath. Only one conversion runs at a time.
func ovConvert(inputPath, outputPath string) error {
	ovMu.Lock()
	defer ovMu.Unlock()

	// Isolated working directory so the output filename is predictable
	workDir, err := os.MkdirTemp(ovCacheDir, "conv_")
	if err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "soffice",
		"--headless",
		"--norestore",
		"--nofirststartwizard",
		"--convert-to", "pdf",
		"--outdir", workDir,
		inputPath,
	)

	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timed out after 120 s")
		}
		return fmt.Errorf("%w\n%s", err, string(out))
	}

	// LibreOffice names the output <basename_no_ext>.pdf
	base := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))
	loOut := filepath.Join(workDir, base+".pdf")
	if _, err := os.Stat(loOut); err != nil {
		return fmt.Errorf("expected output not found: %s", loOut)
	}

	return os.Rename(loOut, outputPath)
}
