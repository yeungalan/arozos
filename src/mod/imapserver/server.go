package imapserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	imapserver "github.com/emersion/go-imap/server"
)

// Config holds the IMAP server configuration.
type Config struct {
	Enabled  bool   `json:"enabled"`
	Port     int    `json:"port"`     // plain/STARTTLS port (default 143)
	TLSPort  int    `json:"tlsPort"`  // SSL port (default 993)
	UseTLS   bool   `json:"useTLS"`   // enable SSL on TLSPort
	CertFile string `json:"certFile"` // PEM cert path (leave empty for self-signed)
	KeyFile  string `json:"keyFile"`  // PEM key path
}

// DefaultConfig returns a Config with safe defaults.
func DefaultConfig() Config {
	return Config{
		Enabled: false,
		Port:    1143, // unprivileged alternative to 143
		TLSPort: 1993, // unprivileged alternative to 993
		UseTLS:  false,
	}
}

// Server wraps a go-imap server and tracks its listeners for clean shutdown.
type Server struct {
	plain  net.Listener
	secure net.Listener
}

// Start creates and starts the IMAP server(s). Returns a *Server that can be
// stopped with Close.
func Start(cfg Config, auth AuthFunc, nd NotesDirFunc, certDir string) (*Server, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	be := NewBackend(auth, nd)
	srv := &Server{}

	// ── STARTTLS / plain listener ────────────────────────────────────────
	plainSrv := imapserver.New(be)
	plainSrv.AllowInsecureAuth = true // allow LOGIN without TLS on LAN

	addr := fmt.Sprintf(":%d", cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("imap listen %s: %w", addr, err)
	}
	srv.plain = ln
	go plainSrv.Serve(ln) //nolint

	// ── SSL listener ─────────────────────────────────────────────────────
	if cfg.UseTLS {
		tlsCfg, err := loadOrGenTLS(cfg, certDir)
		if err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("imap tls config: %w", err)
		}

		tlsSrv := imapserver.New(be)
		tlsSrv.AllowInsecureAuth = true

		tlsAddr := fmt.Sprintf(":%d", cfg.TLSPort)
		tlsLn, err := tls.Listen("tcp", tlsAddr, tlsCfg)
		if err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("imap tls listen %s: %w", tlsAddr, err)
		}
		srv.secure = tlsLn
		go tlsSrv.Serve(tlsLn) //nolint
	}

	return srv, nil
}

// Close shuts down both listeners.
func (s *Server) Close() {
	if s == nil {
		return
	}
	if s.plain != nil {
		_ = s.plain.Close()
	}
	if s.secure != nil {
		_ = s.secure.Close()
	}
}

// ── TLS helpers ───────────────────────────────────────────────────────────────

func loadOrGenTLS(cfg Config, certDir string) (*tls.Config, error) {
	certFile := cfg.CertFile
	keyFile := cfg.KeyFile

	if certFile == "" || keyFile == "" {
		certFile = certDir + "/imap_cert.pem"
		keyFile = certDir + "/imap_key.pem"
	}

	// Generate a self-signed cert if one doesn't exist yet.
	if _, err := os.Stat(certFile); os.IsNotExist(err) {
		if err := generateSelfSigned(certFile, keyFile); err != nil {
			return nil, fmt.Errorf("self-signed cert: %w", err)
		}
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

func generateSelfSigned(certFile, keyFile string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "arozos-imap"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost", "arozos.local"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}

	certOut, err := os.Create(certFile)
	if err != nil {
		return err
	}
	defer certOut.Close()
	_ = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer keyOut.Close()
	return pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
