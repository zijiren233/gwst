package ws

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	"github.com/panjf2000/ants/v2"
	"github.com/zijiren233/gencontainer/rwmap"
)

const (
	DefaultUDPPoolSize  = 512
	DefaultBufferSize   = 16 * 1024
	DefaultWriteTimeout = 15 * time.Second
)

var sharedBufferPool = sync.Pool{
	New: func() interface{} {
		buffer := make([]byte, DefaultBufferSize)
		return &buffer
	},
}

func newBufferPool(size int) *sync.Pool {
	if size == DefaultBufferSize || size <= 0 {
		return &sharedBufferPool
	}
	return &sync.Pool{
		New: func() interface{} {
			buffer := make([]byte, size)
			return &buffer
		},
	}
}

type UDPConn struct {
	*net.UDPConn
	readDeadline  time.Time
	writeDeadline time.Time
}

func (c *UDPConn) Read(b []byte) (int, error) {
	if !c.readDeadline.IsZero() {
		if err := c.UDPConn.SetReadDeadline(c.readDeadline); err != nil {
			return 0, err
		}
	}
	return c.UDPConn.Read(b)
}

func (c *UDPConn) Write(b []byte) (int, error) {
	if !c.writeDeadline.IsZero() {
		if err := c.UDPConn.SetWriteDeadline(c.writeDeadline); err != nil {
			return 0, err
		}
	}
	return c.UDPConn.Write(b)
}

func (c *UDPConn) SetReadDeadline(t time.Time) error {
	c.readDeadline = t
	return nil
}

func (c *UDPConn) SetWriteDeadline(t time.Time) error {
	c.writeDeadline = t
	return nil
}

type udpConnInfo struct {
	net.Conn
	dialLock   sync.Mutex
	dialErr    error
	dialer     *Dialer
	lastActive atomic.Int64
	closed     bool
}

func (u *udpConnInfo) Close() error {
	u.dialLock.Lock()
	defer u.dialLock.Unlock()
	u.closed = true
	if u.Conn != nil {
		return u.Conn.Close()
	}
	return nil
}

func (u *udpConnInfo) Setup() (net.Conn, error) {
	u.dialLock.Lock()
	defer u.dialLock.Unlock()
	if u.closed {
		return nil, net.ErrClosed
	}
	if u.dialErr != nil {
		return nil, u.dialErr
	}
	if u.Conn != nil {
		return u.Conn, nil
	}
	var conn net.Conn
	conn, u.dialErr = u.dialer.DialUDP()
	if u.dialErr != nil {
		return nil, u.dialErr
	}
	u.Conn = conn
	return conn, nil
}

func (u *udpConnInfo) Read(b []byte) (int, error) {
	conn, err := u.Setup()
	if err != nil {
		return 0, err
	}
	u.SetLastActive(time.Now())
	n, err := conn.Read(b)
	u.SetLastActive(time.Now())
	return n, err
}

func (u *udpConnInfo) Write(b []byte) (int, error) {
	conn, err := u.Setup()
	if err != nil {
		return 0, err
	}
	u.SetLastActive(time.Now())
	conn.SetWriteDeadline(time.Now().Add(DefaultWriteTimeout))
	n, err := conn.Write(b)
	u.SetLastActive(time.Now())
	return n, err
}

func (u *udpConnInfo) GetLastActive() time.Time {
	return time.Unix(0, u.lastActive.Load())
}

func (u *udpConnInfo) SetLastActive(t time.Time) {
	u.lastActive.Store(t.UnixNano())
}

type Forwarder struct {
	listenAddr        string
	wsDialer          *Dialer
	udpConns          rwmap.RWMap[string, *udpConnInfo]
	tcpListener       net.Listener
	udpConn           *net.UDPConn
	onListened        chan struct{}
	onListenCloseOnce sync.Once
	shutdowned        chan struct{}
	listenErr         error
	disableTCP        bool
	disableUDP        bool
	udpPool           *ants.Pool
	useSharedUDPPool  bool
	udpPoolSize       int
	udpPoolPreAlloc   bool
	bufferSize        int
	bufferPool        *sync.Pool
}

type ForwarderOption func(*Forwarder)

func WithDisableTCP() ForwarderOption {
	return func(f *Forwarder) {
		f.disableTCP = true
	}
}

func WithDisableUDP() ForwarderOption {
	return func(f *Forwarder) {
		f.disableUDP = true
	}
}

