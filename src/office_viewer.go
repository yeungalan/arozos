package main

/*
	Office Viewer - server-side helpers
	Provides a /api/office-viewer/wmf endpoint that converts a raw WMF/EMF
	blob (POSTed as application/octet-stream) to SVG, enabling the browser
	to display images that are embedded in DOCX files as Windows Metafiles.
*/

import (
	"io"
	"net/http"

	utils "imuslab.com/arozos/mod/utils"
	"imuslab.com/arozos/mod/wmfrender"
)

func OfficeViewerInit() {
	// POST /api/office-viewer/wmf
	// Body: raw WMF bytes (application/octet-stream)
	// Response: SVG document (image/svg+xml) or 400/500 on error
	http.HandleFunc("/api/office-viewer/wmf", officeViewerHandleWMF)
}

func officeViewerHandleWMF(w http.ResponseWriter, r *http.Request) {
	// Only authenticated users may use this endpoint (same session required
	// to read the DOCX via /media, so the WMF bytes come from there).
	_, err := userHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "Unauthorized")
		return
	}

	if r.Method != http.MethodPost {
		utils.SendErrorResponse(w, "Method not allowed")
		return
	}

	// Limit to 16 MB to protect against abusive payloads.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	svg, err := wmfrender.ToSVG(data)
	if err != nil {
		http.Error(w, "WMF conversion failed: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(svg)
}
