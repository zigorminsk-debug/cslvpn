// Command cslvpn is the single binary that contains the relay server, the
// tunnel client and the admin CLI.
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Version is injected at build time (-ldflags "-X main.Version=...").
var Version = "dev"

const usage = `CSLVPN - isolated virtual LANs over the internet (Radmin VPN style)

usage:
  cslvpn server    [flags]   run the relay server on a host with a public IP
  cslvpn client    [flags]   join (or create) a virtual network
  cslvpn status    [flags]   show what the relay currently hosts
  cslvpn version

Run "cslvpn server -h" or "cslvpn client -h" for the flags of each command.

Typical three-host setup:
  # relay (public IP 203.0.113.10)
  sudo cslvpn server -listen :7777 -admin 127.0.0.1:7778
  # first client: create the network "office" with password "s3cret"
  sudo cslvpn client -server 203.0.113.10:7777 -network office -password s3cret -create -transport udp
  # second client: join the same network over TCP instead
  sudo cslvpn client -server 203.0.113.10:7777 -network office -password s3cret -transport tcp
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "server":
		err = runServer(args)
	case "client":
		err = runClient(args)
	case "status":
		err = runStatus(args)
	case "version", "-v", "--version":
		fmt.Printf("cslvpn %s (protocol v1)\n", Version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
