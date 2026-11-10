package relay

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Admin serves a small JSON control API on top of the relay: statistics,
// network listing, network pre-creation and member kicking. It is intended to
// be bound to 127.0.0.1 or protected by the reverse proxy of your choice.
type Admin struct {
	s   *Server
	tok string
	mux *http.ServeMux
}

// NewAdmin creates the admin HTTP handler. token may be empty to disable auth.
func NewAdmin(s *Server, token string) *Admin {
	a := &Admin{s: s, tok: token, mux: http.NewServeMux()}
	a.mux.HandleFunc("/api/v1/health", a.health)
	a.mux.HandleFunc("/api/v1/stats", a.stats)
	a.mux.HandleFunc("/api/v1/networks", a.networks)
	a.mux.HandleFunc("/api/v1/networks/", a.networkAction)
	return a
}

// ServeHTTP implements http.Handler.
func (a *Admin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.tok != "" {
		h := r.Header.Get("Authorization")
		if h != "Bearer "+a.tok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
	}
	a.mux.ServeHTTP(w, r)
}

func (a *Admin) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"status": "ok", "version": 1, "time": time.Now().UTC()})
}

func (a *Admin) stats(w http.ResponseWriter, r *http.Request) {
	nets := a.s.reg.Networks()
	total := 0
	rows := make([]map[string]any, 0, len(nets))
	for _, n := range nets {
		c := n.MemberCount()
		total += c
		rows = append(rows, map[string]any{
			"id":      n.ID,
			"name":    n.Name,
			"subnet":  n.Subnet(),
			"members": c,
			"created": n.created.UTC(),
		})
	}
	writeJSON(w, map[string]any{
		"counters":       a.s.StatsSnapshot(),
		"online_members": total,
		"networks":       rows,
	})
}

type createRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (a *Admin) networks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.stats(w, r)
	case http.MethodPost:
		var req createRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Password == "" {
			http.Error(w, `{"error":"name and password are required"}`, http.StatusBadRequest)
			return
		}
		n, err := a.s.reg.CreateNetwork(req.Name, req.Password)
		if err != nil {
			http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusConflict)
			return
		}
		writeJSON(w, map[string]any{"id": n.ID, "name": n.Name, "subnet": n.Subnet()})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// /api/v1/networks/{name}            -> GET members
// /api/v1/networks/{name}/members    -> GET members
// /api/v1/networks/{name}/members/{member} DELETE -> kick
func (a *Admin) networkAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/networks/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	net := a.s.reg.Lookup(parts[0])
	if net == nil {
		http.Error(w, `{"error":"network not found"}`, http.StatusNotFound)
		return
	}
	if len(parts) >= 3 && parts[1] == "members" && r.Method == http.MethodDelete {
		if a.s.reg.Kick(net, parts[2]) {
			writeJSON(w, map[string]any{"kicked": parts[2]})
		} else {
			http.Error(w, `{"error":"member not found"}`, http.StatusNotFound)
		}
		return
	}
	if len(parts) >= 2 && parts[1] == "members" && r.Method == http.MethodGet {
		rows := []map[string]any{}
		for _, m := range net.Members() {
			rows = append(rows, map[string]any{
				"name":      m.Name,
				"ip":        ipString(m.IP),
				"joined":    m.Joined.UTC(),
				"transport": m.Session.Transport().String(),
				"bytes_in":  m.Session.rxBytes.Load(),
				"bytes_out": m.Session.txBytes.Load(),
			})
		}
		writeJSON(w, map[string]any{"network": net.Name, "subnet": net.Subnet(), "members": rows})
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"id": net.ID, "name": net.Name, "subnet": net.Subnet(), "members": net.MemberCount()})
		return
	}
	http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ParseInt is a helper used by the CLI for optional numeric flags.
func ParseInt(s string, def int) int {
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}
