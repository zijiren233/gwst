package ws

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
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

type ConnectAddrConfig struct {
	Addr          string
	FallbackAddrs []string
}

func (c *ConnectAddrConfig) Clone() *ConnectAddrConfig {
	return &ConnectAddrConfig{
		Addr:          c.Addr,
		FallbackAddrs: slices.Clone(c.FallbackAddrs),
	}
}

type ConnectDialConfig struct {
	splitAddr   string
	splitPort   string
	Host        string
	Path        string
	Target      string
	NamedTarget string
	TLS         bool
	ServerName  string
	Insecure    bool
	UDP         bool
	Dialer      *net.Dialer
	LoadBalance bool
	Headers     http.Header
}

func (c *ConnectDialConfig) Clone() *ConnectDialConfig {
	clone := *c
	clone.Headers = clone.Headers.Clone()
	return &clone
}

type ConnectConfig struct {
	ConnectAddrConfig
	ConnectDialConfig
}

func (c *ConnectConfig) Clone() *ConnectConfig {
	return &ConnectConfig{
		ConnectAddrConfig: *c.ConnectAddrConfig.Clone(),
		ConnectDialConfig: *c.ConnectDialConfig.Clone(),
	}
}

type ConnectOption func(*ConnectConfig)

func WithAddr(addr string) ConnectOption {
	return func(c *ConnectConfig) {
		c.Addr = addr
	}
}

func WithFallbackAddrs(addrs []string) ConnectOption {
	return func(c *ConnectConfig) {
		c.FallbackAddrs = addrs
	}
}

