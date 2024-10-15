package ws

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/fatih/color"
	"golang.org/x/net/websocket"
)

type Server struct {
	listenAddr            string
	targetAddr            string
	allowedTargets        map[string]struct{}
	namedTargets          map[string]string
	server                *http.Server
	path                  string
	tls                   bool
	serverName            string
	certFile              string
	keyFile               string
	selfSignedCertOptions []selfSignedCertOption
	onListened            chan struct{}
	shutdowned            chan struct{}
	listenErr             error
	onListenCloseOnce     sync.Once
	GetCertificate        func(*tls.ClientHelloInfo) (*tls.Certificate, error)
	bufferSize            int
	bufferPool            *sync.Pool
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

func WithSelfSignedCert(opts ...selfSignedCertOption) WsServerOption {
	return func(ps *Server) {
		ps.selfSignedCertOptions = opts
	}
}

func NewServer(listenAddr, targetAddr, path string, opts ...WsServerOption) *Server {
	ps := &Server{
		listenAddr: listenAddr,
		targetAddr: targetAddr,
		path:       path,
		onListened: make(chan struct{}),
		shutdowned: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(ps)
	}
	if ps.bufferSize == 0 {
		ps.bufferSize = DefaultBufferSize
	}
	ps.bufferPool = newBufferPool(ps.bufferSize)
	mux := http.NewServeMux()
	mux.Handle(ps.path, websocket.Handler(ps.handleWebSocket))
	ps.server = &http.Server{Addr: ps.listenAddr, Handler: mux}
	return ps
}

func (ps *Server) getBuffer() *[]byte {
	return ps.bufferPool.Get().(*[]byte)
}

func (ps *Server) putBuffer(buffer *[]byte) {
	ps.bufferPool.Put(buffer)
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
	defer ps.closeOnListened()
	defer close(ps.shutdowned)

	if ps.tls {
		if ps.GetCertificate != nil {
			ps.server.TLSConfig = &tls.Config{
				GetCertificate: ps.GetCertificate,
			}
		} else if ps.certFile == "" && ps.keyFile == "" {
			cert, err := GenerateSelfSignedCert(ps.serverName, ps.selfSignedCertOptions...)
			if err != nil {
				return fmt.Errorf("failed to generate self-signed certificate: %v", err)
			}
			ps.server.TLSConfig = &tls.Config{
				Certificates: []tls.Certificate{cert},
				ServerName:   ps.serverName,
			}
		}
		return ps.listenAndServeTLS(ps.certFile, ps.keyFile)
	}
	return ps.listenAndServe()
}

func (ps *Server) listenAndServeTLS(certFile, keyFile string) error {
	addr := ps.listenAddr
	if addr == "" {
		addr = ":https"
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		ps.listenErr = err
		return err
	}

	ps.closeOnListened()

	defer ln.Close()

	return ps.server.ServeTLS(ln, certFile, keyFile)
}

func (ps *Server) listenAndServe() error {
	addr := ps.listenAddr
	if addr == "" {
		addr = ":http"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		ps.listenErr = err
		return err
	}

	ps.closeOnListened()

	return ps.server.Serve(ln)
}

func (ps *Server) Close() error {
	ps.closeOnListened()
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return ps.server.Shutdown(timeoutCtx)
}

func (ps *Server) handleWebSocket(ws *websocket.Conn) {
	defer ws.Close()

	ws.PayloadType = websocket.BinaryFrame

	protocol := getProtocol(ws.Request().Header.Get("X-Protocol"))
	target, err := ps.getTarget(
		ws.Request().Header.Get("X-Target"),
		ws.Request().Header.Get("X-Named-Target"),
	)
	if err != nil {
		color.Red("Error getting target: %v\n", err)
		return
	}

	color.Green("Received WebSocket connection: %v\n\tTarget: %s\n\tProtocol: %s\n", ws.RemoteAddr(), target, protocol)

	ps.handle(ws, protocol, target)
}

func getProtocol(requestProtocol string) string {
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
		_, err = CopyBufferWithWriteTimeout(ws, conn, *buffer, DefaultWriteTimeout)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			color.Yellow("Failed to copy data to WebSocket: %v\n", err)
		}
	}()

	buffer := ps.getBuffer()
	defer ps.putBuffer(buffer)
	_, err = CopyBufferWithWriteTimeout(conn, ws, *buffer, DefaultWriteTimeout)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		color.Yellow("Failed to copy data to Target: %v\n", err)
	}
}

type deadlineWriter interface {
	io.Writer
	SetWriteDeadline(time.Time) error
}

func CopyBufferWithWriteTimeout(dst deadlineWriter, src io.Reader, buf []byte, timeout time.Duration) (written int64, err error) {
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			dst.SetWriteDeadline(time.Now().Add(timeout))
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errors.New("invalid write result")
				}
			}
			written += int64(nw)
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}
