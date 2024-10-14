package main

import (
	"os"
	"runtime/debug"
	"time"

	"github.com/fatih/color"
	"github.com/zijiren233/gwst/ws"
	"gopkg.in/yaml.v3"
)

func init() {
	debug.SetGCPercent(10)
}

type Endpoint struct {
	IsClient       bool              `yaml:"is_client"`
	ListenAddr     string            `yaml:"listen_addr"`
	TargetAddr     string            `yaml:"target_addr"`
	AllowedTargets []string          `yaml:"allowed_targets"`
	NamedTargets   map[string]string `yaml:"named_targets"`
	Target         string            `yaml:"target"`
	NamedTarget    string            `yaml:"named_target"`
	Path           string            `yaml:"path"`
	TLS            bool              `yaml:"tls"`
	CertFile       string            `yaml:"cert_file"`
	KeyFile        string            `yaml:"key_file"`
	ServerName     string            `yaml:"server_name"`
	Insecure       bool              `yaml:"insecure"`
	DisableTCP     bool              `yaml:"disable_tcp"`
	DisableUDP     bool              `yaml:"disable_udp"`
}

func main() {
	if len(os.Args) < 2 {
		color.Red("Usage: gwst <config.yaml>")
		os.Exit(1)
	}

	configFile := os.Args[1]
	yamlFile, err := os.ReadFile(configFile)
	if err != nil {
		color.Red("Error reading YAML file: %v", err)
		os.Exit(1)
	}

	var endpoints []Endpoint
	err = yaml.Unmarshal(yamlFile, &endpoints)
	if err != nil {
		color.Red("Error parsing YAML file: %v", err)
		os.Exit(1)
	}

	if len(endpoints) == 0 {
		color.Red("No endpoints found in config file")
		os.Exit(1)
	}

	for _, endpoint := range endpoints {
		run(endpoint)
	}

	color.Magenta("All endpoints started, press Ctrl+C to stop")
	select {}
}

func run(endpoint Endpoint) {
	var s server
	if endpoint.IsClient {
		printClientInfo(endpoint)
		s = newClient(endpoint)
	} else {
		printServerInfo(endpoint)
		s = newServer(endpoint)
	}
	go func() {
		defer func() {
			if err := s.Close(); err != nil {
				color.Red("Error closing %s %v", endpoint.ListenAddr, err)
			}
		}()
		err := s.Serve()
		if err != nil {
			color.Red("Error serving %s %v", endpoint.ListenAddr, err)
			color.Yellow("Restarting %s in 3 seconds...", endpoint.ListenAddr)
			time.AfterFunc(time.Second*3, func() {
				run(endpoint)
			})
		}
	}()
	<-s.OnListened()
}

type server interface {
	Serve() error
	OnListened() <-chan struct{}
	Close() error
}

func printClientInfo(config Endpoint) {
	color.Cyan("----------------------------------------")
	if config.Target == "" && config.NamedTarget == "" {
		color.Cyan("Starting client on %s -> %s", config.ListenAddr, config.TargetAddr)
	} else if config.NamedTarget != "" {
		color.Cyan("Starting client on %s -> %s (Named: %s)", config.ListenAddr, config.TargetAddr, config.NamedTarget)
	} else {
		color.Cyan("Starting client on %s -> %s (Target: %s)", config.ListenAddr, config.TargetAddr, config.Target)
	}
	color.Cyan("----------------------------------------")
}

func printServerInfo(config Endpoint) {
	color.Green("----------------------------------------")
	if len(config.AllowedTargets) != 0 || len(config.NamedTargets) != 0 {
		if config.TargetAddr == "" {
			color.Green("Starting server on %s", config.ListenAddr)
			if len(config.AllowedTargets) != 0 {
				color.Yellow("\tAllowed targets: %v", config.AllowedTargets)
			}
			if len(config.NamedTargets) != 0 {
				color.Yellow("\tNamed targets:")
				for name, target := range config.NamedTargets {
					color.White("\t\t%s -> %s", name, target)
				}
			}
		} else {
			color.Green("Starting server on %s -> %s", config.ListenAddr, config.TargetAddr)
			if len(config.AllowedTargets) != 0 {
				color.Yellow("\tAdditional allowed targets: %v", config.AllowedTargets)
			}
			if len(config.NamedTargets) != 0 {
				color.Yellow("\tNamed targets:")
				for name, target := range config.NamedTargets {
					color.White("\t\t%s -> %s", name, target)
				}
			}
		}
	} else {
		color.Green("Starting server on %s -> %s", config.ListenAddr, config.TargetAddr)
	}
	color.Green("----------------------------------------")
}

func newServer(config Endpoint) *ws.Server {
	var opts []ws.WsServerOption = []ws.WsServerOption{
		ws.WithAllowedTargets(config.AllowedTargets),
		ws.WithNamedTargets(config.NamedTargets),
	}
	if config.TLS {
		opts = append(opts, ws.WithTLS(config.CertFile, config.KeyFile, config.ServerName))
	}

	return ws.NewServer(config.ListenAddr, config.TargetAddr, config.Path, opts...)
}

func newClient(config Endpoint) *ws.Forwarder {
	var opts []ws.ConnectOption = []ws.ConnectOption{
		ws.WithTarget(config.Target),
		ws.WithNamedTarget(config.NamedTarget),
	}
	var forwarderOpts []ws.ForwarderOption
	if config.TLS {
		opts = append(opts, ws.WithDialTLS(config.ServerName, config.Insecure))
	}
	if config.DisableTCP {
		forwarderOpts = append(forwarderOpts, ws.WithDisableTCP())
	}
	if config.DisableUDP {
		forwarderOpts = append(forwarderOpts, ws.WithDisableUDP())
	}
	wsDialer := ws.NewDialer(config.TargetAddr, config.Path, opts...)
	return ws.NewForwarder(config.ListenAddr, wsDialer, forwarderOpts...)
}
