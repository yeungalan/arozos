/*
	photoai — ArozOS Photo AI subservice

	Provides photo tagging (colour, scene, EXIF-derived) and skin-tone-based
	face detection with per-user clustering.

	Launch flags (set automatically by the ArozOS subservice loader):
	  -port  :PORT   port to listen on (e.g. ":12320")
	  -rpt   URL     ArozOS AGI reverse-proxy-token endpoint (unused at runtime
	                 but required by the subservice contract)

	All HTTP routes are served at the path prefix that ArozOS proxies based on
	moduleInfo.json StartDir ("photoai/…"), so the subservice sees paths like
	"/index.html", "/api/analyze", etc.
*/
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const version = "1.0.0"

var (
	listenPort  string
	rptEndpoint string
	storage     *FaceStorage
)

func main() {
	flag.StringVar(&listenPort, "port", ":12320", "listen address (e.g. :12320)")
	flag.StringVar(&rptEndpoint, "rpt", "", "ArozOS AGI endpoint (passed by subservice loader)")
	flag.Parse()

	// Open (or create) the per-subservice face database in the working directory.
	var err error
	storage, err = openFaceStorage("data")
	if err != nil {
		fmt.Fprintf(os.Stderr, "photoai: open storage: %v\n", err)
		os.Exit(1)
	}
	defer storage.Close()

	mux := http.NewServeMux()

	// UI / info page.
	mux.HandleFunc("/", handleRoot)
	mux.HandleFunc("/index.html", handleUI)
	mux.HandleFunc("/icon.svg", handleIcon)

	// API routes.
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/analyze", handleAnalyze)
	mux.HandleFunc("/api/faces", handleFaces)
	mux.HandleFunc("/api/faces/label", handleFaceLabel)
	mux.HandleFunc("/api/faces/merge", handleFaceMerge)
	mux.HandleFunc("/api/faces/delete", handleFaceDelete)
	mux.HandleFunc("/api/faces/photo", handleFacesInPhoto)

	fmt.Fprintf(os.Stderr, "photoai %s listening on %s\n", version, listenPort)

	srv := &http.Server{
		Addr:         listenPort,
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "photoai: server error: %v\n", err)
		os.Exit(1)
	}
}

// aoUser extracts the username injected by ArozOS's reverse proxy.
func aoUser(r *http.Request) string {
	u := r.Header.Get("aouser")
	if u == "" {
		u = "anonymous"
	}
	return u
}

func sendJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "photoai: json encode: %v\n", err)
	}
}

func sendError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// handleRoot redirects / to /index.html so the module UI loads when navigated
// to without a trailing path.
func handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" || r.URL.Path == "" {
		http.Redirect(w, r, "index.html", http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

func handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(uiHTML))
}

func handleIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write([]byte(iconSVG))
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	sendJSON(w, map[string]interface{}{
		"ok":      true,
		"version": version,
		"service": "Photo AI",
	})
}

