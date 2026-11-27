package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/meetstream-ai/cli/internal/config"
)

// listenSession is what a running `meetstream listen` tells other commands on
// this computer (join, trigger, env). It holds URLs only, never a key.
type listenSession struct {
	LocalURL  string    `json:"local_url"`
	PublicURL string    `json:"public_url,omitempty"`
	Tunnel    string    `json:"tunnel,omitempty"`
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
}

func listenSessionPath() string { return filepath.Join(config.Dir(), "listen.json") }

func saveListenSession(s *listenSession) error {
	if err := os.MkdirAll(config.Dir(), 0700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(listenSessionPath(), b, 0600)
}

func removeListenSession() { _ = os.Remove(listenSessionPath()) }

// loadListenSession returns the running session, or nil when there is none or
// its process has ended.
func loadListenSession() *listenSession {
	b, err := os.ReadFile(listenSessionPath())
	if err != nil {
		return nil
	}
	var s listenSession
	if json.Unmarshal(b, &s) != nil || s.LocalURL == "" {
		return nil
	}
	if !processAlive(s.PID) {
		return nil
	}
	return &s
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// callbackURL is the URL a new bot should send events to: the tunnel URL, or the
// local URL when the API is local (mock). "" when the API cannot reach it.
func (s *listenSession) callbackURL(apiURL string) string {
	if s == nil {
		return ""
	}
	if s.PublicURL != "" {
		return s.PublicURL
	}
	if isLocalAPI(apiURL) {
		return s.LocalURL
	}
	return ""
}

func tunnelName(v string) string {
	switch {
	case v == "":
		return ""
	case strings.HasPrefix(v, "https://"):
		return "your URL"
	}
	return v
}

type tunnelProc struct {
	URL string
	cmd *exec.Cmd
}

func (t *tunnelProc) Stop() {
	if t != nil && t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
	}
}

var (
	cloudflaredURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
	// lookTunnel and startProc are swapped in tests.
	lookTunnel = exec.LookPath
)

var tunnelInstall = map[string]string{
	"cloudflared": "install cloudflared: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/ (macOS: brew install cloudflared)",
	"ngrok":       "install ngrok and add your authtoken: https://ngrok.com/download",
}

// startTunnel gives a public URL for the local port. It runs only a binary the
// user installed, and stops it when listen stops.
func startTunnel(ctx context.Context, kind string, port int, path string) (*tunnelProc, error) {
	if strings.HasPrefix(kind, "https://") {
		u, err := url.Parse(kind)
		if err != nil || u.Host == "" {
			return nil, usageErr("--tunnel URL is not valid", "")
		}
		if u.Path == "" || u.Path == "/" {
			u.Path = path
		}
		return &tunnelProc{URL: u.String()}, nil
	}
	if kind != "cloudflared" && kind != "ngrok" {
		return nil, usageErr("unknown tunnel "+kind, "choose cloudflared, ngrok, or an https:// URL you run")
	}
	bin, err := lookTunnel(kind)
	if err != nil {
		return nil, &ExitError{Code: 3, Message: kind + " is not installed", Hint: tunnelInstall[kind]}
	}
	local := "http://127.0.0.1:" + strconv.Itoa(port)
	var c *exec.Cmd
	if kind == "cloudflared" {
		c = exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate", "--url", local)
	} else {
		c = exec.CommandContext(ctx, bin, "http", strconv.Itoa(port), "--log", "stdout", "--log-format", "json")
	}
	var r io.ReadCloser
	if kind == "cloudflared" {
		r, err = c.StderrPipe()
	} else {
		r, err = c.StdoutPipe()
	}
	if err != nil {
		return nil, err
	}
	if err := c.Start(); err != nil {
		return nil, err
	}
	t := &tunnelProc{cmd: c}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if u := tunnelURLFrom(kind, sc.Text()); u != "" {
				select {
				case found <- u:
				default:
				}
			}
		}
	}()
	select {
	case u := <-found:
		t.URL = strings.TrimRight(u, "/") + path
		return t, nil
	case <-time.After(30 * time.Second):
		t.Stop()
		return nil, &ExitError{Code: 1, Message: kind + " gave no public URL in 30 s", Hint: "run " + kind + " by hand to see its error"}
	case <-ctx.Done():
		t.Stop()
		return nil, errors.New("interrupted")
	}
}

// tunnelURLFrom reads one log line from the tunnel binary.
func tunnelURLFrom(kind, line string) string {
	if kind == "cloudflared" {
		return cloudflaredURL.FindString(line)
	}
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return ""
	}
	if u, _ := m["url"].(string); strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}
