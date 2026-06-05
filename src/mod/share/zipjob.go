package share

import (
	"compress/flate"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	filesystem "imuslab.com/arozos/mod/filesystem"
	"imuslab.com/arozos/mod/filesystem/arozfs"
	"imuslab.com/arozos/mod/info/logger"
	"imuslab.com/arozos/mod/share/shareEntry"
	uuid "github.com/satori/go.uuid"
)

type zipJobStatus string

const (
	zipStatusPending zipJobStatus = "pending"
	zipStatusRunning zipJobStatus = "running"
	zipStatusDone    zipJobStatus = "done"
	zipStatusFailed  zipJobStatus = "failed"
)

type zipJob struct {
	ID          string
	ZipFilePath string
	ZipFilename string
	Status      zipJobStatus
	Progress    float64
	CurrentFile string
	FilesDone   int
	FilesTotal  int
	ErrMsg      string
	CreatedAt   time.Time
	mu          sync.Mutex
	subscribers []chan zipEvent
}

type zipEvent struct {
	Type        string  `json:"type"`
	Status      string  `json:"status"`
	Progress    float64 `json:"progress"`
	CurrentFile string  `json:"currentFile"`
	FilesDone   int     `json:"filesDone"`
	FilesTotal  int     `json:"filesTotal"`
	Error       string  `json:"error,omitempty"`
}

func newZipJob() *zipJob {
	return &zipJob{
		ID:          uuid.NewV4().String(),
		Status:      zipStatusPending,
		CreatedAt:   time.Now(),
		subscribers: []chan zipEvent{},
	}
}

func (j *zipJob) broadcast(ev zipEvent) {
	j.mu.Lock()
	j.Status = zipJobStatus(ev.Status)
	j.Progress = ev.Progress
	j.CurrentFile = ev.CurrentFile
	j.FilesDone = ev.FilesDone
	j.FilesTotal = ev.FilesTotal
	if ev.Error != "" {
		j.ErrMsg = ev.Error
	}
	subs := make([]chan zipEvent, len(j.subscribers))
	copy(subs, j.subscribers)
	j.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (j *zipJob) subscribe() chan zipEvent {
	j.mu.Lock()
	defer j.mu.Unlock()
	ch := make(chan zipEvent, 32)
	j.subscribers = append(j.subscribers, ch)
	return ch
}

func (j *zipJob) unsubscribe(ch chan zipEvent) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for i, sub := range j.subscribers {
		if sub == ch {
			j.subscribers = append(j.subscribers[:i], j.subscribers[i+1:]...)
			close(ch)
			return
		}
	}
}

func (j *zipJob) snapshot() zipEvent {
	j.mu.Lock()
	defer j.mu.Unlock()
	return zipEvent{
		Type:        "progress",
		Status:      string(j.Status),
		Progress:    j.Progress,
		CurrentFile: j.CurrentFile,
		FilesDone:   j.FilesDone,
		FilesTotal:  j.FilesTotal,
		Error:       j.ErrMsg,
	}
}

