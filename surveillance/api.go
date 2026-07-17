package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// API wires the camera/group store to HTTP handlers. Every route is namespaced
// under /surveillance/ because ArozOS reverse-proxies that prefix to this
// binary without stripping it (the directory of StartDir is the proxy
// endpoint).
type API struct {
	store *Store
}

// routePrefix is the reverse-proxy namespace this subservice serves under.
const routePrefix = "/surveillance"

// Handler returns the fully-wired http.Handler for the subservice, including
// the embedded front-end and the JSON API.
func (a *API) Handler(webFS http.Handler) http.Handler {
	mux := http.NewServeMux()

	// Front-end (embedded). Serve both the bare prefix and any static asset.
	mux.Handle("GET "+routePrefix+"/", webFS)

	// Camera CRUD.
	mux.HandleFunc("GET "+routePrefix+"/api/cameras", a.listCameras)
	mux.HandleFunc("POST "+routePrefix+"/api/cameras", a.createCamera)
	mux.HandleFunc("GET "+routePrefix+"/api/cameras/{id}", a.getCamera)
	mux.HandleFunc("PUT "+routePrefix+"/api/cameras/{id}", a.updateCamera)
	mux.HandleFunc("DELETE "+routePrefix+"/api/cameras/{id}", a.deleteCamera)
	mux.HandleFunc("POST "+routePrefix+"/api/cameras/{id}/enable", a.enableCamera)
	mux.HandleFunc("POST "+routePrefix+"/api/cameras/{id}/disable", a.disableCamera)
	mux.HandleFunc("POST "+routePrefix+"/api/cameras/{id}/test", a.testCamera)

	// Groups & tags.
	mux.HandleFunc("GET "+routePrefix+"/api/groups", a.listGroups)
	mux.HandleFunc("POST "+routePrefix+"/api/groups", a.createGroup)
	mux.HandleFunc("PUT "+routePrefix+"/api/groups/{id}", a.updateGroup)
	mux.HandleFunc("DELETE "+routePrefix+"/api/groups/{id}", a.deleteGroup)
	mux.HandleFunc("GET "+routePrefix+"/api/tags", a.listTags)

	// Search & validation of an unsaved URL.
	mux.HandleFunc("GET "+routePrefix+"/api/search", a.search)
	mux.HandleFunc("POST "+routePrefix+"/api/validate", a.validateURL)

	return mux
}

// ---- helpers -------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// decodeBody reads a JSON body with a sane size limit.
func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20)) // 1 MiB
	return dec.Decode(v)
}

// ---- camera handlers -----------------------------------------------------

func (a *API) listCameras(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListCameras())
}

func (a *API) getCamera(w http.ResponseWriter, r *http.Request) {
	c, err := a.store.GetCamera(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) createCamera(w http.ResponseWriter, r *http.Request) {
	var c Camera
	if err := decodeBody(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	created, err := a.store.AddCamera(c)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	logInfo("Surveillance", "camera added: "+created.Name)
	writeJSON(w, http.StatusCreated, created)
}

func (a *API) updateCamera(w http.ResponseWriter, r *http.Request) {
	var c Camera
	if err := decodeBody(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	updated, err := a.store.UpdateCamera(r.PathValue("id"), c)
	if err != nil {
		if err == ErrNotFound {
			writeError(w, http.StatusNotFound, "camera not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (a *API) deleteCamera(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteCamera(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) enableCamera(w http.ResponseWriter, r *http.Request) {
	c, err := a.store.SetEnabled(r.PathValue("id"), true)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) disableCamera(w http.ResponseWriter, r *http.Request) {
	c, err := a.store.SetEnabled(r.PathValue("id"), false)
	if err != nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// testCamera probes a stored camera's live reachability and records the result
// in its status/last-seen fields.
func (a *API) testCamera(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.store.mu.RLock()
	raw, ok := a.store.getRaw(id)
	var url, user, pass string
	if ok {
		if u, err := raw.connectionURL(); err == nil {
			url = u
		}
		user, pass = raw.Username, raw.Password
	}
	a.store.mu.RUnlock()
	if !ok {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}
	result := validateStream(url, user, pass, 5*time.Second)
	status := StatusOffline
	if result.Reachable {
		status = StatusOnline
	}
	if _, err := a.store.SetStatus(id, status, result.Reachable); err != nil {
		logError("Surveillance", "failed to persist camera status", err)
	}
	writeJSON(w, http.StatusOK, result)
}

// ---- group handlers ------------------------------------------------------

func (a *API) listGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.ListGroups())
}

func (a *API) createGroup(w http.ResponseWriter, r *http.Request) {
	var g Group
	if err := decodeBody(r, &g); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	created, err := a.store.AddGroup(g)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *API) updateGroup(w http.ResponseWriter, r *http.Request) {
	var g Group
	if err := decodeBody(r, &g); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	updated, err := a.store.UpdateGroup(r.PathValue("id"), g)
	if err != nil {
		if err == ErrNotFound {
			writeError(w, http.StatusNotFound, "group not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (a *API) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteGroup(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "group not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) listTags(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.Tags())
}

// ---- search & validate ---------------------------------------------------

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	q := SearchQuery{
		Text:         r.URL.Query().Get("q"),
		Status:       strings.ToLower(r.URL.Query().Get("status")),
		GroupID:      r.URL.Query().Get("group"),
		Tag:          r.URL.Query().Get("tag"),
		Manufacturer: r.URL.Query().Get("manufacturer"),
	}
	writeJSON(w, http.StatusOK, a.store.SearchCameras(q))
}

// validateURL probes an arbitrary (unsaved) RTSP URL so the UI can verify a
// camera before it is stored (spec section 2.2).
func (a *API) validateURL(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RTSPURL  string `json:"rtspUrl"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.RTSPURL) == "" {
		writeError(w, http.StatusBadRequest, "rtspUrl is required")
		return
	}
	writeJSON(w, http.StatusOK, validateStream(body.RTSPURL, body.Username, body.Password, 5*time.Second))
}
