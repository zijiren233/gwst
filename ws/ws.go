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
	listener              net.Listener
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
	waitListenCloseOnce   sync.Once
	tls                   bool
}

type ServerOption func(*Server)

func WithListener(listener net.Listener) ServerOption {
	return func(ps *Server) {
		ps.listener = listener
	}
}

func WithListenAddr(listenAddr string) ServerOption {
	return func(ps *Server) {
		ps.listenAddr = listenAddr
	}
}

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

func NewServer(path string, wsHandler *Handler, opts ...ServerOption) *Server {
	ps := &Server{
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

func (ps *Server) closeWaitListen() {
	ps.waitListenCloseOnce.Do(func() {
		close(ps.onListened)
	})
}

func (ps *Server) WaitListen() error {
	<-ps.onListened
	return ps.listenErr
}

func (ps *Server) WaitShutdown() {
	<-ps.shutdowned
}

func (ps *Server) Serve() error {
	server := ps.Server()

	defer ps.closeWaitListen()
	defer close(ps.shutdowned)

	if ps.tls {
		return ps.listenAndServeTLS(server)
	}
	return ps.listenAndServe(server)
}

func (ps *Server) listenAndServeTLS(server *http.Server) error {
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

	ln, err := ps.getListener()
	if err != nil {
		ps.listenErr = err
		return err
	}
	defer ln.Close()

	ps.closeWaitListen()

	server.TLSConfig = ps.tlsConfig

	return server.ServeTLS(ln, ps.certFile, ps.keyFile)
}

func (ps *Server) getListener() (net.Listener, error) {
	if ps.listener != nil {
		return ps.listener, nil
	}
	addr := ps.listenAddr
	if addr == "" {
		if ps.tls {
			addr = ":https"
		} else {
			addr = ":http"
		}
	}
	return net.Listen("tcp", addr)
}

func (ps *Server) listenAndServe(server *http.Server) error {
	ln, err := ps.getListener()
	if err != nil {
		ps.listenErr = err
		return err
	}
	defer ln.Close()

	ps.closeWaitListen()

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
	defer ps.closeWaitListen()
	return ps.server.Close()
}

func (ps *Server) Shutdown(ctx context.Context) error {
	defer ps.closeWaitListen()
	defer ps.wsHandler.Wait()
	return ps.server.Shutdown(ctx)
}
