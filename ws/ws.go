package ws

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/fatih/color"
	"golang.org/x/net/websocket"
)

type Server struct {
	listenAddr     string
	targetAddr     string
	allowedTargets map[string]struct{}
	namedTargets   map[string]string
	server         *http.Server
	stopCleanup    chan struct{}
	path           string
	tls            bool
	serverName     string
	certFile       string
	keyFile        string
	GetCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	bufferSize     int
	bufferPool     sync.Pool
}

type WsServerOption func(*Server)

func WithTLS(certFile, keyFile, serverName string) WsServerOption {
	return func(ps *Server) {
		ps.tls = true
		ps.certFile = certFile
		ps.keyFile = keyFile
		ps.serverName = serverName
	}
}

func WithAllowedTargets(allowedTargets []string) WsServerOption {
	return func(ps *Server) {
		if len(allowedTargets) == 0 {
			return
		}
		ps.allowedTargets = make(map[string]struct{})
		for _, target := range allowedTargets {
			ps.allowedTargets[target] = struct{}{}
		}
	}
}

func WithNamedTargets(namedTargets map[string]string) WsServerOption {
	return func(ps *Server) {
		ps.namedTargets = namedTargets
	}
}

func WithGetCertificate(getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error)) WsServerOption {
	return func(ps *Server) {
		ps.tls = true
		ps.GetCertificate = getCertificate
	}
}

func WithServerBufferSize(size int) WsServerOption {
	return func(ps *Server) {
		ps.bufferSize = size
	}
}

func NewServer(listenAddr, targetAddr, path string, opts ...WsServerOption) *Server {
	ps := &Server{
		listenAddr:  listenAddr,
		targetAddr:  targetAddr,
		stopCleanup: make(chan struct{}),
		path:        path,
	}
	for _, opt := range opts {
		opt(ps)
	}
	if ps.bufferSize == 0 {
		ps.bufferSize = DefaultBufferSize
	}
	ps.bufferPool = sync.Pool{
		New: func() interface{} {
			buffer := make([]byte, ps.bufferSize)
			return &buffer
		},
	}
	return ps
}

func (ps *Server) getBuffer() *[]byte {
	return ps.bufferPool.Get().(*[]byte)
}

func (ps *Server) putBuffer(buffer *[]byte) {
	ps.bufferPool.Put(buffer)
}

func (ps *Server) Serve(opts ...selfSignedCertOption) error {
	mux := http.NewServeMux()
	mux.Handle(ps.path, websocket.Handler(ps.handleWebSocket))
	ps.server = &http.Server{Addr: ps.listenAddr, Handler: mux}
	if ps.tls {
		if ps.GetCertificate != nil {
			ps.server.TLSConfig = &tls.Config{
				GetCertificate: ps.GetCertificate,
			}
		} else if ps.certFile == "" && ps.keyFile == "" {
			cert, err := GenerateSelfSignedCert(ps.serverName, opts...)
			if err != nil {
				return fmt.Errorf("failed to generate self-signed certificate: %v", err)
			}
			ps.server.TLSConfig = &tls.Config{
				Certificates: []tls.Certificate{cert},
				ServerName:   ps.serverName,
			}
		}
		return ps.server.ListenAndServeTLS(ps.certFile, ps.keyFile)
	}
	return ps.server.ListenAndServe()
}

func (ps *Server) Close() error {
	if ps.server != nil {
		close(ps.stopCleanup)
		return ps.server.Close()
	}
	return fmt.Errorf("server not started")
}

func (ps *Server) handleWebSocket(ws *websocket.Conn) {
	defer ws.Close()

	ws.PayloadType = websocket.BinaryFrame

	protocol := ps.getProtocol(ws.Request().Header.Get("X-Protocol"))
	target, err := ps.getTarget(
		ws.Request().Header.Get("X-Target"),
		ws.Request().Header.Get("X-Named-Target"),
	)
	if err != nil {
		color.Red("Error getting target: %v\n", err)
		return
	}

	color.Green("Received WebSocket connection: %v\nTarget: %s\nProtocol: %s\n", ws.RemoteAddr(), target, protocol)

	ps.handle(ws, protocol, target)
}

func (ps *Server) getProtocol(requestProtocol string) string {
	switch requestProtocol {
	case "udp":
		return "udp"
	default:
		return "tcp"
	}
}

func (ps *Server) getTarget(requestTarget string, namedTarget string) (string, error) {
	if namedTarget != "" {
		if target, ok := ps.namedTargets[namedTarget]; ok {
			return target, nil
		}
	}
	if requestTarget == "" || requestTarget == ps.targetAddr || len(ps.allowedTargets) == 0 {
		return ps.targetAddr, nil
	}

	if _, ok := ps.allowedTargets[requestTarget]; !ok {
		return "", fmt.Errorf("target %s not allowed", requestTarget)
	}

	return requestTarget, nil
}

func (ps *Server) handle(ws *websocket.Conn, network string, target string) {
	if target == "" {
		return
	}

	conn, err := net.Dial(network, target)
	if err != nil {
		color.Red("Failed to connect to target: %v\n", err)
		return
	}
	defer conn.Close()

	go func() {
		buffer := ps.getBuffer()
		defer ps.putBuffer(buffer)
		_, err := io.CopyBuffer(conn, ws, *buffer)
		if err != nil && err != io.EOF {
			color.Yellow("Failed to copy data to target: %v\n", err)
		}
	}()

	buffer := ps.getBuffer()
	defer ps.putBuffer(buffer)
	_, err = io.CopyBuffer(ws, conn, *buffer)
	if err != nil && err != io.EOF {
		color.Yellow("Failed to copy data to WebSocket: %v\n", err)
	}
}
