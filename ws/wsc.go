package ws

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/zijiren233/gencontainer/rwmap"
)

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
	lock    sync.RWMutex
	lastRec time.Time
}

func (u *udpConnInfo) Read(b []byte) (int, error) {
	n, err := u.Conn.Read(b)
	u.SetLastRec(time.Now())
	return n, err
}

func (u *udpConnInfo) Write(b []byte) (int, error) {
	n, err := u.Conn.Write(b)
	u.SetLastRec(time.Now())
	return n, err
}

func (u *udpConnInfo) GetLastRec() time.Time {
	u.lock.RLock()
	defer u.lock.RUnlock()
	return u.lastRec
}

func (u *udpConnInfo) SetLastRec(t time.Time) {
	u.lock.Lock()
	defer u.lock.Unlock()
	u.lastRec = t
}

type WsForwarder struct {
	listenAddr    string
	wsDialer      *WsDialer
	udpConns      rwmap.RWMap[string, *udpConnInfo]
	tcpListener   net.Listener
	udpConn       *net.UDPConn
	cleanupTicker *time.Ticker
	done          chan struct{}
}

func NewWsForwarder(listenAddr string, wsDialer *WsDialer) *WsForwarder {
	wf := &WsForwarder{
		listenAddr: listenAddr,
		wsDialer:   wsDialer,
		done:       make(chan struct{}),
	}
	go wf.cleanupIdleConnections()
	return wf
}

func (wf *WsForwarder) cleanupIdleConnections() {
	wf.cleanupTicker = time.NewTicker(30 * time.Second)
	defer wf.cleanupTicker.Stop()

	for {
		select {
		case <-wf.cleanupTicker.C:
			now := time.Now()
			wf.udpConns.Range(func(key string, value *udpConnInfo) bool {
				if now.Sub(value.GetLastRec()) > 3*time.Minute {
					if wf.udpConns.CompareAndDelete(key, value) {
						value.Conn.Close()
					}
				}
				return true
			})
		case <-wf.done:
			return
		}
	}
}

func (wf *WsForwarder) Serve() error {
	var err error
	wf.tcpListener, err = net.Listen("tcp", wf.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to start TCP listener: %w", err)
	}

	udpAddr, err := net.ResolveUDPAddr("udp", wf.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to resolve UDP address: %w", err)
	}

	wf.udpConn, err = net.ListenUDP("udp", udpAddr)
	if err != nil {
		wf.tcpListener.Close()
		return fmt.Errorf("failed to start UDP listener: %w", err)
	}

	go wf.handleUDP()

	for {
		conn, err := wf.tcpListener.Accept()
		if err != nil {
			select {
			case <-wf.done:
				return nil
			default:
				fmt.Printf("Failed to accept TCP connection: %v\n", err)
				continue
			}
		}
		go wf.handleTCP(conn)
	}
}

func (wf *WsForwarder) Close() error {
	close(wf.done)
	var errs []error
	if wf.tcpListener != nil {
		errs = append(errs, wf.tcpListener.Close())
	}
	if wf.udpConn != nil {
		errs = append(errs, wf.udpConn.Close())
	}
	if wf.cleanupTicker != nil {
		wf.cleanupTicker.Stop()
	}
	wf.udpConns.Range(func(key string, value *udpConnInfo) bool {
		value.Conn.Close()
		return true
	})
	if len(errs) > 0 {
		return fmt.Errorf("errors closing WsForwarder: %v", errs)
	}
	return nil
}

func (wf *WsForwarder) handleTCP(conn net.Conn) {
	defer conn.Close()

	wsConn, err := wf.wsDialer.DialTCP()
	if err != nil {
		fmt.Printf("Failed to dial WebSocket: %v\n", err)
		return
	}
	defer wsConn.Close()

	go io.Copy(wsConn, conn)
	io.Copy(conn, wsConn)
}

func (wf *WsForwarder) handleUDP() {
	buffer := make([]byte, 65507)
	for {
		select {
		case <-wf.done:
			return
		default:
			n, remoteAddr, err := wf.udpConn.ReadFromUDP(buffer)
			if err != nil {
				fmt.Printf("Failed to read from UDP: %v\n", err)
				continue
			}

			key := remoteAddr.String()
			value, loaded := wf.udpConns.LoadOrStore(key, &udpConnInfo{Conn: nil, lastRec: time.Now()})
			if !loaded {
				if err := wf.setupNewUDPConn(value, remoteAddr); err != nil {
					fmt.Printf("Failed to setup new UDP connection: %v\n", err)
					wf.udpConns.CompareAndDelete(key, value)
					continue
				}
			}

			_, err = value.Write(buffer[:n])
			if err != nil {
				fmt.Printf("Failed to write to UDP connection: %v\n", err)
				wf.udpConns.CompareAndDelete(key, value)
				value.Conn.Close()
			}
		}
	}
}

func (wf *WsForwarder) setupNewUDPConn(value *udpConnInfo, remoteAddr *net.UDPAddr) error {
	newConn, err := wf.wsDialer.DialUDP()
	if err != nil {
		return fmt.Errorf("failed to dial WebSocket for UDP: %w", err)
	}

	value.Conn = newConn
	go wf.handleUDPResponse(value, remoteAddr)
	return nil
}

func (wf *WsForwarder) handleUDPResponse(value *udpConnInfo, remoteAddr *net.UDPAddr) {
	defer func() {
		wf.udpConns.CompareAndDelete(remoteAddr.String(), value)
		value.Conn.Close()
	}()

	buffer := make([]byte, 65507)
	for {
		select {
		case <-wf.done:
			return
		default:
			n, err := value.Read(buffer)
			if err != nil {
				if err != io.EOF {
					fmt.Printf("Failed to read from WebSocket: %v\n", err)
				}
				return
			}

			_, err = wf.udpConn.WriteToUDP(buffer[:n], remoteAddr)
			if err != nil {
				fmt.Printf("Failed to write to UDP: %v\n", err)
				return
			}
		}
	}
}
