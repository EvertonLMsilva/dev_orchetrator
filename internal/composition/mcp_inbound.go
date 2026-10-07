package composition

import (
	"context"
	"crypto/rand"
	"dev-orchestrator/internal/adapters/localsecurity"
	inbound "dev-orchestrator/internal/adapters/mcp"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type MCPInboundConfig struct {
	Listen      string
	Concurrency int
	Security    MCPReadConfig
}

func validateMCP(c Config) error {
	m := c.MCP
	if m == nil || os.Getenv("MCPGODEBUG") != "" || c.RequestTimeout > Duration(120*time.Second) || c.ShutdownTimeout > Duration(120*time.Second) || m.Concurrency < 1 || m.Concurrency > 16 {
		return ErrConfig
	}
	host, port, err := net.SplitHostPort(m.Listen)
	number, e := strconv.Atoi(port)
	if err != nil || e != nil || number < 0 || number > 65535 || (host != "127.0.0.1" && host != "::1" && host != "0.0.0.0") {
		return ErrConfig
	}
	s := m.Security
	if filepath.Dir(s.AuditFile) != c.StateDir || s.AuditFile == filepath.Join(c.StateDir, "audit.json") || s.AuditFile == filepath.Join(c.StateDir, "tasks.json") || filepath.Ext(s.AuditFile) != ".jsonl" {
		return ErrConfig
	}
	for _, file := range []string{s.AuthenticationFile, s.GrantsFile} {
		if !filepath.IsAbs(file) || contains(c.StateDir, file) || contains("/var/lib/dev-orchestrator/runtime-auth", file) {
			return ErrConfig
		}
		if _, err := canonicalDir(filepath.Dir(file)); err != nil {
			return ErrConfig
		}
		if privateSecurityFile(file) != nil {
			return ErrConfig
		}
		for _, project := range c.Projects {
			if contains(project.Workspace, file) || contains(filepath.Dir(file), project.Workspace) {
				return ErrConfig
			}
		}
	}
	if localsecurity.Validate(s.AuthenticationFile, s.GrantsFile) != nil {
		return ErrConfig
	}
	return nil
}

func (s *Service) composeMCP(isolation func(string) error) error {
	config := s.config.MCP
	for _, path := range []string{config.Security.AuthenticationFile, config.Security.GrantsFile} {
		if isolation(path) != nil {
			return ErrConfig
		}
	}
	if info, err := os.Lstat(config.Security.AuditFile); err == nil {
		if privateSecurityFile(config.Security.AuditFile) != nil || !info.Mode().IsRegular() {
			return ErrConfig
		}
	} else if !os.IsNotExist(err) {
		return ErrConfig
	}
	audit := localsecurity.NewAudit(config.Security.AuditFile)
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ErrConfig
	}
	// Use the MCP-4 audit writer itself to prove durable storage before READY.
	if audit.Append(s.ctx, ports.ReadAuditEvent{AttemptID: hex.EncodeToString(id[:]), Operation: "UNKNOWN", Timestamp: time.Now().UTC(), Outcome: "RECEIVED", CorrelationID: "runtime-startup"}) != nil {
		return ErrStorage
	}
	runtime := application.NewSecureReadRuntime(localsecurity.NewAuthentication(config.Security.AuthenticationFile), localsecurity.NewGrants(config.Security.GrantsFile), audit, s.projects, s.tasks, s.localAgent)
	adapter, err := inbound.New(s.ctx, runtime, time.Duration(s.config.RequestTimeout), config.Concurrency)
	if err != nil {
		return ErrConfig
	}
	s.mcp = adapter
	return nil
}

// StartMCP starts one inbound listener on this Service's existing lifecycle.
func (s *Service) StartMCP() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mcp == nil || s.mcpServer != nil || s.stopped || s.ctx.Err() != nil {
		return ErrConfig
	}
	listener, err := net.Listen("tcp", s.config.MCP.Listen)
	if err != nil {
		return errors.New("MCP listener unavailable")
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", s.mcp)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		w.Write([]byte("LIVE\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !s.MCPReady() {
			w.WriteHeader(503)
			w.Write([]byte("NOT_READY\n"))
			return
		}
		w.Write([]byte("READY\n"))
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: time.Duration(s.config.RequestTimeout), WriteTimeout: time.Duration(s.config.RequestTimeout) + 15*time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return s.ctx }}
	s.mcpServer = server
	s.mcpAddress = "http://" + listener.Addr().String()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.failClosed()
		}
	}()
	return nil
}
func (s *Service) MCPReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped && s.mcpServer != nil && s.mcp != nil && s.mcp.Ready()
}
func (s *Service) MCPAddress() string { s.mu.Lock(); defer s.mu.Unlock(); return s.mcpAddress }
