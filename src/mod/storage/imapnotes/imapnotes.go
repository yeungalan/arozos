/*
Package imapnotes provides a minimal IMAP4rev1 server that bridges arozos Notes
with Apple's iPhone Notes app via IMAP-based sync.

Authentication uses the arozos username as the IMAP login name and an arozos
auto-login token as the password.  Only a single virtual mailbox ("Notes") is
exposed per user.

The implementation intentionally omits features like TLS negotiation, IDLE, and
advanced search operators that are not required for Apple Notes sync.  It is
NOT a general-purpose mail server and deliberately does not relay email.
*/
package imapnotes

import (
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
	Running     bool
	userHandler *user.UserHandler
	authAgent   *auth.AuthAgent
	database    *database.Database
	sysLog      *logger.Logger // system-wide logger (writes to log file + stdout)
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
}

// NewServer creates a new IMAP Notes server (not yet started).
func NewServer(cfg Config) *Server {
	lg := cfg.Logger
	if lg == nil {
		lg = imapLogger // fall back to stdout-only tmp logger
	}
	return &Server{
		Port:        cfg.Port,
		userHandler: cfg.UserHandler,
		authAgent:   cfg.AuthAgent,
		database:    cfg.Database,
		sysLog:      lg,
		done:        make(chan struct{}),
	}
}

// log writes to the system-wide logger (file + stdout) via sysLog.
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

	s.listener = ln
	s.done = make(chan struct{})
	s.Running = true

	go s.acceptLoop()
	s.log(fmt.Sprintf("IMAP Notes server listening on port %d", s.Port), nil)
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
