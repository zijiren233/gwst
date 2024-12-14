package ws

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

type Server struct {
	listenErr             error
	shutdowned            chan struct{}
	onListened            chan struct{}
	server                *http.Server
	wsHandler             *Handler
	tlsConfig             *tls.Config
	path                  string
	certFile              string
	keyFile               string
	serverName            string
	listenAddr            string
	selfSignedCertOptions []SelfSignedCertOption
	onListenCloseOnce     sync.Once
	tls                   bool
}

type ServerOption func(*Server)

func WithTLS(certFile, keyFile, serverName string) ServerOption {
	return func(ps *Server) {
		ps.tls = true
		ps.certFile = certFile
		ps.keyFile = keyFile
		WithServerServerName(serverName)(ps)
	}
}

func WithServerServerName(serverName string) ServerOption {
	return func(ps *Server) {
		ps.serverName = serverName
	}
}

func WithTLSConfig(tlsConfig *tls.Config) ServerOption {
	return func(ps *Server) {
		if tlsConfig != nil {
			ps.tls = true
			ps.tlsConfig = tlsConfig
		}
	}
}

func WithSelfSignedCert(opts ...SelfSignedCertOption) ServerOption {
	return func(ps *Server) {
		ps.selfSignedCertOptions = opts
	}
}

func NewServer(listenAddr, path string, wsHandler *Handler, opts ...ServerOption) *Server {
	ps := &Server{
		listenAddr: listenAddr,
		wsHandler:  wsHandler,
		path:       path,
		onListened: make(chan struct{}),
		shutdowned: make(chan struct{}),
	}

	for _, opt := range opts {
		opt(ps)
	}

	return ps
}

func (ps *Server) closeOnListened() {
	ps.onListenCloseOnce.Do(func() {
		close(ps.onListened)
	})
}

func (ps *Server) OnListened() <-chan struct{} {
	return ps.onListened
}

func (ps *Server) ListenErr() error {
	return ps.listenErr
}

func (ps *Server) Shutdowned() <-chan struct{} {
	return ps.shutdowned
}

func (ps *Server) ShutdownedBool() bool {
	select {
	case <-ps.shutdowned:
		return true
	default:
		return false
	}
}

func (ps *Server) Serve() error {
	server := ps.Server()

	defer ps.closeOnListened()
	defer close(ps.shutdowned)

	if ps.tls {
		return ps.listenAndServeTLS(server)
	}
	return ps.listenAndServe(server)
}

func (ps *Server) listenAndServeTLS(server *http.Server) error {
	addr := ps.listenAddr
	if addr == "" {
		addr = ":https"
	}

	if ps.tlsConfig == nil && ps.certFile == "" && ps.keyFile == "" {
		cert, err := GenerateSelfSignedCert(ps.serverName, ps.selfSignedCertOptions...)
		if err != nil {
			return fmt.Errorf("failed to generate self-signed certificate: %w", err)
		}
		ps.tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{*cert},
			MinVersion:   tls.VersionTLS13,
		}
	}

	if ps.tlsConfig.ServerName == "" {
		ps.tlsConfig.ServerName = ps.serverName
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		ps.listenErr = err
		return err
	}
	defer ln.Close()

	ps.closeOnListened()

	server.TLSConfig = ps.tlsConfig

	return server.ServeTLS(ln, ps.certFile, ps.keyFile)
}

func (ps *Server) listenAndServe(server *http.Server) error {
	addr := ps.listenAddr
	if addr == "" {
		addr = ":http"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		ps.listenErr = err
		return err
	}
	defer ln.Close()

	ps.closeOnListened()

	return server.Serve(ln)
}

func (ps *Server) Server() *http.Server {
	if ps.server == nil {
		mux := http.NewServeMux()
		mux.Handle(ps.path, ps.wsHandler)
		ps.server = &http.Server{
			Addr:              ps.listenAddr,
			Handler:           mux,
			ReadHeaderTimeout: time.Second * 5,
			MaxHeaderBytes:    16 * 1024,
		}
		ps.server.RegisterOnShutdown(func() {
			ps.wsHandler.Close()
		})
	}
	return ps.server
}

func (ps *Server) Close() error {
	defer ps.closeOnListened()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return ps.server.Shutdown(ctx)
}

func (ps *Server) Shutdown(ctx context.Context) error {
	defer ps.closeOnListened()
	defer ps.wsHandler.Wait()
	return ps.server.Shutdown(ctx)
}
