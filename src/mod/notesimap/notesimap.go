package notesimap

/*
	notesimap.go - lifecycle manager for the Notes IMAP sync server.

	Exposes each user's Notes web-app data as a "Notes" IMAP mailbox so Apple
	Notes can be added as a plain IMAP account (Settings > Notes > Accounts >
	Add Account > Other, on iPhone/iPad/Mac) and sync bidirectionally - the
	same mechanism Apple Notes uses today for Gmail/Fastmail/other non-iCloud
	IMAP accounts. No Apple ID or iCloud credentials are ever involved.
*/

import (
	"crypto/tls"
	"fmt"
	"net"
	"sync"

	"github.com/emersion/go-imap/server"
	"imuslab.com/arozos/mod/auth"
	"imuslab.com/arozos/mod/database"
	"imuslab.com/arozos/mod/info/logger"
	"imuslab.com/arozos/mod/user"
)

const dbTable = "notesimap"
const defaultPort = 1143

// ManagerOption holds the dependencies required to run the Notes IMAP server.
type ManagerOption struct {
	AuthAgent   *auth.AuthAgent
	UserHandler *user.UserHandler
	Sysdb       *database.Database
	Logger      *logger.Logger
	// TLSCertFile / TLSKeyFile, if both load successfully, enable STARTTLS
	// and require it before LOGIN. If unset or unloadable, the server falls
	// back to plaintext IMAP (fine for trusted LAN use, not for the Internet).
	TLSCertFile string
	TLSKeyFile  string
}

// Manager starts, stops and configures the Notes IMAP sync server, persisting
// its enabled/port state in the system database so it survives a restart.
type Manager struct {
	option ManagerOption
	mu     sync.Mutex
	server *server.Server
}

// NewManager creates the manager and, if previously enabled, restarts the
// server automatically.
func NewManager(option ManagerOption) *Manager {
	option.Sysdb.NewTable(dbTable)

	m := &Manager{option: option}

	enabled := false
	if option.Sysdb.KeyExists(dbTable, "enabled") {
		option.Sysdb.Read(dbTable, "enabled", &enabled)
	} else {
		option.Sysdb.Write(dbTable, "enabled", false)
	}

	if enabled {
		if err := m.Start(); err != nil {
			option.Logger.PrintAndLog("NotesSync", "failed to auto-start Notes IMAP sync server", err)
		}
	}
	return m
}

// GetPort returns the configured listening port (default 1143 if unset).
func (m *Manager) GetPort() int {
	port := defaultPort
	if m.option.Sysdb.KeyExists(dbTable, "port") {
		m.option.Sysdb.Read(dbTable, "port", &port)
	}
	return port
}

// SetPort persists a new listening port, restarting the server on it if it
// is currently running.
func (m *Manager) SetPort(port int) error {
	m.option.Sysdb.Write(dbTable, "port", port)
	if m.IsRunning() {
		if err := m.Stop(); err != nil {
			return err
		}
		return m.Start()
	}
	return nil
}

// IsRunning reports whether the IMAP listener is currently active.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.server != nil
}

// UsesTLS reports whether the running server requires STARTTLS before LOGIN.
func (m *Manager) UsesTLS() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.server != nil && m.server.TLSConfig != nil
}

// Start binds the configured port and begins serving IMAP connections.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.server != nil {
		return nil
	}

	be := newBackend(m.option.AuthAgent, m.option.UserHandler)
	s := server.New(be)
	s.Addr = fmt.Sprintf(":%d", m.GetPort())
	s.AllowInsecureAuth = true

	if cert, err := tls.LoadX509KeyPair(m.option.TLSCertFile, m.option.TLSKeyFile); err == nil {
		s.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		s.AllowInsecureAuth = false
	}

	listener, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}

	m.server = s
	go func() {
		//nolint - Serve returns once Close() closes the listener; that is a
		// normal shutdown, not an error worth logging.
		s.Serve(listener)
	}()

	m.option.Sysdb.Write(dbTable, "enabled", true)
	m.option.Logger.PrintAndLog("NotesSync", "Notes IMAP sync server started on "+s.Addr, nil)
	return nil
}

// Stop closes the listener and all active connections.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.option.Sysdb.Write(dbTable, "enabled", false)
	if m.server == nil {
		return nil
	}
	err := m.server.Close()
	m.server = nil
	m.option.Logger.PrintAndLog("NotesSync", "Notes IMAP sync server stopped", nil)
	return err
}
