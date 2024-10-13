package main

import (
	"os"
	"sync"

	"github.com/fatih/color"
	"github.com/zijiren233/gwst/ws"
	"gopkg.in/yaml.v3"
)

type EndpointConfig struct {
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

	var config []EndpointConfig
	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		color.Red("Error parsing YAML file: %v", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup
	for _, endpointConfig := range config {
		wg.Add(1)
		if endpointConfig.IsClient {
			printClientInfo(endpointConfig)
		} else {
			printServerInfo(endpointConfig)
		}
		go func(cfg EndpointConfig) {
			defer wg.Done()
			if cfg.IsClient {
				startClient(cfg)
			} else {
				startServer(cfg)
			}
		}(endpointConfig)
	}

	color.Magenta("All endpoints started, press Ctrl+C to stop")

	wg.Wait()
}

func printClientInfo(config EndpointConfig) {
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

func printServerInfo(config EndpointConfig) {
	color.Green("----------------------------------------")
	if len(config.AllowedTargets) != 0 || len(config.NamedTargets) != 0 {
		if config.TargetAddr == "" {
			color.Green("Starting server on %s", config.ListenAddr)
			if len(config.AllowedTargets) != 0 {
				color.Yellow("  Allowed targets: %v", config.AllowedTargets)
			}
			if len(config.NamedTargets) != 0 {
				color.Yellow("  Named targets:")
				for name, target := range config.NamedTargets {
					color.White("    %s -> %s", name, target)
				}
			}
		} else {
			color.Green("Starting server on %s -> %s", config.ListenAddr, config.TargetAddr)
			if len(config.AllowedTargets) != 0 {
				color.Yellow("  Additional allowed targets: %v", config.AllowedTargets)
			}
			if len(config.NamedTargets) != 0 {
				color.Yellow("  Named targets:")
				for name, target := range config.NamedTargets {
					color.White("    %s -> %s", name, target)
				}
			}
		}
	} else {
		color.Green("Starting server on %s -> %s", config.ListenAddr, config.TargetAddr)
	}
	color.Green("----------------------------------------")
}

func startServer(config EndpointConfig) {
	var opts []ws.WsServerOption = []ws.WsServerOption{
		ws.WithAllowedTargets(config.AllowedTargets),
		ws.WithNamedTargets(config.NamedTargets),
	}
	if config.TLS {
		opts = append(opts, ws.WithTLS(config.CertFile, config.KeyFile, config.ServerName))
	}

	wss := ws.NewServer(config.ListenAddr, config.TargetAddr, config.Path, opts...)
	err := wss.Serve()
	if err != nil {
		color.Red("Error starting server on %s: %v", config.ListenAddr, err)
	}
}

func startClient(config EndpointConfig) {
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
	wsf := ws.NewForwarder(config.ListenAddr, wsDialer, forwarderOpts...)
	err := wsf.Serve()
	if err != nil {
		color.Red("Error serving client on %s: %v", config.ListenAddr, err)
	}
}
