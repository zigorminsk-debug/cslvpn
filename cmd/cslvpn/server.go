package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/zigorminsk-debug/cslvpn/internal/relay"
	"github.com/zigorminsk-debug/cslvpn/internal/tun"
)

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	listen := fs.String("listen", ":7777", "address for both the UDP and TCP relay listeners")
	udpListen := fs.String("udp", "", "UDP listen address (default: -listen)")
	tcpListen := fs.String("tcp", "", "TCP listen address (default: -listen)")
	adminAddr := fs.String("admin", "", "admin HTTP API address, e.g. 127.0.0.1:7778 (disabled when empty)")
	adminToken := fs.String("admin-token", os.Getenv("CSLVPN_ADMIN_TOKEN"), "bearer token for the admin API (env CSLVPN_ADMIN_TOKEN)")
	maxMembers := fs.Int("max-members", 150, "maximum clients per network")
	maxNetworks := fs.Int("max-networks", 4096, "maximum number of networks")
	debug := fs.Bool("debug", false, "log every forwarded packet (verbose, for troubleshooting)")
	allowCreate := fs.Bool("allow-create", true, "let clients create networks on first join")
	idle := fs.Duration("idle-network", 24*time.Hour, "drop a network after it stayed empty this long")
	ban := fs.Duration("ban", 15*time.Minute, "how long a kicked member stays banned")
	natCIDR := fs.String("nat-cidr", "26.0.0.0/8", "virtual subnet masqueraded when -gateway is used")
	gateway := fs.Bool("gateway", false, "serve as internet gateway: clients started with -internet are NATed through -gateway-iface")
	gwIface := fs.String("gateway-iface", "", "existing TUN interface used as the gateway (created by you, see docs/SERVER.md)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lmicroseconds)
	logger.Printf("cslvpn %s relay starting (protocol v1, %s/%s, go %s)", Version, runtime.GOOS, runtime.GOARCH, runtime.Version())

	cfg := relay.DefaultConfig()
	cfg.AllowCreate = *allowCreate
	cfg.MaxMembers = *maxMembers
	cfg.MaxNetworks = *maxNetworks
	cfg.IdleNetwork = *idle
	cfg.BanDuration = *ban
	cfg.Logf = logger.Printf
	cfg.Debug = *debug

	srv := relay.NewServer(cfg, logger)

	if *udpListen == "" {
		*udpListen = *listen
	}
	if *tcpListen == "" {
		*tcpListen = *listen
	}
	if *udpListen != "off" {
		if err := srv.StartUDP(*udpListen); err != nil {
			return fmt.Errorf("udp listener: %w", err)
		}
		logger.Printf("UDP relay listening on %s", srv.UDPAddr())
	}
	if *tcpListen != "off" {
		if err := srv.StartTCP(*tcpListen); err != nil {
			return fmt.Errorf("tcp listener: %w", err)
		}
		logger.Printf("TCP relay listening on %s", srv.TCPAddr())
	}

	if *gateway {
		if *gwIface == "" {
			logger.Printf("warning: -gateway needs -gateway-iface (a TUN device you created and configured)")
		} else {
			dev, err := tun.Open(*gwIface)
			if err != nil {
				return fmt.Errorf("gateway interface %q: %w", *gwIface, err)
			}
			if err := srv.EnableGateway(dev, *natCIDR); err != nil {
				return fmt.Errorf("enabling gateway: %w", err)
			}
		}
	}

	stop := make(chan struct{})
	go srv.Registry().RunGC(stop)

	var httpSrv *http.Server
	if *adminAddr != "" {
		if *adminToken == "" {
			logger.Printf("warning: admin API without -admin-token is open to everyone who can reach %s", *adminAddr)
		}
		admin := relay.NewAdmin(srv, *adminToken)
		httpSrv = &http.Server{Addr: *adminAddr, Handler: admin, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			logger.Printf("admin API on http://%s/api/v1/stats", *adminAddr)
			if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Printf("admin API stopped: %v", err)
			}
		}()
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Printf("shutting down")
	close(stop)
	if httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = httpSrv.Shutdown(ctx)
		cancel()
	}
	srv.Shutdown()
	if *gateway {
		tun.RemoveNAT(*gwIface, *natCIDR)
	}
	return nil
}
