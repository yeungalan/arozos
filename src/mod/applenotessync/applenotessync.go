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

	TLS: a self-signed certificate is generated on first start and stored
	in the database so it remains stable across restarts. Apple devices
	connect with "Use SSL: On" and accept the certificate warning once.
*/

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

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
	Enabled    bool `json:"enabled"`
	Port       int  `json:"port"`
	TLSEnabled bool `json:"tlsEnabled"`
}

// Handler owns the IMAP server lifecycle and the HTTP management endpoints.
type Handler struct {
	opts Options

	mu        sync.Mutex
	server    *server.Server
	listener  net.Listener
	running   bool
	lastErr   string
	tlsConfig *tls.Config // nil until loadOrGenerateTLS succeeds

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

	tlsCfg, err := h.loadOrGenerateTLS()
	if err != nil {
		opts.Logger.PrintAndLog("AppleNotesSync", "TLS init failed (falling back to plaintext): "+err.Error(), err)
	} else {
		h.tlsConfig = tlsCfg
	}

	cfg := h.loadServerConfig()
	if cfg.Enabled {
		if err := h.startServer(cfg); err != nil {
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

// ── TLS certificate ───────────────────────────────────────────────────────────

func (h *Handler) loadOrGenerateTLS() (*tls.Config, error) {
	var certPEM, keyPEM string
	h.opts.Database.Read(dbTable, "tls:cert", &certPEM)
	h.opts.Database.Read(dbTable, "tls:key", &keyPEM)

	if certPEM == "" || keyPEM == "" {
		h.opts.Logger.PrintAndLog("AppleNotesSync", "Generating self-signed TLS certificate…", nil)
		var err error
		certPEM, keyPEM, err = generateSelfSignedCert()
		if err != nil {
			return nil, err
		}
		h.opts.Database.Write(dbTable, "tls:cert", certPEM)
		h.opts.Database.Write(dbTable, "tls:key", keyPEM)
		h.opts.Logger.PrintAndLog("AppleNotesSync", "Self-signed TLS certificate generated and stored", nil)
	}

	tlsCert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("loading TLS key pair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// generateSelfSignedCert returns PEM-encoded certificate and private key.
// The cert is valid for 20 years and lists common LAN hostnames as SANs.
func generateSelfSignedCert() (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"arozos Notes IMAP"}},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().Add(20 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "arozos.local"},
		IPAddresses: []net.IP{
			net.ParseIP("127.0.0.1"),
			net.ParseIP("::1"),
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}

	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM, nil
}

// ── Debug logging helpers ─────────────────────────────────────────────────────

// imapDebugWriter forwards raw IMAP traffic to the arozos logger.
type imapDebugWriter struct {
	log *logger.Logger
}

func (w *imapDebugWriter) Write(p []byte) (n int, err error) {
	line := strings.TrimRight(string(p), "\r\n")
	if line != "" {
		w.log.PrintAndLog("AppleNotesSync/IMAP", line, nil)
	}
	return len(p), nil
}

// imapErrorLogger wraps the arozos logger for go-imap's ErrorLog interface.
type imapErrorLogger struct {
	log *logger.Logger
}

func (l *imapErrorLogger) Printf(format string, v ...interface{}) {
	l.log.PrintAndLog("AppleNotesSync", fmt.Sprintf(format, v...), nil)
}

func (l *imapErrorLogger) Println(v ...interface{}) {
	l.log.PrintAndLog("AppleNotesSync", fmt.Sprint(v...), nil)
}

// loggingListener wraps net.Listener and logs each new TCP connection.
type loggingListener struct {
	net.Listener
	log *logger.Logger
}

func (l *loggingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	addr := conn.RemoteAddr().String()
	l.log.PrintAndLog("AppleNotesSync", fmt.Sprintf("TCP connect: %s", addr), nil)
	return &loggingConn{Conn: conn, log: l.log, addr: addr}, nil
}

// loggingConn logs when a TCP connection is closed.
type loggingConn struct {
	net.Conn
	log  *logger.Logger
	addr string
}

func (c *loggingConn) Close() error {
	err := c.Conn.Close()
	c.log.PrintAndLog("AppleNotesSync", fmt.Sprintf("TCP disconnect: %s", c.addr), nil)
	return err
}

// ── Server lifecycle ──────────────────────────────────────────────────────────

func (h *Handler) startServer(cfg ServerConfig) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.running {
		return nil
	}

	port := cfg.Port
	useTLS := cfg.TLSEnabled && h.tlsConfig != nil

	if port <= 0 {
		if useTLS {
			port = 993
		} else {
			port = 143
		}
	}

	addr := fmt.Sprintf(":%d", port)

	var rawL net.Listener
	var err error
	if useTLS {
		rawL, err = tls.Listen("tcp", addr, h.tlsConfig)
		h.opts.Logger.PrintAndLog("AppleNotesSync", fmt.Sprintf("Starting IMAP notes server with TLS on port %d", port), nil)
	} else {
		rawL, err = net.Listen("tcp", addr)
		h.opts.Logger.PrintAndLog("AppleNotesSync", fmt.Sprintf("Starting IMAP notes server (plaintext) on port %d", port), nil)
	}
	if err != nil {
		return err
	}
	l := &loggingListener{Listener: rawL, log: h.opts.Logger}

	be := &imapBackend{handler: h}
	s := server.New(be)
	s.Addr = addr
	s.AllowInsecureAuth = true // safe: connection is either TLS or a trusted LAN
	s.Debug = &imapDebugWriter{log: h.opts.Logger}
	s.ErrorLog = &imapErrorLogger{log: h.opts.Logger}

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

	proto := "plaintext"
	if useTLS {
		proto = "TLS"
	}
	h.opts.Logger.PrintAndLog("AppleNotesSync", fmt.Sprintf("IMAP notes server started on port %d (%s)", port, proto), nil)
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
		"enabled":    cfg.Enabled,
		"running":    running,
		"port":       cfg.Port,
		"tlsEnabled": cfg.TLSEnabled,
		"lastError":  lastErr,
		"username":   userinfo.Username,
		"isAdmin":    userinfo.IsAdmin(),
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
			if incoming.TLSEnabled {
				incoming.Port = 993
			} else {
				incoming.Port = 143
			}
		}
		if err := h.opts.Database.Write(dbTable, "config", incoming); err != nil {
			utils.SendErrorResponse(w, "failed to save configuration")
			return
		}

		// Apply: restart listener with the new settings
		h.stopServer()
		if incoming.Enabled {
			if err := h.startServer(incoming); err != nil {
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
	cfg := ServerConfig{Enabled: false, Port: 993, TLSEnabled: true}
	h.opts.Database.Read(dbTable, "config", &cfg)
	if cfg.Port <= 0 {
		if cfg.TLSEnabled {
			cfg.Port = 993
		} else {
			cfg.Port = 143
		}
	}
	return cfg
}
