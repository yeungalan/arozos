/*
Package imapnotes provides a minimal IMAP4rev1 server that bridges arozos Notes
with Apple's iPhone Notes app via IMAP-based sync.

Authentication uses the arozos username as the IMAP login name and an arozos
auto-login token as the password.  Only a single virtual mailbox ("Notes") is
exposed per user.

When TLSCertFile and TLSKeyFile are both set the server wraps the listener with
TLS (IMAPS), allowing iPhones to connect with SSL enabled.  Without TLS the
server is plain IMAP and the iPhone must have SSL disabled in account settings.
*/
package imapnotes

import (
	"crypto/tls"
	"fmt"
	"net"
	"sync"

	"imuslab.com/arozos/mod/auth"
	"imuslab.com/arozos/mod/database"
	"imuslab.com/arozos/mod/info/logger"
	"imuslab.com/arozos/mod/user"
)

// Server is the IMAP Notes server instance.
type Server struct {
	Port        int
	TLSEnabled  bool
	Running     bool
	userHandler *user.UserHandler
	authAgent   *auth.AuthAgent
	database    *database.Database
	sysLog      *logger.Logger
	tlsConfig   *tls.Config // nil when TLS is not configured
	listener    net.Listener
	done        chan struct{}
	mu          sync.Mutex
}

// Config holds all dependencies needed to create an IMAP Notes server.
type Config struct {
	Port        int
	UserHandler *user.UserHandler
	AuthAgent   *auth.AuthAgent
	Database    *database.Database
	// Logger is optional; when provided (e.g. systemWideLogger from main) all
	// important events are written to the arozos log file in addition to stdout.
	Logger *logger.Logger
	// TLSCertFile and TLSKeyFile are optional.  When both are set the listener is
	// wrapped with TLS so iPhones can connect with SSL enabled (IMAPS).
	TLSCertFile string
	TLSKeyFile  string
}

// NewServer creates a new IMAP Notes server (not yet started).
func NewServer(cfg Config) *Server {
	lg := cfg.Logger
	if lg == nil {
		lg = imapLogger
	}

	srv := &Server{
		Port:        cfg.Port,
		userHandler: cfg.UserHandler,
		authAgent:   cfg.AuthAgent,
		database:    cfg.Database,
		sysLog:      lg,
		done:        make(chan struct{}),
	}

	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			lg.PrintAndLog("IMAPNotes", "TLS cert load failed, falling back to plain IMAP: "+err.Error(), err)
		} else {
			srv.tlsConfig = &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			}
			srv.TLSEnabled = true
		}
	}

	return srv
}

// log writes to the system-wide logger (file + stdout).
func (s *Server) log(msg string, err error) {
	s.sysLog.PrintAndLog("IMAPNotes", msg, err)
}

// Start begins listening for IMAP connections.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Running {
		return fmt.Errorf("IMAP Notes server already running")
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.Port))
	if err != nil {
		return fmt.Errorf("IMAP Notes listen on port %d: %w", s.Port, err)
	}

	if s.tlsConfig != nil {
		ln = tls.NewListener(ln, s.tlsConfig)
		s.log(fmt.Sprintf("IMAP Notes server (TLS/IMAPS) listening on port %d", s.Port), nil)
	} else {
		s.log(fmt.Sprintf("IMAP Notes server (plain IMAP) listening on port %d", s.Port), nil)
	}

	s.listener = ln
	s.done = make(chan struct{})
	s.Running = true

	go s.acceptLoop()
	return nil
}

// Stop shuts the server down gracefully.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.Running {
		return
	}
	s.Running = false
	close(s.done)
	if s.listener != nil {
		s.listener.Close()
	}
	s.log("IMAP Notes server stopped", nil)
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				s.log("Accept error: "+err.Error(), err)
				continue
			}
		}
		s.log(fmt.Sprintf("New connection from %s", conn.RemoteAddr()), nil)
		go newConnHandler(conn, s.authAgent, s.userHandler, s.database, s.sysLog).run()
	}
}
