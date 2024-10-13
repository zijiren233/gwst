package main

import (
	"os"
	"sync"

	"github.com/fatih/color"
	"github.com/zijiren233/gwst/ws"
	"gopkg.in/yaml.v3"
)

type EndpointConfig struct {
	IsClient       bool     `yaml:"is_client"`
	ListenAddr     string   `yaml:"listen_addr"`
	TargetAddr     string   `yaml:"target_addr"`
	AllowedTargets []string `yaml:"allowed_targets"`
	Target         string   `yaml:"target"`
	Path           string   `yaml:"path"`
	TLS            bool     `yaml:"tls"`
	CertFile       string   `yaml:"cert_file"`
	KeyFile        string   `yaml:"key_file"`
	ServerName     string   `yaml:"server_name"`
	Insecure       bool     `yaml:"insecure"`
	DisableTCP     bool     `yaml:"disable_tcp"`
	DisableUDP     bool     `yaml:"disable_udp"`
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
			if endpointConfig.Target == "" {
				color.Cyan("Starting client on %s -> %s", endpointConfig.ListenAddr, endpointConfig.TargetAddr)
			} else {
				color.Cyan("Starting client on %s -> %s -> %s", endpointConfig.ListenAddr, endpointConfig.TargetAddr, endpointConfig.Target)
			}
			go func(cfg EndpointConfig) {
				defer wg.Done()
				startClient(cfg)
			}(endpointConfig)
		} else {
			if len(endpointConfig.AllowedTargets) != 0 {
				if endpointConfig.TargetAddr == "" {
					color.Green("Starting server on %s -> %v", endpointConfig.ListenAddr, endpointConfig.AllowedTargets)
				} else {
					color.Green("Starting server on %s -> %s |-> %v", endpointConfig.ListenAddr, endpointConfig.TargetAddr, endpointConfig.AllowedTargets)
				}
			} else {
				color.Green("Starting server on %s -> %s", endpointConfig.ListenAddr, endpointConfig.TargetAddr)
			}
			go func(cfg EndpointConfig) {
				defer wg.Done()
				startServer(cfg)
			}(endpointConfig)
		}
	}

	wg.Wait()
}

func startServer(config EndpointConfig) {
	var opts []ws.WsServerOption = []ws.WsServerOption{
		ws.WithAllowedTargets(config.AllowedTargets),
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
