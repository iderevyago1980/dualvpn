package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"dualvpn/internal/vpn"
)

const (
	Version = "1.7.0-dual-vpn"
	DefaultConfigPath = ".dualvpn.toml"
)

func main() {
	var showVer bool
	var configFile string

	flag.BoolVar(&showVer, "version", false, "Print version and exit")
	flag.StringVar(&configFile, "config", DefaultConfigPath, "Path to TOML config file")
	flag.Parse()

	if showVer {
		fmt.Println(Version)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := vpn.DefaultTunnelsConfig() // Use project's built-in config
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Starting VPN dual client version: %s", Version)
	log.Printf("Primary tunnel: [%s], Mode: %s", cfg.Tunnel1.ID, cfg.Tunnel1.Mode)
	log.Printf("Secondary tunnel: [%s], Mode: %s", cfg.Tunnel2.ID, cfg.Tunnel2.Mode)

	mgr, err := vpn.NewDualVPNManager(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to create VPN manager: %v", err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	if err := mgr.DualConnect(ctx); err != nil {
		// Proceed with partial connectivity
		log.Printf("WARNING: VPN connection partially established: %v", err)
	}

	<-sigs
	
	log.Println("Received signal, shutting down...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()

	_ = mgr.DualDisconnect()
	log.Println("VPN client shutdown complete")
}
