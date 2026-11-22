package localapps

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var cloudURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

type CommandTunnel struct {
	Kind   string
	Port   int
	URL    string
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func (t *CommandTunnel) Start(ctx context.Context) (string, error) {
	local := "http://127.0.0.1:" + strconv.Itoa(t.Port)
	binary := "cloudflared"
	args := []string{"tunnel", "--url", local, "--no-autoupdate"}
	if t.Kind == "ngrok" {
		binary = "ngrok"
		args = []string{"http", local, "--log", "stdout", "--log-format", "json"}
		if t.URL != "" {
			args = append(args, "--url", t.URL)
		}
	}
	life, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(life, binary, args...)
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		return "", fmt.Errorf("tunnel program could not start")
	}
	t.mu.Lock()
	t.cancel = cancel
	t.done = make(chan struct{})
	done := t.done
	t.mu.Unlock()
	found := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			url := ""
			if t.Kind == "cloudflare" {
				url = cloudURL.FindString(line)
			} else {
				var m map[string]any
				if json.Unmarshal([]byte(line), &m) == nil {
					u, _ := m["url"].(string)
					if strings.HasPrefix(u, "https://") {
						url = u
					}
				}
			}
			if url != "" {
				select {
				case found <- url:
				default:
				}
			}
		}
		_ = reader.Close()
	}()
	go func() { _ = cmd.Wait(); _ = writer.Close(); close(done) }()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	var url string
	select {
	case url = <-found:
	case <-timer.C:
		t.Stop()
		return "", fmt.Errorf("tunnel did not report an address")
	case <-ctx.Done():
		t.Stop()
		return "", ctx.Err()
	case <-done:
		t.Stop()
		return "", fmt.Errorf("tunnel program exited")
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	client := &http.Client{Timeout: 3 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			switch resp.StatusCode {
			case 502, 503, 504, 530:
			default:
				return url, nil
			}
		}
		select {
		case <-ctx.Done():
			t.Stop()
			return "", ctx.Err()
		case <-deadline.C:
			t.Stop()
			return "", fmt.Errorf("tunnel does not reach the app")
		case <-time.After(time.Second):
		}
	}
}
func (t *CommandTunnel) Stop() {
	t.mu.Lock()
	cancel, done := t.cancel, t.done
	t.cancel = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}