func WithHost(host string) ConnectOption {
	return func(c *ConnectConfig) {
		c.Host = host
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

func WithNamedTarget(namedTarget string) ConnectOption {
	return func(c *ConnectConfig) {
		c.NamedTarget = namedTarget
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

func WithLoadBalance(loadBalance bool) ConnectOption {
	return func(c *ConnectConfig) {
		c.LoadBalance = loadBalance
	}
}

func WithAppendHeaders(headers http.Header) ConnectOption {
	return func(c *ConnectConfig) {
		if c.Headers == nil {
			c.Headers = headers
		} else {
			for k, v := range headers {
				c.Headers[k] = v
			}
		}
	}
}

func WithHeaders(headers http.Header) ConnectOption {
	return func(c *ConnectConfig) {
		c.Headers = headers
	}
}

func Connect(ctx context.Context, opts ...ConnectOption) (net.Conn, error) {
	cfg := ConnectConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	return ConnectWithConfig(ctx, cfg)
}

func ConnectWithConfig(ctx context.Context, cfg ConnectConfig) (net.Conn, error) {
	dialCfg, err := generateDialConfig(cfg.Addr, cfg.ConnectDialConfig)
	if err != nil {
		return nil, err
	}

	if cfg.LoadBalance {
		cfg.Addr, cfg.FallbackAddrs = balanceTargets(cfg.Addr, cfg.FallbackAddrs)
	}

	ws, err := connect(ctx, dialCfg)
	if err == nil {
		ws.PayloadType = websocket.BinaryFrame
		return ws, nil
	}

	if len(cfg.FallbackAddrs) == 0 {
		return nil, fmt.Errorf("failed to connect to %s, error: %w", cfg.Addr, err)
	}

	var errs []error
	for i := 0; i < len(cfg.FallbackAddrs); i += 3 {
		end := i + 3
		if end > len(cfg.FallbackAddrs) {
			end = len(cfg.FallbackAddrs)
		}
		batch := cfg.FallbackAddrs[i:end]

		ws, cerr := connectConcurrent(ctx, dialCfg, batch)
		if cerr == nil {
			color.Yellow("Warning: Target '%s' is unreachable: [%v], using fallback '%s'", cfg.Addr, err, ws.RemoteAddr().String())
			ws.PayloadType = websocket.BinaryFrame
			return ws, nil
		}
		errs = append(errs, cerr)
	}

	return nil, errors.Join(errs...)
}

func balanceTargets(target string, fallbackAddrs []string) (string, []string) {
	if len(fallbackAddrs) == 0 {
		return target, fallbackAddrs
	}

	allAddrs := append([]string{target}, fallbackAddrs...)
	rand.Shuffle(len(allAddrs), func(i, j int) {
		allAddrs[i], allAddrs[j] = allAddrs[j], allAddrs[i]
	})

	return allAddrs[0], allAddrs[1:]
}

func connectConcurrent(ctx context.Context, cfg *ConnectDialConfig, addrs []string) (*websocket.Conn, error) {
	type result struct {
		conn *websocket.Conn
		err  error
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan result, len(addrs))
	var wg sync.WaitGroup

	for _, addr := range addrs {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			dialCfgCopy, err := generateDialConfig(addr, *cfg)
			if err != nil {
				results <- result{nil, err}
				return
			}
			conn, err := connect(ctx, dialCfgCopy)
			results <- result{conn, err}
		}(addr)
	}

	go func() {
		wg.Wait()
		close(results)
		<-ctx.Done()
		for res := range results {
			if res.conn != nil {
				res.conn.Close()
			}
		}
	}()

	var errs []error = make([]error, 0, len(addrs))
	for res := range results {
		if res.err == nil {
			return res.conn, nil
		}
		errs = append(errs, res.err)
	}

	return nil, errors.Join(errs...)
}

func generateDialConfig(addr string, cfg ConnectDialConfig) (*ConnectDialConfig, error) {
	if cfg.Dialer == nil {
		cfg.Dialer = defaultDialer
	}

	addr, port, err := parseAddrAndPort(addr, cfg.TLS)
	if err != nil {
		return nil, err
	}
	cfg.splitAddr = addr
	cfg.splitPort = port

	if cfg.Host == "" {
		if cfg.ServerName != "" {
			cfg.Host = cfg.ServerName
		} else {
			cfg.Host = addr
		}
	}

	if cfg.ServerName == "" {
		cfg.ServerName = cfg.Host
	}

	cfg.Path = ensureLeadingSlash(cfg.Path)

	return &cfg, nil
}

func parseAddrAndPort(addr string, tlsEnabled bool) (string, string, error) {
	domain, port, err := net.SplitHostPort(addr)
	if err != nil {
		if err.Error() == "missing port in address" {
			port = defaultPort(tlsEnabled)
			return addr, port, nil
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

func connect(ctx context.Context, cfg *ConnectDialConfig) (*websocket.Conn, error) {
	ws_config, err := createWebsocketConfig(cfg)
	if err != nil {
		return nil, err
	}

	dialConn, err := dialWithTimeout(ctx, cfg.Dialer, cfg.splitAddr, cfg.splitPort)
	if err != nil {
		return nil, err
	}

	if cfg.TLS {
		config := &tls.Config{
			InsecureSkipVerify: cfg.Insecure,
			ServerName:         cfg.ServerName,
		}

		tlsConn, err := createTLSClient(dialConn, config)
		if err != nil {
			dialConn.Close()
			return nil, err
		}
		dialConn = tlsConn
	}

	ws, err := websocket.NewClient(ws_config, dialConn)
	if err != nil {
		dialConn.Close()
		return nil, err
	}
	return ws, nil
}

func createWebsocketConfig(cfg *ConnectDialConfig) (*websocket.Config, error) {
	var server, origin string
	if cfg.TLS {
		server = fmt.Sprintf("wss://%s%s", cfg.Host, cfg.Path)
		origin = fmt.Sprintf("https://%s%s", cfg.Host, cfg.Path)
	} else {
		server = fmt.Sprintf("ws://%s%s", cfg.Host, cfg.Path)
		origin = fmt.Sprintf("http://%s%s", cfg.Host, cfg.Path)
	}
	ws_config, err := websocket.NewConfig(server, origin)
	if err != nil {
		return nil, fmt.Errorf("failed to create websocket config: %w", err)
	}
	setReqHeader(ws_config, cfg.UDP, cfg.Target, cfg.NamedTarget, cfg.Headers)
	ws_config.Dialer = cfg.Dialer
	return ws_config, nil
}

func setReqHeader(ws_config *websocket.Config, isUdp bool, target string, namedTarget string, headers http.Header) {
	ws_config.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; WOW64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/86.0.4240.198 Safari/537.36")
	if target != "" {
		ws_config.Header.Set("X-Target", target)
	}
	if namedTarget != "" {
		ws_config.Header.Set("X-Named-Target", namedTarget)
	}
	if isUdp {
		ws_config.Header.Set("X-Protocol", "udp")
	}
	for k, v := range headers {
		ws_config.Header[k] = v
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

	hasALPNExtension := false
	for _, ext := range spec.Extensions {
		if alpnExt, ok := ext.(*tls.ALPNExtension); ok {
			alpnExt.AlpnProtocols = []string{"http/1.1"}
			hasALPNExtension = true
		}
	}

	if !hasALPNExtension {
		spec.Extensions = append(spec.Extensions, &tls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}})
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
			ConnectAddrConfig: ConnectAddrConfig{
				Addr: addr,
			},
			ConnectDialConfig: ConnectDialConfig{
				Path: path,
			},
		},
	}
	for _, option := range options {
		option(&wc.config)
	}
	return wc
}

func (wc *Dialer) DialContext(ctx context.Context, network string, options ...ConnectOption) (net.Conn, error) {
	cfg := wc.config.Clone()
	cfg.UDP = strings.HasPrefix(network, "udp")
	for _, option := range options {
		option(cfg)
	}
	return ConnectWithConfig(ctx, *cfg)
}

func (wc *Dialer) Dial(network string, options ...ConnectOption) (net.Conn, error) {
	return wc.DialContext(context.Background(), network, options...)
}

func (wc *Dialer) DialUDP(options ...ConnectOption) (net.Conn, error) {
	return wc.Dial("udp", options...)
}

func (wc *Dialer) DialContextUDP(ctx context.Context, options ...ConnectOption) (net.Conn, error) {
	return wc.DialContext(ctx, "udp", options...)
}

func (wc *Dialer) DialTCP(options ...ConnectOption) (net.Conn, error) {
	return wc.Dial("tcp", options...)
}

func (wc *Dialer) DialContextTCP(ctx context.Context, options ...ConnectOption) (net.Conn, error) {
	return wc.DialContext(ctx, "tcp", options...)
}
