package ws

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"

	"golang.org/x/net/websocket"
)

type WsServer struct {
	listenAddr     string
	targetAddr     string
	server         *http.Server
	stopCleanup    chan struct{}
	path           string
	host           string
	tls            bool
	certFile       string
	keyFile        string
	GetCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
}

type WsServerOption func(*WsServer)

func WithTLS(certFile, keyFile string) WsServerOption {
	return func(ps *WsServer) {
		ps.tls = true
		ps.certFile = certFile
		ps.keyFile = keyFile
	}
}

func WithGetCertificate(getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)) WsServerOption {
	return func(ps *WsServer) {
		ps.tls = true
		ps.GetCertificate = getCertificate
	}
}

func NewWsServer(listenAddr, targetAddr, host, path string, opts ...WsServerOption) *WsServer {
	ps := &WsServer{
		listenAddr:  listenAddr,
		targetAddr:  targetAddr,
		stopCleanup: make(chan struct{}),
		path:        path,
		host:        host,
	}
	for _, opt := range opts {
		opt(ps)
	}
	return ps
}

func (ps *WsServer) Serve(opts ...selfSignedCertOption) error {
	mux := http.NewServeMux()
	mux.Handle(ps.path, websocket.Handler(ps.handleWebSocket))
	ps.server = &http.Server{Addr: ps.listenAddr, Handler: mux}
	if ps.tls {
		if ps.GetCertificate != nil {
			ps.server.TLSConfig = &tls.Config{
				GetCertificate: ps.GetCertificate,
			}
		} else if ps.certFile == "" && ps.keyFile == "" {
			cert, err := GenerateSelfSignedCert(ps.host, opts...)
			if err != nil {
				return fmt.Errorf("failed to generate self-signed certificate: %v", err)
			}
			ps.server.TLSConfig = &tls.Config{
				Certificates: []tls.Certificate{cert},
			}
		}
		return ps.server.ListenAndServeTLS(ps.certFile, ps.keyFile)
	}
	return ps.server.ListenAndServe()
}

func (ps *WsServer) Close() error {
	if ps.server != nil {
		close(ps.stopCleanup)
		return ps.server.Close()
	}
	return fmt.Errorf("server not started")
}

func (ps *WsServer) handleWebSocket(ws *websocket.Conn) {
	defer ws.Close()

	ws.PayloadType = websocket.BinaryFrame

	protocol := ws.Request().Header.Get("X-Protocol")

	if protocol == "udp" {
		ps.handle(ws, "udp")
	} else {
		ps.handle(ws, "tcp")
	}
}

func (ps *WsServer) handle(ws *websocket.Conn, network string) {
	conn, err := net.Dial(network, ps.targetAddr)
	if err != nil {
		fmt.Printf("Failed to connect to target: %v\n", err)
		return
	}
	defer conn.Close()

	go io.Copy(conn, ws)
	io.Copy(ws, conn)
}
