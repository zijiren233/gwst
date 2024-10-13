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

type ConnectConfig struct {
	TargetAddr string
	Path       string
	Target     string
	TLS        bool
	ServerName string
	Insecure   bool
	UDP        bool
	Dialer     *net.Dialer
}

type ConnectOption func(*ConnectConfig)

func WithTargetAddr(targetAddr string) ConnectOption {
	return func(c *ConnectConfig) {
		c.TargetAddr = targetAddr
	}
}

func WithPath(path string) ConnectOption {
	return func(c *ConnectConfig) {
		c.Path = path
	}
}

func WithTarget(target string) ConnectOption {
	return func(c *ConnectConfig) {
		c.Target = target
	}
}

func WithDialTLS(serverName string, insecure bool) ConnectOption {
	return func(c *ConnectConfig) {
		c.TLS = true
		c.ServerName = serverName
		c.Insecure = insecure
	}
}

func WithUDP() ConnectOption {
	return func(c *ConnectConfig) {
		c.UDP = true
	}
}

func WithDialer(dialer *net.Dialer) ConnectOption {
	return func(c *ConnectConfig) {
		c.Dialer = dialer
	}
}

func Connect(ctx context.Context, opts ...ConnectOption) (net.Conn, error) {
	cfg := &ConnectConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.Dialer == nil {
		cfg.Dialer = defaultDialer
	}

	addr, port, err := parseHostAndPort(cfg.TargetAddr, cfg.TLS)
	if err != nil {
		return nil, err
	}

	cfg.Path = ensureLeadingSlash(cfg.Path)

	var ws *websocket.Conn
	if cfg.TLS {
		ws, err = connectTLS(ctx, addr, port, cfg)
	} else {
		ws, err = connectNonTLS(ctx, addr, port, cfg)
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

func connectTLS(ctx context.Context, addr, port string, cfg *ConnectConfig) (*websocket.Conn, error) {
	ws_config, err := createWebsocketConfig("wss", addr, port, cfg)
	if err != nil {
		return nil, err
	}

	if cfg.ServerName == "" {
		cfg.ServerName = addr
	}

	config := &tls.Config{
		InsecureSkipVerify: cfg.Insecure,
		ServerName:         cfg.ServerName,
	}

	dialConn, err := dialWithTimeout(ctx, cfg.Dialer, addr, port)
	if err != nil {
		return nil, err
	}

	client, err := createTLSClient(dialConn, config)
	if err != nil {
		return nil, err
	}

	return websocket.NewClient(ws_config, client)
}

func connectNonTLS(ctx context.Context, addr, port string, cfg *ConnectConfig) (*websocket.Conn, error) {
	ws_config, err := createWebsocketConfig("ws", addr, port, cfg)
	if err != nil {
		return nil, err
	}
	return ws_config.DialContext(ctx)
}

func createWebsocketConfig(scheme, addr, port string, cfg *ConnectConfig) (*websocket.Config, error) {
	url := fmt.Sprintf("%s://%s:%s%s", scheme, addr, port, cfg.Path)
	ws_config, err := websocket.NewConfig(url, url)
	if err != nil {
		return nil, fmt.Errorf("failed to create websocket config: %w", err)
	}
	setReqHeader(ws_config, cfg.UDP, cfg.Target)
	ws_config.Dialer = cfg.Dialer
	return ws_config, nil
}

func setReqHeader(ws_config *websocket.Config, isUdp bool, target string) {
	ws_config.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.198 Safari/537.36")
	if target != "" {
		ws_config.Header.Set("X-Target", target)
	}
	if isUdp {
		ws_config.Header.Set("X-Protocol", "udp")
	}
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

type Dialer struct {
	config ConnectConfig
}

func NewDialer(addr, path string, options ...ConnectOption) *Dialer {
	wc := &Dialer{
		config: ConnectConfig{
			TargetAddr: addr,
			Path:       path,
		},
	}
	for _, option := range options {
		option(&wc.config)
	}
	return wc
}

func (wc *Dialer) Dial(network string) (net.Conn, error) {
	return wc.DialContext(context.Background(), network)
}

func (wc *Dialer) DialContext(ctx context.Context, network string) (net.Conn, error) {
	cfg := wc.config
	cfg.UDP = strings.HasPrefix(network, "udp")
	return Connect(ctx, func(c *ConnectConfig) { *c = cfg })
}

func (wc *Dialer) DialUDP() (net.Conn, error) {
	return wc.Dial("udp")
}

func (wc *Dialer) DialContextUDP(ctx context.Context) (net.Conn, error) {
	return wc.DialContext(ctx, "udp")
}

func (wc *Dialer) DialTCP() (net.Conn, error) {
	return wc.Dial("tcp")
}

func (wc *Dialer) DialContextTCP(ctx context.Context) (net.Conn, error) {
	return wc.DialContext(ctx, "tcp")
}
