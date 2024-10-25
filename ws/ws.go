package ws

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/fatih/color"
	"golang.org/x/net/websocket"
)

const (
	DefaultUDPDialReadTimeout = time.Second / 2
)

type NamedTarget struct {
	Addr          string
	FallbackAddrs []string
}

type Server struct {
	listenAddr     string
	loadBalance    bool
	targetAddr     string
	fallbackAddrs  []string
	allowedTargets map[string][]string
	namedTargets   map[string]NamedTarget
	path           string

	tls                   bool
	serverName            string
	certFile              string
	keyFile               string
	selfSignedCertOptions []selfSignedCertOption
	GetCertificate        func(*tls.ClientHelloInfo) (*tls.Certificate, error)

	server *http.Server

	onListened        chan struct{}
	shutdowned        chan struct{}
	listenErr         error
	onListenCloseOnce sync.Once

	bufferSize int
	bufferPool *sync.Pool

	disableTcpProtocol bool
	disableUdpProtocol bool
	udpDialReadTimeout time.Duration
}

type WsServerOption func(*Server)

func WithServerFallbackAddrs(fallbackAddrs []string) WsServerOption {
	return func(ps *Server) {
		ps.fallbackAddrs = fallbackAddrs
	}
}

func WithTLS(certFile, keyFile, serverName string) WsServerOption {
	return func(ps *Server) {
		ps.tls = true
		ps.certFile = certFile
		ps.keyFile = keyFile
		ps.serverName = serverName
	}
}

func WithAllowedTargets(allowedTargets map[string][]string) WsServerOption {
	return func(ps *Server) {
		if len(allowedTargets) > 0 {
			ps.allowedTargets = allowedTargets
		}
	}
}