func WithUDPPool(pool *ants.Pool) ForwarderOption {
	return func(f *Forwarder) {
		f.udpPool = pool
		f.useSharedUDPPool = pool != nil
	}
}

func WithUDPPoolSize(size int) ForwarderOption {
	return func(f *Forwarder) {
		f.udpPoolSize = size
	}
}

func WithUDPPoolPreAlloc(preAlloc bool) ForwarderOption {
	return func(f *Forwarder) {
		f.udpPoolPreAlloc = preAlloc
	}
}

func WithBufferSize(size int) ForwarderOption {
	return func(f *Forwarder) {
		f.bufferSize = size
	}
}

func NewForwarder(listenAddr string, wsDialer *Dialer, opts ...ForwarderOption) *Forwarder {
	wf := &Forwarder{
		listenAddr: listenAddr,
		wsDialer:   wsDialer,
		onListened: make(chan struct{}),
		shutdowned: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(wf)
	}
	if wf.bufferSize == 0 {
		wf.bufferSize = DefaultBufferSize
	}
	wf.bufferPool = newBufferPool(wf.bufferSize)
	return wf
}

func (wf *Forwarder) getBuffer() *[]byte {
	return wf.bufferPool.Get().(*[]byte)
}

func (wf *Forwarder) putBuffer(buffer *[]byte) {
	wf.bufferPool.Put(buffer)
}

func (wf *Forwarder) cleanupUDPIdleConnections() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now()
			wf.udpConns.Range(func(key string, value *udpConnInfo) bool {
				if now.Sub(value.GetLastActive()) <= 3*time.Minute {
					return true
				}
				if wf.udpConns.CompareAndDelete(key, value) {
					value.Close()
				}
				return true
			})
		case <-wf.shutdowned:
			wf.udpConns.Range(func(key string, value *udpConnInfo) bool {
				if wf.udpConns.CompareAndDelete(key, value) {
					value.Close()
				}
				return true
			})
			return
		}
	}
}

func (wf *Forwarder) closeOnListened() {
	wf.onListenCloseOnce.Do(func() {
		close(wf.onListened)
	})
}

func (wf *Forwarder) OnListened() <-chan struct{} {
	return wf.onListened
}

func (wf *Forwarder) ListenErr() error {
	return wf.listenErr
}

func (wf *Forwarder) Shutdowned() <-chan struct{} {
	return wf.shutdowned
}

func (wf *Forwarder) ShutdownedBool() bool {
	select {
	case <-wf.shutdowned:
		return true
	default:
		return false
	}
}

func (wf *Forwarder) Serve() (err error) {
	defer wf.closeOnListened()
	defer close(wf.shutdowned)

	if wf.disableTCP && wf.disableUDP {
		return fmt.Errorf("both TCP and UDP are disabled")
	}

	if !wf.disableTCP {
		ln, err := net.Listen("tcp", wf.listenAddr)
		if err != nil {
			wf.listenErr = fmt.Errorf("failed to start TCP listener: %w", err)
			return fmt.Errorf("failed to start TCP listener: %w", err)
		}
		wf.tcpListener = ln
	}

	if !wf.disableUDP {
		if !wf.useSharedUDPPool {
			if wf.udpPoolSize == 0 {
				wf.udpPoolSize = DefaultUDPPoolSize
			}
			udpPool, err := ants.NewPool(
				wf.udpPoolSize,
				ants.WithPreAlloc(wf.udpPoolPreAlloc),
				ants.WithNonblocking(true),
			)
			if err != nil {
				if wf.tcpListener != nil {
					wf.tcpListener.Close()
					wf.tcpListener = nil
				}
				wf.listenErr = fmt.Errorf("failed to create UDP worker pool: %w", err)
				return fmt.Errorf("failed to create UDP worker pool: %w", err)
			}
			wf.udpPool = udpPool
		}

		var udpAddr *net.UDPAddr
		udpAddr, err = net.ResolveUDPAddr("udp", wf.listenAddr)
		if err != nil {
			return fmt.Errorf("failed to resolve UDP address: %w", err)
		}

		var udpConn *net.UDPConn
		udpConn, err = net.ListenUDP("udp", udpAddr)
		if err != nil {
			if wf.tcpListener != nil {
				wf.tcpListener.Close()
				wf.tcpListener = nil
			}
			wf.listenErr = fmt.Errorf("failed to start UDP listener: %w", err)
			return fmt.Errorf("failed to start UDP listener: %w", err)
		}
		wf.udpConn = udpConn

		go wf.cleanupUDPIdleConnections()
	}

	wf.closeOnListened()

	if !wf.disableTCP && !wf.disableUDP {
		go func() {
			for {
				err := wf.processUDP()
				if err != nil {
					if errors.Is(err, net.ErrClosed) {
						return
					}
					color.Red("Failed to process UDP: %v", err)
				}
			}
		}()
		for {
			conn, err := wf.tcpListener.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return nil
				}
				color.Red("Failed to accept TCP connection: %v", err)
				continue
			}
			go wf.handleTCP(conn)
		}
	} else if !wf.disableTCP {
		for {
			conn, err := wf.tcpListener.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return nil
				}
				color.Red("Failed to accept TCP connection: %v", err)
				continue
			}
			go wf.handleTCP(conn)
		}
	} else {
		for {
			err := wf.processUDP()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return nil
				}
				color.Red("Failed to process UDP: %v", err)
			}
		}
	}
}

