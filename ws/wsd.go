package ws

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"

	tls "github.com/refraction-networking/utls"
	"golang.org/x/net/websocket"
)

var randomFingerprint tls.ClientHelloID

func init() {
	modernFingerprints := []tls.ClientHelloID{
		tls.HelloChrome_Auto,
		tls.HelloFirefox_Auto,
		tls.HelloEdge_Auto,
		tls.HelloSafari_Auto,
		tls.HelloIOS_Auto,
	}
	randomFingerprint = modernFingerprints[rand.Intn(len(modernFingerprints))]
}

var defaultDialer = &net.Dialer{
	Timeout: time.Second * 5,
}

func Connect(ctx context.Context, addr, path string, tls bool, serverName string, insecure bool, udp bool, dialer *net.Dialer) (net.Conn, error) {
	addr, port, err := parseHostAndPort(addr, tls)
	if err != nil {
		return nil, err
	}

	path = ensureLeadingSlash(path)

	if dialer == nil {
		dialer = defaultDialer
	}

	var ws *websocket.Conn
	if tls {
		ws, err = connectTLS(ctx, addr, port, path, serverName, insecure, udp, dialer)
	} else {
		ws, err = connectNonTLS(ctx, addr, port, path, udp, dialer)
	}
	if err != nil {
		return nil, err
	}

	ws.PayloadType = websocket.BinaryFrame
	return ws, nil
}

func parseHostAndPort(host string, tlsEnabled bool) (string, string, error) {
	host = strings.TrimPrefix(strings.TrimPrefix(host, "ws://"), "wss://")
	domain, port, err := net.SplitHostPort(host)
	if err != nil {
		if err.Error() == "missing port in address" {
			port = defaultPort(tlsEnabled)
			return host, port, nil
		}
		return "", "", fmt.Errorf("failed to split host and port: %w", err)
	}
	return domain, port, nil
}

func defaultPort(tlsEnabled bool) string {
	if tlsEnabled {
		return "443"
	}
	return "80"
}

func ensureLeadingSlash(path string) string {
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func connectTLS(ctx context.Context, addr, port, path, serverName string, insecure, isUdp bool, dialer *net.Dialer) (*websocket.Conn, error) {
	ws_config, err := createWebsocketConfig("wss", addr, port, path, isUdp, dialer)
	if err != nil {
		return nil, err
	}

	if serverName == "" {
		serverName = addr
	}

	config := &tls.Config{
		InsecureSkipVerify: insecure,
		ServerName:         serverName,
	}

	dialConn, err := dialWithTimeout(ctx, dialer, addr, port)
	if err != nil {
		return nil, err
	}

	client, err := createTLSClient(dialConn, config)
	if err != nil {
		return nil, err
	}

	return websocket.NewClient(ws_config, client)
}

func connectNonTLS(ctx context.Context, addr, port, path string, isUdp bool, dialer *net.Dialer) (*websocket.Conn, error) {
	ws_config, err := createWebsocketConfig("ws", addr, port, path, isUdp, dialer)
	if err != nil {
		return nil, err
	}
	return ws_config.DialContext(ctx)
}

func createWebsocketConfig(scheme, addr, port, path string, isUdp bool, dialer *net.Dialer) (*websocket.Config, error) {
	url := fmt.Sprintf("%s://%s:%s%s", scheme, addr, port, path)
	ws_config, err := websocket.NewConfig(url, url)
	if err != nil {
		return nil, fmt.Errorf("failed to create websocket config: %w", err)
	}
	setReqHeader(ws_config, isUdp)
	ws_config.Dialer = dialer
	return ws_config, nil
}

func setReqHeader(ws_config *websocket.Config, isUdp bool) {
	ws_config.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.198 Safari/537.36")
	protocol := "tcp"
	if isUdp {
		protocol = "udp"
	}
	ws_config.Header.Set("X-Protocol", protocol)
}

func dialWithTimeout(ctx context.Context, dialer *net.Dialer, addr, port string) (net.Conn, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	return dialer.DialContext(timeoutCtx, "tcp", fmt.Sprintf("%s:%s", addr, port))
}

func createTLSClient(conn net.Conn, config *tls.Config) (*tls.UConn, error) {
	client := tls.UClient(conn, config, tls.HelloCustom)
	spec, err := tls.UTLSIdToSpec(randomFingerprint)
	if err != nil {
		return nil, fmt.Errorf("failed to get utls spec: %w", err)
	}

	for _, ext := range spec.Extensions {
		if alpnExt, ok := ext.(*tls.ALPNExtension); ok {
			alpnExt.AlpnProtocols = []string{"http/1.1"}
		}
	}

	if err := client.ApplyPreset(&spec); err != nil {
		return nil, fmt.Errorf("failed to apply utls spec: %w", err)
	}

	return client, nil
}

type WsDialer struct {
	addr       string
	path       string
	tls        bool
	serverName string
	insecure   bool
	dialer     *net.Dialer
}

type WsClientOption func(*WsDialer)

func WithNetDialer(dialer *net.Dialer) WsClientOption {
	return func(wc *WsDialer) {
		wc.dialer = dialer
	}
}

func WithDialTLS(serverName string, insecure bool) WsClientOption {
	return func(wc *WsDialer) {
		wc.tls = true
		wc.insecure = insecure
		wc.serverName = serverName
	}
}

func NewWsDialer(addr, path string, options ...WsClientOption) *WsDialer {
	wc := &WsDialer{
		addr: addr,
		path: path,
	}
	for _, option := range options {
		option(wc)
	}
	return wc
}

func (wc *WsDialer) Dial(network string) (net.Conn, error) {
	return wc.DialContext(context.Background(), network)
}

func (wc *WsDialer) DialContext(ctx context.Context, network string) (net.Conn, error) {
	return Connect(ctx, wc.addr, wc.path, wc.tls, wc.serverName, wc.insecure, strings.HasPrefix(network, "udp"), wc.dialer)
}

func (wc *WsDialer) DialUDP() (net.Conn, error) {
	return wc.Dial("udp")
}

func (wc *WsDialer) DialContextUDP(ctx context.Context) (net.Conn, error) {
	return wc.DialContext(ctx, "udp")
}

func (wc *WsDialer) DialTCP() (net.Conn, error) {
	return wc.Dial("tcp")
}

func (wc *WsDialer) DialContextTCP(ctx context.Context) (net.Conn, error) {
	return wc.DialContext(ctx, "tcp")
}
