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
	"imuslab.com/arozos/mod/user"
)

// Server is the IMAP Notes server instance.
type Server struct {
	Port        int
	Running     bool
	userHandler *user.UserHandler
	authAgent   *auth.AuthAgent
	database    *database.Database
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
}

// NewServer creates a new IMAP Notes server (not yet started).
func NewServer(cfg Config) *Server {
	return &Server{
		Port:        cfg.Port,
		userHandler: cfg.UserHandler,
		authAgent:   cfg.AuthAgent,
		database:    cfg.Database,
		done:        make(chan struct{}),
	}
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
	imapLogger.PrintAndLog("IMAPNotes", fmt.Sprintf("IMAP Notes server listening on port %d", s.Port), nil)
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
	imapLogger.PrintAndLog("IMAPNotes", "IMAP Notes server stopped", nil)
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				imapLogger.PrintAndLog("IMAPNotes", "Accept error: "+err.Error(), err)
				continue
			}
		}
		go newConnHandler(conn, s.authAgent, s.userHandler, s.database).run()
	}
}