// handleAnalyze decodes the submitted image (base64 data-URL), runs colour
// analysis and face detection, and returns the results.
//
// POST /api/analyze
//
//	Body (JSON):
//	  { "imageData": "data:image/jpeg;base64,...",
//	    "vpath":     "user:/Photo/img.jpg",         // for reference only
//	    "hints":     { "megapixels": "12.5", ... }  // optional EXIF hints
//	  }
func handleAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	var req struct {
		ImageData string            `json:"imageData"`
		VPath     string            `json:"vpath"`
		Hints     map[string]string `json:"hints"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sendError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ImageData == "" {
		sendError(w, http.StatusBadRequest, "imageData is required")
		return
	}

	analysis, err := AnalyzeImageData(req.ImageData, req.Hints)
	if err != nil {
		sendError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Save face detections if a vpath and username are provided.
	username := aoUser(r)
	var clusterIDs []string
	if req.VPath != "" && len(analysis.Faces) > 0 && storage != nil {
		clusterIDs, _ = storage.SaveDetections(username, req.VPath, analysis.Faces)
	}

	type faceResp struct {
		FaceDetection
		ClusterID string `json:"cluster_id"`
	}
	faceResps := make([]faceResp, len(analysis.Faces))
	for i, f := range analysis.Faces {
		faceResps[i] = faceResp{FaceDetection: f}
		if i < len(clusterIDs) {
			faceResps[i].ClusterID = clusterIDs[i]
		}
	}

	sendJSON(w, map[string]interface{}{
		"tags":  analysis.Tags,
		"faces": faceResps,
	})
}

// handleFaces returns all known face clusters for the requesting user.
//
// GET /api/faces
func handleFaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	if storage == nil {
		sendError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	clusters, err := storage.ListClusters(aoUser(r))
	if err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if clusters == nil {
		clusters = []FaceCluster{}
	}
	sendJSON(w, clusters)
}

// handleFacesInPhoto returns face detections for a specific photo.
//
// GET /api/faces/photo?vpath=user%3A%2FPhoto%2Fimg.jpg
func handleFacesInPhoto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		sendError(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	vpath, _ := url.QueryUnescape(r.URL.Query().Get("vpath"))
	if vpath == "" {
		sendError(w, http.StatusBadRequest, "vpath required")
		return
	}
	if storage == nil {
		sendJSON(w, []FaceDetectionRecord{})
		return
	}
	dets, err := storage.GetFacesInPhoto(aoUser(r), vpath)
	if err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if dets == nil {
		dets = []FaceDetectionRecord{}
	}
	sendJSON(w, dets)
}

// handleFaceLabel renames a face cluster.
//
// POST /api/faces/label   Body: { "cluster_id": "…", "name": "Alice" }
func handleFaceLabel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req struct {
		ClusterID string `json:"cluster_id"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ClusterID == "" {
		sendError(w, http.StatusBadRequest, "cluster_id and name required")
		return
	}
	if storage == nil {
		sendError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	if err := storage.LabelCluster(aoUser(r), req.ClusterID, strings.TrimSpace(req.Name)); err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sendJSON(w, map[string]bool{"ok": true})
}

// handleFaceMerge merges srcID into dstID.
//
// POST /api/faces/merge   Body: { "src_id": "…", "dst_id": "…" }
func handleFaceMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req struct {
		SrcID string `json:"src_id"`
		DstID string `json:"dst_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SrcID == "" || req.DstID == "" {
		sendError(w, http.StatusBadRequest, "src_id and dst_id required")
		return
	}
	if storage == nil {
		sendError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	if err := storage.MergeClusters(aoUser(r), req.SrcID, req.DstID); err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sendJSON(w, map[string]bool{"ok": true})
}

// handleFaceDelete deletes a cluster and all its detections.
//
// POST /api/faces/delete   Body: { "cluster_id": "…" }
func handleFaceDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req struct {
		ClusterID string `json:"cluster_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ClusterID == "" {
		sendError(w, http.StatusBadRequest, "cluster_id required")
		return
	}
	if storage == nil {
		sendError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}
	if err := storage.DeleteCluster(aoUser(r), req.ClusterID); err != nil {
		sendError(w, http.StatusInternalServerError, err.Error())
		return
	}
	sendJSON(w, map[string]bool{"ok": true})
}

// iconSVG is the module icon served at /icon.svg.
const iconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect width="64" height="64" rx="12" fill="#4F8EF7"/>
  <circle cx="22" cy="24" r="6" fill="white" opacity=".9"/>
  <circle cx="42" cy="24" r="6" fill="white" opacity=".9"/>
  <rect x="10" y="38" width="44" height="6" rx="3" fill="white" opacity=".7"/>
  <rect x="16" y="48" width="32" height="4" rx="2" fill="white" opacity=".5"/>
