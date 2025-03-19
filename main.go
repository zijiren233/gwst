package main

import (
	stdlog "log"
	"os"
	"runtime/debug"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/zijiren233/gwst/ws"
	"gopkg.in/yaml.v3"
)

func init() {
	debug.SetGCPercent(20)
	initLogger()
}

func initLogger() {
	log.SetOutput(os.Stdout)
	log.SetReportCaller(false)
	log.SetFormatter(&log.TextFormatter{
		ForceColors:      true,
		DisableColors:    false,
		DisableQuote:     true,
		DisableSorting:   false,
		FullTimestamp:    true,
		TimestampFormat:  time.DateTime,
		QuoteEmptyFields: true,
	})
	stdlog.SetOutput(log.StandardLogger().Writer())
}

type Endpoint struct {
	IsClient            bool                      `yaml:"is_client"`
	ListenAddr          string                    `yaml:"listen_addr"`
	LoadBalance         bool                      `yaml:"load_balance"`
	TargetAddr          string                    `yaml:"target_addr"`
	FallbackAddrs       []string                  `yaml:"fallback_addrs"`
	AllowedTargets      map[string][]string       `yaml:"allowed_targets"`
	NamedTargets        map[string]ws.NamedTarget `yaml:"named_targets"`
	Target              string                    `yaml:"target"`
	NamedTarget         string                    `yaml:"named_target"`
	Host                string                    `yaml:"host"`
	Path                string                    `yaml:"path"`
	Key                 string                    `yaml:"key"`
	TLS                 bool                      `yaml:"tls"`
	CertFile            string                    `yaml:"cert_file"`
	KeyFile             string                    `yaml:"key_file"`
	ServerName          string                    `yaml:"server_name"`
	Insecure            bool                      `yaml:"insecure"`
	DisableTCP          bool                      `yaml:"disable_tcp"`
	DisableUDP          bool                      `yaml:"disable_udp"`
	DisableUDPEarlyData bool                      `yaml:"disable_udp_early_data"`
}

type Endpoints []Endpoint

func main() {
	if len(os.Args) < 2 {
		log.Error("Usage: gwst <config.yaml>")
		os.Exit(1)
	}

	configFile := os.Args[1]
	yamlFile, err := os.ReadFile(configFile)
	if err != nil {
		log.Errorf("Error reading YAML file: %v", err)
		os.Exit(1)
	}

	var endpoints Endpoints
	err = yaml.Unmarshal(yamlFile, &endpoints)
	if err != nil {
		log.Errorf("Error parsing YAML file: %v", err)
		os.Exit(1)
	}

	if len(endpoints) == 0 {
		log.Error("No endpoints found in config file")
		os.Exit(1)
	}

	for _, endpoint := range endpoints {
		if endpoint.IsClient {
			printClientInfo(endpoint)
		} else {
			printServerInfo(endpoint)
		}
		go run(endpoint)
	}

	log.Info("All endpoints started, press Ctrl+C to stop")
	select {}
}

func run(endpoint Endpoint) {
	var s server
	if endpoint.IsClient {
		s = newClient(endpoint)
	} else {
		s = newServer(endpoint)
	}
	defer func() {
		if err := s.Close(); err != nil {
			log.Errorf("Error closing %s %v", endpoint.ListenAddr, err)
		}
	}()
	err := s.Serve()
	if err != nil {
		log.Errorf("Error serving %s %v", endpoint.ListenAddr, err)
		log.Warnf("Restarting %s in 3 seconds...", endpoint.ListenAddr)
		time.AfterFunc(time.Second*3, func() {
			run(endpoint)
		})
	}
}

type server interface {
	Serve() error
	Close() error
}

func printClientInfo(config Endpoint) {
	log.Info("----------------------------------------")
	if config.Target == "" && config.NamedTarget == "" {
		log.Infof("Starting client on %s -> %s", config.ListenAddr, config.TargetAddr)
	} else if config.NamedTarget != "" {
		log.Infof("Starting client on %s -> %s (Named: %s)", config.ListenAddr, config.TargetAddr, config.NamedTarget)
	} else {
		log.Infof("Starting client on %s -> %s (Target: %s)", config.ListenAddr, config.TargetAddr, config.Target)
	}
	log.Info("----------------------------------------")
}

func printServerInfo(config Endpoint) {
	log.Info("----------------------------------------")
	if len(config.AllowedTargets) != 0 || len(config.NamedTargets) != 0 {
		if config.TargetAddr == "" {
			log.Infof("Starting server on %s", config.ListenAddr)
			if len(config.AllowedTargets) != 0 {
				log.Warnf("\tAllowed targets: %v", config.AllowedTargets)
			}
			if len(config.NamedTargets) != 0 {
				log.Warn("\tNamed targets:")
				for name, target := range config.NamedTargets {
					log.Infof("\t\t%s -> %s", name, target)
				}
			}
		} else {
			log.Infof("Starting server on %s -> %s", config.ListenAddr, config.TargetAddr)
			if len(config.AllowedTargets) != 0 {
				log.Warnf("\tAdditional allowed targets: %v", config.AllowedTargets)
			}
			if len(config.NamedTargets) != 0 {
				log.Warn("\tNamed targets:")
				for name, target := range config.NamedTargets {
					log.Infof("\t\t%s -> %s", name, target)
				}
			}
		}
	} else {
		log.Infof("Starting server on %s -> %s", config.ListenAddr, config.TargetAddr)
	}
	log.Info("----------------------------------------")
}

func newServer(config Endpoint) *ws.Server {
	handler := ws.NewHandler(
		ws.WithHandlerLogger(log.StandardLogger()),
		ws.WithHandlerDefaultTargetAddr(config.TargetAddr),
		ws.WithHandlerAllowedTargets(config.AllowedTargets),
		ws.WithHandlerNamedTargets(config.NamedTargets),
		ws.WithHandlerFallbackAddrs(config.FallbackAddrs),
		ws.WithHandlerLoadBalance(config.LoadBalance),
		ws.WithHandlerKey(config.Key),
	)
	opts := []ws.ServerOption{
		ws.WithListenAddr(config.ListenAddr),
	}
	if config.TLS {
		opts = append(opts,
			ws.WithTLS(config.CertFile, config.KeyFile),
			ws.WithServerName(config.ServerName),
		)
	}
	return ws.NewServer(config.Path, handler, opts...)
}

func newClient(config Endpoint) *ws.Forwarder {
	opts := []ws.ConnectOption{
		ws.WithAddr(config.TargetAddr),
		ws.WithPath(config.Path),
		ws.WithHost(config.Host),
		ws.WithTarget(config.Target),
		ws.WithNamedTarget(config.NamedTarget),
		ws.WithFallbackAddrs(config.FallbackAddrs),
		ws.WithLoadBalance(config.LoadBalance),
		ws.WithDialTLS(config.TLS),
		ws.WithDialServerName(config.ServerName),
		ws.WithInsecure(config.Insecure),
		ws.WithKey(config.Key),
	}
	forwarderOpts := []ws.ForwarderOption{
		ws.WithLogger(log.StandardLogger()),
	}
	if config.DisableTCP {
		forwarderOpts = append(forwarderOpts, ws.WithDisableTCP())
	}
	if config.DisableUDP {
		forwarderOpts = append(forwarderOpts, ws.WithDisableUDP())
	}
	if config.DisableUDPEarlyData {
		forwarderOpts = append(forwarderOpts, ws.WithDisableUDPEarlyData())
	}
	wsDialer := ws.NewDialer(opts...)
	return ws.NewForwarder(
		config.ListenAddr,
		wsDialer,
		forwarderOpts...,
	)
}
