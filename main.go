package main

import (
	"fmt"
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
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: gwst <config.yaml>\n")
		os.Exit(1)
	}

	configFile := os.Args[1]
	yamlFile, err := os.ReadFile(configFile)
	if err != nil {
		fmt.Printf("Error reading YAML file: %v\n", err)
		os.Exit(1)
	}

	var config []EndpointConfig
	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		fmt.Printf("Error parsing YAML file: %v\n", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup
	for _, endpointConfig := range config {
		wg.Add(1)
		if endpointConfig.IsClient {
			clientColor := color.New(color.FgHiCyan).SprintFunc()
			if endpointConfig.Target == "" {
				fmt.Printf("%s\n", clientColor(fmt.Sprintf("Starting client on %s -> %s", endpointConfig.ListenAddr, endpointConfig.TargetAddr)))
			} else {
				fmt.Printf("%s\n", clientColor(fmt.Sprintf("Starting client on %s -> %s -> %s", endpointConfig.ListenAddr, endpointConfig.TargetAddr, endpointConfig.Target)))
			}
			go func(cfg EndpointConfig) {
				defer wg.Done()
				startClient(cfg)
			}(endpointConfig)
		} else {
			serverColor := color.New(color.FgHiGreen).SprintFunc()
			if len(endpointConfig.AllowedTargets) != 0 {
				if endpointConfig.TargetAddr == "" {
					fmt.Printf("%s\n", serverColor(fmt.Sprintf("Starting server on %s -> %v", endpointConfig.ListenAddr, endpointConfig.AllowedTargets)))
				} else {
					fmt.Printf("%s\n", serverColor(fmt.Sprintf("Starting server on %s -> %s |-> %v", endpointConfig.ListenAddr, endpointConfig.TargetAddr, endpointConfig.AllowedTargets)))
				}
			} else {
				fmt.Printf("%s\n", serverColor(fmt.Sprintf("Starting server on %s -> %s", endpointConfig.ListenAddr, endpointConfig.TargetAddr)))
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
		fmt.Printf("Error starting server on %s: %v\n", config.ListenAddr, err)
	}
}

func startClient(config EndpointConfig) {
	var opts []ws.ConnectOption = []ws.ConnectOption{
		ws.WithTarget(config.Target),
	}
	if config.TLS {
		opts = append(opts, ws.WithDialTLS(config.ServerName, config.Insecure))
	}

	wsDialer := ws.NewDialer(config.TargetAddr, config.Path, opts...)
	wsf := ws.NewForwarder(config.ListenAddr, wsDialer)
	err := wsf.Serve()
	if err != nil {
		fmt.Printf("Error starting client on %s: %v\n", config.ListenAddr, err)
	}
}