</svg>`

// uiHTML is the admin / settings page served at /index.html.
const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Photo AI</title>
<style>
  :root { --bg:#f5f7fa; --card:#fff; --accent:#4F8EF7; --text:#222; --sub:#666; }
  *{box-sizing:border-box;margin:0;padding:0}
  body{font-family:system-ui,sans-serif;background:var(--bg);color:var(--text);padding:24px}
  h1{font-size:1.4rem;margin-bottom:4px;color:var(--accent)}
  .sub{color:var(--sub);font-size:.9rem;margin-bottom:24px}
  .card{background:var(--card);border-radius:10px;padding:20px;margin-bottom:16px;box-shadow:0 1px 4px rgba(0,0,0,.08)}
  .card h2{font-size:1rem;margin-bottom:12px}
  .stat{display:flex;justify-content:space-between;padding:6px 0;border-bottom:1px solid #f0f0f0;font-size:.9rem}
  .stat:last-child{border-bottom:none}
  .badge{background:var(--accent);color:#fff;border-radius:20px;padding:2px 10px;font-size:.8rem}
  .badge.ok{background:#22c55e}
  .btn{display:inline-block;padding:8px 20px;border-radius:6px;background:var(--accent);color:#fff;cursor:pointer;font-size:.9rem;border:none}
  .btn:hover{opacity:.85}
  table{width:100%;border-collapse:collapse;font-size:.85rem}
  th,td{text-align:left;padding:6px 8px;border-bottom:1px solid #f0f0f0}
  th{color:var(--sub);font-weight:600}
  input{width:100%;padding:6px 8px;border:1px solid #ddd;border-radius:5px;font-size:.85rem}
  .row{display:flex;gap:8px;align-items:center}
</style>
</head>
<body>
<h1>📷 Photo AI</h1>
<p class="sub">ArozOS photo tagging &amp; face recognition subservice v` + version + `</p>

<div class="card">
  <h2>Status</h2>
  <div class="stat"><span>Service</span><span class="badge ok" id="statusBadge">checking…</span></div>
  <div class="stat"><span>Face clusters</span><span id="clusterCount">—</span></div>
</div>

<div class="card">
  <h2>Face Clusters</h2>
  <p style="font-size:.85rem;color:var(--sub);margin-bottom:12px">
    The Photo app groups detected faces automatically. Name the people below to enable person search.
  </p>
  <table>
    <thead><tr><th>Name</th><th>Photos</th><th></th></tr></thead>
    <tbody id="clusterTable"><tr><td colspan="3" style="color:var(--sub)">Loading…</td></tr></tbody>
  </table>
</div>

<div class="card">
  <h2>How to use</h2>
  <p style="font-size:.85rem;color:var(--sub);line-height:1.6">
    Open the <strong>Photo</strong> app → select any photo → click
    <strong>AI Analyse</strong> in the toolbar to tag it and detect faces.<br>
    Use the search bar with <code>tag:warm</code>, <code>tag:people</code>, or
    <code>person:Alice</code> to filter your library.
  </p>
</div>

<script>
(function(){
  fetch('api/status').then(r=>r.json()).then(d=>{
    document.getElementById('statusBadge').textContent = d.ok ? 'Running' : 'Error';
  }).catch(()=>{
    document.getElementById('statusBadge').textContent = 'Unreachable';
    document.getElementById('statusBadge').className = 'badge';
  });

  function loadClusters(){
    fetch('api/faces').then(r=>r.json()).then(clusters=>{
      document.getElementById('clusterCount').textContent = clusters.length;
      var tbody = document.getElementById('clusterTable');
      if(!clusters.length){
        tbody.innerHTML = '<tr><td colspan="3" style="color:var(--sub)">No faces detected yet. Open the Photo app and analyse some photos.</td></tr>';
        return;
      }
      tbody.innerHTML = clusters.map(function(c){
        return '<tr><td><div class="row"><input type="text" value="'+escHtml(c.name)+'" data-id="'+c.id+'" placeholder="Unnamed person" style="max-width:180px"></div></td>'+
               '<td>'+c.count+'</td>'+
               '<td><button class="btn" style="padding:4px 12px;font-size:.8rem" onclick="saveLabel(this)">Save</button></td></tr>';
      }).join('');
    });
  }

  window.saveLabel = function(btn){
    var tr = btn.closest('tr');
    var inp = tr.querySelector('input');
    fetch('api/faces/label',{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({cluster_id:inp.dataset.id, name:inp.value})
    }).then(function(){ loadClusters(); });
  };

  function escHtml(s){ return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }

  loadClusters();
})();
</script>
</body>
</html>`
