package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// runStatus queries a relay's admin API. It is a convenience wrapper so that
// operators do not need curl on the relay host.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	admin := fs.String("admin", "http://127.0.0.1:7778", "admin API base URL")
	token := fs.String("token", os.Getenv("CSLVPN_ADMIN_TOKEN"), "admin bearer token")
	raw := fs.Bool("json", false, "print the raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, *admin+"/api/v1/stats", nil)
	if err != nil {
		return err
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("admin API returned %s: %s", resp.Status, string(body))
	}
	if *raw {
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			return err
		}
		return printJSON(v)
	}
	var out struct {
		Counters map[string]uint64 `json:"counters"`
		Online   int               `json:"online_members"`
		Networks []struct {
			ID      uint32 `json:"id"`
			Name    string `json:"name"`
			Subnet  string `json:"subnet"`
			Members int    `json:"members"`
		} `json:"networks"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	fmt.Printf("relay %s\n", *admin)
	fmt.Printf("  sessions=%d networks=%d online=%d rxFrames=%d txFrames=%d dropped=%d\n",
		out.Counters["sessions"], out.Counters["networks"], out.Online,
		out.Counters["rx_frames"], out.Counters["tx_frames"], out.Counters["dropped"])
	for _, n := range out.Networks {
		fmt.Printf("  network %-24q subnet %-16s members %d\n", n.Name, n.Subnet, n.Members)
	}
	return nil
}