func (wf *Forwarder) Close() error {
	wf.closeOnListened()
	var errs []error
	if wf.tcpListener != nil {
		err := wf.tcpListener.Close()
		if err != nil {
			errs = append(errs, err)
		}
	}
	if wf.udpConn != nil {
		err := wf.udpConn.Close()
		if err != nil {
			errs = append(errs, err)
		}
	}
	if !wf.useSharedUDPPool && wf.udpPool != nil {
		wf.udpPool.Release()
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing WsForwarder: %v", errs)
	}
	return nil
}

func (wf *Forwarder) handleTCP(conn net.Conn) {
	defer conn.Close()

	wsConn, err := wf.wsDialer.DialTCP()
	if err != nil {
		color.Red("Failed to dial WebSocket: %v", err)
		return
	}
	defer wsConn.Close()

	go func() {
		buffer := wf.getBuffer()
		defer wf.putBuffer(buffer)
		_, err := CopyBufferWithWriteTimeout(wsConn, conn, *buffer, DefaultWriteTimeout)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			color.Yellow("Failed to copy data to WebSocket: %v\n", err)
		}
	}()
	buffer := wf.getBuffer()
	defer wf.putBuffer(buffer)
	_, err = CopyBufferWithWriteTimeout(conn, wsConn, *buffer, DefaultWriteTimeout)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		color.Yellow("Failed to copy data to Target: %v\n", err)
	}
}

func (wf *Forwarder) processUDP() error {
	buffer := wf.getBuffer()

	n, remoteAddr, err := wf.udpConn.ReadFromUDP(*buffer)
	if err != nil {
		wf.putBuffer(buffer)
		return fmt.Errorf("failed to read from UDP: %w", err)
	}

	err = wf.udpPool.Submit(func() {
		defer wf.putBuffer(buffer)

		key := remoteAddr.String()
		value, loaded := wf.udpConns.LoadOrStore(key, &udpConnInfo{dialer: wf.wsDialer})
		if !loaded {
			if _, err := value.Setup(); err != nil {
				color.Red("Failed to setup new UDP in websocket connection: %v", err)
				wf.udpConns.CompareAndDelete(key, value)
				return
			}
			go wf.handleUDPResponse(value, remoteAddr)
		}

		_, err := value.Write((*buffer)[:n])
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				wf.udpConns.CompareAndDelete(key, value)
				return
			}
			color.Red("Failed to write to UDP in websocket connection: %v", err)
			if wf.udpConns.CompareAndDelete(key, value) {
				value.Close()
			}
		}
	})
	if err != nil {
		wf.putBuffer(buffer)
		if err == ants.ErrPoolOverload {
			color.Red("UDP pool is overloaded, dropping packet: %v", remoteAddr.String())
		} else {
			color.Red("Failed to submit UDP task: %v", err)
		}
	}
	return nil
}

func (wf *Forwarder) handleUDPResponse(value *udpConnInfo, remoteAddr *net.UDPAddr) {
	buffer := wf.getBuffer()
	defer func() {
		wf.putBuffer(buffer)
		if wf.udpConns.CompareAndDelete(remoteAddr.String(), value) {
			value.Close()
		}
	}()

	for {
		n, err := value.Read(*buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != io.EOF {
				color.Red("Failed to read from WebSocket: %v", err)
			}
			return
		}

		wf.udpConn.SetWriteDeadline(time.Now().Add(DefaultWriteTimeout))
		_, err = wf.udpConn.WriteToUDP((*buffer)[:n], remoteAddr)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			color.Red("Failed to write to UDP: %v", err)
			return
		}
	}
}