// checkSharePermission validates access and writes an error response if denied.
// Returns true only when the caller may proceed.
func (s *Manager) checkSharePermission(w http.ResponseWriter, r *http.Request, shareOption *shareEntry.ShareOption) bool {
	switch shareOption.Permission {
	case "anyone":
		return true
	case "signedin":
		if !s.options.AuthAgent.CheckAuth(r) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("401 - Unauthorized"))
			return false
		}
		return true
	case "samegroup":
		thisuserinfo, err := s.options.UserHandler.GetUserInfoFromRequest(w, r)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("401 - Unauthorized"))
			return false
		}
		thisGroupNames := []string{}
		for _, pg := range thisuserinfo.PermissionGroup {
			thisGroupNames = append(thisGroupNames, pg.Name)
		}
		for _, required := range shareOption.Accessibles {
			found := false
			for _, g := range thisGroupNames {
				if g == required {
					found = true
					break
				}
			}
			if !found {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte("403 - Forbidden"))
				return false
			}
		}
		return true
	case "users":
		thisuserinfo, err := s.options.UserHandler.GetUserInfoFromRequest(w, r)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("401 - Unauthorized"))
			return false
		}
		for _, allowed := range shareOption.Accessibles {
			if allowed == thisuserinfo.Username || shareOption.Owner == thisuserinfo.Username {
				return true
			}
		}
		if shareOption.Owner == thisuserinfo.Username {
			return true
		}
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("403 - Forbidden"))
		return false
	case "groups":
		thisuserinfo, err := s.options.UserHandler.GetUserInfoFromRequest(w, r)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte("401 - Unauthorized"))
			return false
		}
		thisGroupNames := []string{}
		for _, pg := range thisuserinfo.PermissionGroup {
			thisGroupNames = append(thisGroupNames, pg.Name)
		}
		for _, thisGroup := range thisGroupNames {
			for _, allowed := range shareOption.Accessibles {
				if thisGroup == allowed {
					return true
				}
			}
		}
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("403 - Forbidden"))
		return false
	default:
		http.NotFound(w, r)
		return false
	}
}

// handleStartZipJob handles POST/GET /share/zip/{shareUUID}
// Starts an async zip job and returns {"jobId":"..."} as JSON.
func (s *Manager) handleStartZipJob(w http.ResponseWriter, r *http.Request, shareUUID string) {
	val, ok := s.options.ShareEntryTable.UrlToFileMap.Load(shareUUID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	shareOption := val.(*shareEntry.ShareOption)

	if !s.checkSharePermission(w, r, shareOption) {
		return
	}

	if !shareOption.IsFolder {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400 - Target is not a folder share"))
		return
	}

	owner, err := s.options.UserHandler.GetUserInfoFromUsername(shareOption.Owner)
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("403 - Share owner account not found"))
		return
	}

	targetFsh, err := owner.GetFileSystemHandlerFromVirtualPath(shareOption.FileVirtualPath)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("500 - Unable to load shared file system"))
		return
	}

	targetFshAbs := targetFsh.FileSystemAbstraction
	fileRuntimeAbsPath, _ := targetFshAbs.VirtualPathToRealPath(shareOption.FileVirtualPath, owner.Username)
	if !targetFshAbs.FileExists(fileRuntimeAbsPath) {
		http.NotFound(w, r)
		return
	}

	compressionLevel := flate.DefaultCompression
	if cl := r.URL.Query().Get("compression_level"); cl != "" {
		if v, err := strconv.Atoi(cl); err == nil && v >= -2 && v <= 9 {
			compressionLevel = v
		}
	}

	job := newZipJob()
	job.ZipFilename = arozfs.Base(shareOption.FileRealPath) + ".zip"

	s.zipJobMu.Lock()
	s.zipJobs[job.ID] = job
	s.zipJobMu.Unlock()

	go s.runZipJob(job, targetFsh, targetFshAbs, fileRuntimeAbsPath, shareOption, compressionLevel)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"jobId": job.ID})
}

