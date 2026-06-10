package applenotessync

/*
	Apple Notes Sync Module

	Runs an IMAP server inside arozos that exposes each user's Notes
	(user:/Document/Notes) as the "Notes" mailbox Apple Notes expects.
	Apple devices connect to arozos as a regular IMAP account with
	Notes enabled, so the arozos Notes app is the single source of truth.

	Apple Notes over IMAP stores each note as an RFC 2822 message with
	Content-Type: text/html and these identifying headers:
	  X-Uniform-Type-Identifier:      com.apple.mail-note
	  X-Universally-Unique-Identifier: <note UUID, stable across edits>
*/

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/emersion/go-imap/server"

	auth "imuslab.com/arozos/mod/auth"
	db "imuslab.com/arozos/mod/database"
	"imuslab.com/arozos/mod/info/logger"
	user "imuslab.com/arozos/mod/user"
	"imuslab.com/arozos/mod/utils"
)

const dbTable = "apple_notes_imap"

// Options holds dependencies for the IMAP notes server.
type Options struct {
	UserHandler *user.UserHandler
	AuthAgent   *auth.AuthAgent
	Database    *db.Database
	Logger      *logger.Logger
}

// ServerConfig is the admin-managed configuration of the IMAP listener.
type ServerConfig struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
}

// Handler owns the IMAP server lifecycle and the HTTP management endpoints.
type Handler struct {
	opts Options

	mu       sync.Mutex
	server   *server.Server
	listener net.Listener
	running  bool
	lastErr  string

	// Maps a username to the real path of their Notes directory; replaced
	// in tests to avoid the full user / storage subsystem
	notesDirResolver func(username string) (string, error)

	// Serializes mailbox state mutation per user (reconcile / append / expunge)
	userLocks sync.Map // username -> *sync.Mutex
}

// NewHandler creates the handler, ensures the DB table exists and starts
// the IMAP server if it was enabled previously.
func NewHandler(opts Options) *Handler {
	opts.Database.NewTable(dbTable)
	h := &Handler{opts: opts}
	h.notesDirResolver = h.resolveUserNotesDir

	cfg := h.loadServerConfig()
	if cfg.Enabled {
		if err := h.startServer(cfg.Port); err != nil {
			h.lastErr = err.Error()
			opts.Logger.PrintAndLog("AppleNotesSync", "IMAP server failed to start: "+err.Error(), err)
		}
	}
	return h
}

func (h *Handler) lockUser(username string) *sync.Mutex {
	m, _ := h.userLocks.LoadOrStore(username, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// ── Server lifecycle ──────────────────────────────────────────────────────────

func (h *Handler) startServer(port int) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.running {
		return nil
	}
	if port <= 0 {
		port = 143
	}

	be := &imapBackend{handler: h}
	s := server.New(be)
	s.Addr = fmt.Sprintf(":%d", port)
	// LAN usage: allow LOGIN over plaintext connections. Apple devices must
	// have "Use SSL" switched off for this account.
	s.AllowInsecureAuth = true

	l, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}

	h.server = s
	h.listener = l
	h.running = true
	h.lastErr = ""

	go func() {
		serveErr := s.Serve(l)
		h.mu.Lock()
		if h.running { // unexpected exit, not a manual stop
			h.lastErr = serveErr.Error()
			h.running = false
		}
		h.mu.Unlock()
	}()

	h.opts.Logger.PrintAndLog("AppleNotesSync", fmt.Sprintf("IMAP notes server started on port %d", port), nil)
	return nil
}

func (h *Handler) stopServer() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.running {
		return
	}
	h.running = false
	if h.server != nil {
		h.server.Close()
		h.server = nil
		h.listener = nil
	}
	h.opts.Logger.PrintAndLog("AppleNotesSync", "IMAP notes server stopped", nil)
}

// ── HTTP handlers ─────────────────────────────────────────────────────────────

// HandleStatus returns server state plus the per-user connection hints.
// Available to all authenticated users.
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	userinfo, err := h.opts.UserHandler.GetUserInfoFromRequest(w, r)
	if err != nil {
		utils.SendErrorResponse(w, "authentication required")
		return
	}

	cfg := h.loadServerConfig()
	h.mu.Lock()
	running := h.running
	lastErr := h.lastErr
	h.mu.Unlock()

	resp := map[string]interface{}{
		"enabled":   cfg.Enabled,
		"running":   running,
		"port":      cfg.Port,
		"lastError": lastErr,
		"username":  userinfo.Username,
		"isAdmin":   userinfo.IsAdmin(),
	}
	js, _ := json.Marshal(resp)
	utils.SendJSONResponse(w, string(js))
}

// HandleConfig gets or sets the IMAP server configuration. Admin only
// (enforced by the admin router this is registered on).
func (h *Handler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := h.loadServerConfig()
		js, _ := json.Marshal(cfg)
		utils.SendJSONResponse(w, string(js))

	case http.MethodPost:
		var incoming ServerConfig
		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			utils.SendErrorResponse(w, "invalid JSON body")
			return
		}
		if incoming.Port <= 0 || incoming.Port > 65535 {
			incoming.Port = 143
		}
		if err := h.opts.Database.Write(dbTable, "config", incoming); err != nil {
			utils.SendErrorResponse(w, "failed to save configuration")
			return
		}

		// Apply: restart listener with the new settings
		h.stopServer()
		if incoming.Enabled {
			if err := h.startServer(incoming.Port); err != nil {
				h.mu.Lock()
				h.lastErr = err.Error()
				h.mu.Unlock()
				utils.SendErrorResponse(w, "failed to start IMAP server: "+err.Error())
				return
			}
		}
		utils.SendOK(w)

	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) loadServerConfig() ServerConfig {
	cfg := ServerConfig{Enabled: false, Port: 143}
	h.opts.Database.Read(dbTable, "config", &cfg)
	if cfg.Port <= 0 {
		cfg.Port = 143
	}
	return cfg
}
