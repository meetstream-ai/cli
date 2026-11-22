package localapps

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Tunnel interface {
	Start(context.Context) (string, error)
	Stop()
}
type Shares struct {
	mu     sync.Mutex
	open   map[int]share
	closed bool
	Which  func(string) (string, error)
	IsOpen func(int) bool
	Make   func(kind string, port int) Tunnel
}
type share struct {
	tunnel Tunnel
	url    string
}

func New() *Shares {
	return &Shares{open: map[int]share{}, Which: exec.LookPath, IsOpen: func(port int) bool {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 1e9)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, Make: func(kind string, port int) Tunnel { return &CommandTunnel{Kind: kind, Port: port} }}
}
func (s *Shares) Share(ctx context.Context, port int, path string) (string, bool) {
	if port < 1 || port > 65535 {
		return "port must be a number from 1 to 65535", true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "the meeting ended", true
	}
	suffix := ""
	if path != "" {
		suffix = "/" + strings.TrimLeft(path, "/")
	}
	if v, ok := s.open[port]; ok {
		return v.url + suffix + " (already shared)", false
	}
	if len(s.open) >= 2 {
		return "at most 2 local apps per meeting; stop one with stop_local_app first", true
	}
	if !s.IsOpen(port) {
		return fmt.Sprintf("nothing is listening on localhost:%d on the owner's computer; start the app first", port), true
	}
	kinds := []string{}
	for _, p := range [][2]string{{"cloudflare", "cloudflared"}, {"ngrok", "ngrok"}} {
		if _, err := s.Which(p[1]); err == nil {
			kinds = append(kinds, p[0])
		}
	}
	if len(kinds) == 0 {
		return "no tunnel program on this computer (install cloudflared: brew install cloudflared)", true
	}
	for _, kind := range kinds {
		t := s.Make(kind, port)
		url, err := t.Start(ctx)
		if err != nil {
			t.Stop()
			continue
		}
		s.open[port] = share{t, url}
		return fmt.Sprintf("%s%s now shows localhost:%d. Call screen_share with this url. It stays open until the meeting ends or stop_local_app.", url, suffix, port), false
	}
	return fmt.Sprintf("could not open an address for port %d", port), true
}
func (s *Shares) Stop(port int) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ports := []int{}
	for p, v := range s.open {
		if port == 0 || port == p {
			v.tunnel.Stop()
			delete(s.open, p)
			ports = append(ports, p)
		}
	}
	if len(ports) == 0 {
		if port != 0 {
			return fmt.Sprintf("no local app was shared on port %d", port), true
		}
		return "no local app was shared", false
	}
	sort.Ints(ports)
	ss := []string{}
	for _, p := range ports {
		ss = append(ss, strconv.Itoa(p))
	}
	return "closed: " + strings.Join(ss, ", "), false
}
func (s *Shares) Close() { s.mu.Lock(); s.closed = true; s.mu.Unlock(); _, _ = s.Stop(0) }
