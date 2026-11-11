package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/zigorminsk-debug/cslvpn/internal/client"
	"github.com/zigorminsk-debug/cslvpn/internal/proto"
)

func runClient(args []string) error {
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	server := fs.String("server", os.Getenv("CSLVPN_SERVER"), "relay address, host:port (env CSLVPN_SERVER)")
	transport := fs.String("transport", "udp", "tunnel transport: udp or tcp")
	network := fs.String("network", "", "virtual network (room) name")
	password := fs.String("password", os.Getenv("CSLVPN_PASSWORD"), "network password (env CSLVPN_PASSWORD)")
	member := fs.String("name", "", "member name shown to other peers (default: hostname)")
	create := fs.Bool("create", false, "create the network if it does not exist yet")
	wantIP := fs.String("ip", "", "requested virtual address inside the network, e.g. 26.0.0.10")
	iface := fs.String("iface", defaultIface(), "TUN interface name")
	mtu := fs.Int("mtu", 1400, "tunnel MTU")
	internet := fs.Bool("internet", false, "route ALL traffic through the relay (requires a relay started with -gateway)")
	verbose := fs.Bool("v", false, "log peer list changes")
	debug := fs.Bool("debug", false, "log every packet (verbose, for troubleshooting)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" || *network == "" || *password == "" {
		return fmt.Errorf("-server, -network and -password are required")
	}
	tr, err := proto.ParseTransport(*transport)
	if err != nil {
		return err
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	logger.Printf("cslvpn %s client (protocol v1, %s/%s)", Version, runtime.GOOS, runtime.GOARCH)

	c := client.New(client.Options{
		Server:    *server,
		Transport: tr,
		Network:   *network,
		Password:  *password,
		Member:    *member,
		Create:    *create,
		WantIP:    *wantIP,
		Iface:     *iface,
		MTU:       *mtu,
		Internet:  *internet,
		Logger:    logger,
		Verbose:   *verbose,
		Debug:     *debug,
		OnConnect: func(w *proto.Welcome, self string) {
			logger.Printf("READY: your address in %q is %s (transport %s)", *network, self, w.Transport)
		},
		OnPeers: func(peers []proto.Peer) {
			if !*verbose {
				return
			}
			for _, p := range peers {
				mark := " "
				if p.Flags&proto.PeerFlagSelf != 0 {
					mark = "*"
				}
				logger.Printf("  %s %-24s %s via %s", mark, p.Name, ipStringClient(p.IP), p.Transport)
			}
		},
	})

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		logger.Printf("shutting down")
		c.Close()
	}()

	err = c.Run()
	if err != nil {
		return err
	}
	return nil
}

func defaultIface() string {
	if runtime.GOOS == "windows" {
		return "CSLVPN"
	}
	return "cslvpn0"
}

func ipStringClient(ip uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
}
