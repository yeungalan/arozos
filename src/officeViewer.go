package main

/*
	Office Viewer API

	Provides server-side conversion of DOCX / XLSX / PPTX files to
	JSON/HTML for rendering in the browser without heavyweight JS parsers.

	Endpoints:
	  GET /api/officepreview/convert?file=<vpath>
*/

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	officeConverter "imuslab.com/arozos/mod/officeConverter"
	"imuslab.com/arozos/mod/utils"
)

func officeViewer_init() {
	http.HandleFunc("/api/officepreview/convert", officePreview_convert)
}

// officePreview_convert handles document conversion requests.
func officePreview_convert(w http.ResponseWriter, r *http.Request) {
	// Authenticate
	userinfo, err := userHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "User not logged in")
		return
	}

	// Get virtual path
	vpath, err := utils.GetPara(r, "file")
	if err != nil || vpath == "" {
		utils.SendErrorResponse(w, "Missing 'file' parameter")
		return
	}

	// Resolve virtual path → real path
	fsh, subpath, err := GetFSHandlerSubpathFromVpath(vpath)
	if err != nil {
		utils.SendErrorResponse(w, "Path resolution failed: "+err.Error())
		return
	}
	realPath, err := fsh.FileSystemAbstraction.VirtualPathToRealPath(subpath, userinfo.Username)
	if err != nil {
		utils.SendErrorResponse(w, "Path translation failed: "+err.Error())
		return
	}

	// Check read permission
	if !userinfo.CanRead(vpath) {
		http.Error(w, "403 - Access Denied", http.StatusForbidden)
		return
	}

	// Open and read the file
	file, err := fsh.FileSystemAbstraction.ReadStream(realPath)
	if err != nil {
		utils.SendErrorResponse(w, "Cannot read file: "+err.Error())
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		utils.SendErrorResponse(w, "Cannot read file data: "+err.Error())
		return
	}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(realPath), "."))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")

	switch ext {
	case "docx":
		result, err := officeConverter.ConvertDOCX(data)
		if err != nil {
			writeConvertError(w, err.Error())
			return
		}
		writeJSON(w, map[string]interface{}{
			"type": "docx",
			"html": result.HTML,
		})

	case "xlsx", "xlsm", "xlsb":
		result, err := officeConverter.ConvertXLSX(data)
		if err != nil {
			writeConvertError(w, err.Error())
			return
		}
		writeJSON(w, map[string]interface{}{
			"type":   "xlsx",
			"sheets": result.Sheets,
		})

	case "pptx", "pptm":
		result, err := officeConverter.ConvertPPTX(data)
		if err != nil {
			writeConvertError(w, err.Error())
			return
		}
		writeJSON(w, map[string]interface{}{
			"type":   "pptx",
			"width":  result.Width,
			"height": result.Height,
			"slides": result.Slides,
		})

	default:
		writeConvertError(w, "Unsupported file format: "+ext)
	}
}

func writeConvertError(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "JSON encoding error", http.StatusInternalServerError)
	}
}