func (s *Manager) runZipJob(job *zipJob, targetFsh *filesystem.FileSystemHandler, targetFshAbs filesystem.FileSystemAbstraction, fileRuntimeAbsPath string, shareOption *shareEntry.ShareOption, compressionLevel int) {
	tmpFolder := filepath.Join(s.options.TmpFolder, "share-cache")
	os.MkdirAll(tmpFolder, 0755)
	targetZipFilepath := filepath.Join(tmpFolder, job.ID+".zip")
	job.ZipFilePath = targetZipFilepath

	job.broadcast(zipEvent{Type: "progress", Status: string(zipStatusRunning), Progress: 0})

	zippingSource := shareOption.FileRealPath
	localBuff := ""
	zippingSourceFsh := targetFsh

	if targetFsh.RequireBuffer {
		localBuff = filepath.Join(tmpFolder, job.ID+"-buff", arozfs.Base(fileRuntimeAbsPath))
		os.MkdirAll(localBuff, 0755)

		targetFshAbs.Walk(fileRuntimeAbsPath, func(path string, info fs.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			relPath := strings.TrimPrefix(filepath.ToSlash(path), filepath.ToSlash(fileRuntimeAbsPath))
			localPath := filepath.Join(localBuff, relPath)
			if info.IsDir() {
				os.MkdirAll(localPath, 0755)
			} else {
				f, err := targetFshAbs.ReadStream(path)
				if err != nil {
					return nil
				}
				defer f.Close()
				dest, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY, 0775)
				if err != nil {
					return nil
				}
				defer dest.Close()
				io.Copy(dest, f)
			}
			return nil
		})

		zippingSource = localBuff
		zippingSourceFsh = nil
	}

	err := filesystem.ArozZipFileWithProgressAndCompression(
		[]*filesystem.FileSystemHandler{zippingSourceFsh},
		[]string{zippingSource},
		nil,
		targetZipFilepath,
		false,
		compressionLevel,
		func(filename string, done, total int, pct float64) int {
			job.broadcast(zipEvent{
				Type:        "progress",
				Status:      string(zipStatusRunning),
				Progress:    pct,
				CurrentFile: filename,
				FilesDone:   done,
				FilesTotal:  total,
			})
			return 0
		},
	)

	if targetFsh.RequireBuffer && localBuff != "" {
		os.RemoveAll(filepath.Dir(localBuff))
	}

	if err != nil {
		job.broadcast(zipEvent{
			Type:   "error",
			Status: string(zipStatusFailed),
			Error:  err.Error(),
		})
		logger.PrintAndLog("Share", "Async zip job "+job.ID+" failed: "+err.Error(), nil)
		return
	}

	job.broadcast(zipEvent{
		Type:     "done",
		Status:   string(zipStatusDone),
		Progress: 100,
	})

	// Auto-cleanup after 1 hour if not yet downloaded
	go func() {
		time.Sleep(1 * time.Hour)
		os.Remove(targetZipFilepath)
		s.zipJobMu.Lock()
		delete(s.zipJobs, job.ID)
		s.zipJobMu.Unlock()
	}()
}

// handleZipProgress serves a Server-Sent Events stream for GET /share/zip/progress/{jobID}
func (s *Manager) handleZipProgress(w http.ResponseWriter, r *http.Request, jobID string) {
	s.zipJobMu.RLock()
	job, ok := s.zipJobs[jobID]
	s.zipJobMu.RUnlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("500 - Streaming not supported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send current state immediately so the client sees progress on reconnect
	current := job.snapshot()
	writeSSE(w, flusher, current)

	if current.Status == string(zipStatusDone) || current.Status == string(zipStatusFailed) {
		return
	}

	ch := job.subscribe()
	defer job.unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case ev, open := <-ch:
			if !open {
				return
			}
			writeSSE(w, flusher, ev)
			if ev.Status == string(zipStatusDone) || ev.Status == string(zipStatusFailed) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, ev zipEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// handleZipDownload serves the completed zip for GET /share/zip/download/{jobID}
func (s *Manager) handleZipDownload(w http.ResponseWriter, r *http.Request, jobID string) {
	s.zipJobMu.RLock()
	job, ok := s.zipJobs[jobID]
	s.zipJobMu.RUnlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	job.mu.Lock()
	status := job.Status
	zipPath := job.ZipFilePath
	filename := job.ZipFilename
	job.mu.Unlock()

	if status != zipStatusDone {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte("202 - Zip file not ready yet"))
		return
	}

	if !filesystem.FileExists(zipPath) {
		w.WriteHeader(http.StatusGone)
		w.Write([]byte("410 - Zip file has already been removed"))
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+strings.ReplaceAll(url.QueryEscape(filename), "+", "%20"))
	w.Header().Set("Content-Type", "application/zip")
	http.ServeFile(w, r, zipPath)

	// Remove zip and job after serving
	go func() {
		time.Sleep(10 * time.Second)
		os.Remove(zipPath)
		s.zipJobMu.Lock()
		delete(s.zipJobs, jobID)
		s.zipJobMu.Unlock()
	}()
}