func WithNamedTargets(namedTargets map[string]NamedTarget) WsServerOption {
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

func WithServerLoadBalance(loadBalance bool) WsServerOption {
	return func(ps *Server) {
		ps.loadBalance = loadBalance
	}
}

func WithServerUDPDialReadTimeout(timeout time.Duration) WsServerOption {
	return func(ps *Server) {
		ps.udpDialReadTimeout = timeout
	}
}

func WithServerDisableTcpProtocol(disable bool) WsServerOption {
	return func(ps *Server) {
		ps.disableTcpProtocol = disable
	}
}

func WithServerDisableUdpProtocol(disable bool) WsServerOption {
	return func(ps *Server) {
		ps.disableUdpProtocol = disable
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

	if ps.udpDialReadTimeout == 0 {
		ps.udpDialReadTimeout = DefaultUDPDialReadTimeout
	}

	mux := http.NewServeMux()
	mux.Handle(ps.path, websocket.Handler(ps.handleWebSocket))
	ps.server = &http.Server{
		Addr:              ps.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: time.Second * 5,
		MaxHeaderBytes:    16 * 1024,
	}

	return ps
}

func (ps *Server) getBuffer() *[]byte {
	return ps.bufferPool.Get().(*[]byte)
}

func (ps *Server) putBuffer(buffer *[]byte) {
	if buffer != nil {
		*buffer = (*buffer)[:cap(*buffer)]
		ps.bufferPool.Put(buffer)
	}
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
	if ps.disableTcpProtocol && ps.disableUdpProtocol {
		return errors.New("both TCP and UDP protocols are disabled")
	}

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
	if ps.disableTcpProtocol && protocol == "tcp" {
		color.Red("TCP protocol is disabled")
		return
	}
	if ps.disableUdpProtocol && protocol == "udp" {
		color.Red("UDP protocol is disabled")
		return
	}
	target, fallbackAddrs, err := ps.getTarget(
		ws.Request().Header.Get("X-Target"),
		ws.Request().Header.Get("X-Named-Target"),
	)
	if err != nil {
		color.Red("Error getting target: %v\n", err)
		return
	}

	color.Green("Received WebSocket connection:\n\tAddr: %v\n\tHost: %s\n\tOrigin: %s\n\tTarget: %s\n\tFallback: %v\n\tProtocol: %s\n",
		ws.Request().RemoteAddr, ws.Request().Host, ws.RemoteAddr(), target, fallbackAddrs, protocol)

	if ps.loadBalance {
		target, fallbackAddrs = ps.balanceTargets(target, fallbackAddrs)
	}

	ps.handle(ws, protocol, target, fallbackAddrs)
}

func getProtocol(requestProtocol string) string {
	switch requestProtocol {
	case "udp":
		return "udp"
	default:
		return "tcp"
	}
}

func (ps *Server) getTarget(requestTarget string, namedTarget string) (string, []string, error) {
	if namedTarget != "" {
		if target, ok := ps.namedTargets[namedTarget]; ok {
			return target.Addr, target.FallbackAddrs, nil
		}
	}
	if requestTarget == "" || requestTarget == ps.targetAddr || len(ps.allowedTargets) == 0 {
		return ps.targetAddr, ps.fallbackAddrs, nil
	}

	if v, ok := ps.allowedTargets[requestTarget]; ok {
		return requestTarget, v, nil
	}

	return "", nil, fmt.Errorf("target %s not allowed", requestTarget)
}

func (ps *Server) balanceTargets(target string, fallbackAddrs []string) (string, []string) {
	if len(fallbackAddrs) == 0 {
		return target, fallbackAddrs
	}

	allAddrs := append([]string{target}, fallbackAddrs...)
	rand.Shuffle(len(allAddrs), func(i, j int) {
		allAddrs[i], allAddrs[j] = allAddrs[j], allAddrs[i]
	})

	return allAddrs[0], allAddrs[1:]
}

func (ps *Server) handle(ws *websocket.Conn, network string, addr string, fallbackAddrs []string) {
	if network == "udp" {
		ps.handleUDP(ws, addr, fallbackAddrs)
		return
	}
	ps.handleNetwork(ws, network, addr, fallbackAddrs)
}

func (ps *Server) handleUDP(ws *websocket.Conn, addr string, fallbackAddrs []string) {
	buffer := ps.getBuffer()
	defer ps.putBuffer(buffer)

	ws.SetReadDeadline(time.Now().Add(time.Second))
	n, err := ws.Read(*buffer)
	if err != nil {
		color.Red("Failed to read from WebSocket: %v\n", err)
		return
	}
	ws.SetReadDeadline(time.Time{})

	readBuffer, rn, conn, err := ps.dialUdp(ws.Request().Context(), (*buffer)[:n], addr, fallbackAddrs)
	if err != nil {
		ps.putBuffer(readBuffer)
		color.Red("Failed to connect to UDP target: %v\n", err)
		return
	}
	defer conn.Close()

	if _, err = ws.Write((*readBuffer)[:rn]); err != nil {
		ps.putBuffer(readBuffer)
		color.Red("Failed to write to WebSocket: %v\n", err)
		return
	}

	go func() {
		defer ps.putBuffer(readBuffer)
		if _, err := CopyBufferWithWriteTimeout(conn, ws, *readBuffer, DefaultWriteTimeout); err != nil && !errors.Is(err, net.ErrClosed) {
			color.Yellow("Failed to copy data to Target: %v\n", err)
		}
	}()

	if _, err := CopyBufferWithWriteTimeout(ws, conn, *buffer, DefaultWriteTimeout); err != nil && !errors.Is(err, net.ErrClosed) {
		color.Yellow("Failed to copy data to WebSocket: %v\n", err)
	}
}

func (ps *Server) handleNetwork(ws *websocket.Conn, network, addr string, fallbackAddrs []string) {
	conn, err := dial(ws.Request().Context(), network, addr, fallbackAddrs)
	if err != nil {
		color.Red("Failed to connect to target: %v\n", err)
		return
	}
	defer conn.Close()

	go func() {
		buffer := ps.getBuffer()
		defer ps.putBuffer(buffer)
		if _, err := CopyBufferWithWriteTimeout(conn, ws, *buffer, DefaultWriteTimeout); err != nil && !errors.Is(err, net.ErrClosed) {
			color.Yellow("Failed to copy data to Target: %v\n", err)
		}
	}()

	buffer := ps.getBuffer()
	defer ps.putBuffer(buffer)
	if _, err := CopyBufferWithWriteTimeout(ws, conn, *buffer, DefaultWriteTimeout); err != nil && !errors.Is(err, net.ErrClosed) {
		color.Yellow("Failed to copy data to WebSocket: %v\n", err)
	}
}

func dial(_ context.Context, network, addr string, fallbackAddrs []string) (net.Conn, error) {
	conn, err := net.Dial(network, addr)
	if err == nil {
		return conn, nil
	}

	if len(fallbackAddrs) == 0 {
		return nil, err
	}

	errs := []error{err}
	for _, addr := range fallbackAddrs {
		conn, batchErr := net.Dial("tcp", addr)
		if batchErr == nil {
			return conn, nil
		}
		errs = append(errs, batchErr)
	}
	return nil, errors.Join(errs...)
}

func (ps *Server) dialUdp(ctx context.Context, preWrite []byte, addr string, fallbackAddrs []string) (*[]byte, int, net.Conn, error) {
	buffer, rn, conn, err := ps.dialAndCheckUdp(ctx, preWrite, addr)
	if err == nil {
		return buffer, rn, conn, nil
	}

	if len(fallbackAddrs) == 0 {
		return nil, 0, nil, err
	}

	errs := []error{err}
	for _, addr := range fallbackAddrs {
		buffer, rn, conn, batchErr := ps.dialAndCheckUdp(ctx, preWrite, addr)
		if batchErr == nil {
			color.Yellow("Warning: Target '%s' is unreachable: [%v], using fallback '%s'", addr, err, conn.RemoteAddr().String())
			return buffer, rn, conn, nil
		}
		errs = append(errs, batchErr)
	}
	return nil, 0, nil, errors.Join(errs...)
}

func (ps *Server) dialAndCheckUdp(_ context.Context, preWrite []byte, addr string) (*[]byte, int, net.Conn, error) {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return nil, 0, nil, err
	}

	n, err := conn.Write(preWrite)
	if err != nil {
		conn.Close()
		return nil, 0, nil, err
	}
	if len(preWrite) != n {
		conn.Close()
		return nil, 0, nil, errors.New("invalid write result")
	}

	buffer := ps.getBuffer()
	conn.SetReadDeadline(time.Now().Add(ps.udpDialReadTimeout))
	rn, err := conn.Read(*buffer)
	if err != nil {
		ps.putBuffer(buffer)
		conn.Close()
		return nil, 0, nil, err
	}
	conn.SetReadDeadline(time.Time{})

	return buffer, rn, conn, nil
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
