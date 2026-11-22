package bridge

import (
	"bufio"
	"context"
	"fmt"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func ValidateStdioURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/mcp/") {
		return fmt.Errorf("mcp-stdio only talks to the local bridge (http://127.0.0.1:.../mcp/...)")
	}
	if _, _, err = net.SplitHostPort(u.Host); err != nil {
		return fmt.Errorf("invalid loopback address")
	}
	return nil
}
func Stdio(ctx context.Context, raw, secret string, in io.Reader, out io.Writer) error {
	if err := ValidateStdioURL(raw); err != nil {
		return err
	}
	client := &http.Client{Timeout: 70 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), config.MaxBodyBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, strings.NewReader(line))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, config.MaxBodyBytes))
		_ = resp.Body.Close()
		if err == nil && strings.TrimSpace(string(b)) != "" {
			if _, err = fmt.Fprintln(out, strings.TrimSpace(string(b))); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
