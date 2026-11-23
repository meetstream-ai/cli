// Package telemetry sends anonymous, opt-out usage events to PostHog, the same
// events the Node CLI (@meetstream/cli 0.x) sent: the command name and the
// version. It never sends API keys, meeting URLs, transcripts, flags or any
// other content. Turn it off with MEETSTREAM_TELEMETRY=0 or DO_NOT_TRACK=1.
package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/user"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Key is the PostHog project's public key (safe to ship, write-only).
const Key = "phc_oFCXdmvQVdgxSwQoCG2GCwupkmt3UqGovj4feMp5ZuCq"

// Endpoint is PostHog's capture API. A variable so tests can point it elsewhere.
var Endpoint = "https://us.i.posthog.com/capture/"

// Enabled reports whether events may be sent.
func Enabled() bool {
	if testing.Testing() && os.Getenv("MEETSTREAM_TELEMETRY_TEST") == "" {
		return false
	}
	switch strings.ToLower(os.Getenv("DO_NOT_TRACK")) {
	case "1", "true", "yes":
		return false
	}
	switch strings.ToLower(os.Getenv("MEETSTREAM_TELEMETRY")) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// AnonID is the same anonymous id the Node CLI used: a hash of host, user and
// home directory, so one computer keeps one id across both CLIs.
func AnonID() string {
	host, _ := os.Hostname()
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	home, _ := os.UserHomeDir()
	sum := sha256.Sum256([]byte(host + "|" + name + "|" + home))
	return "anon_" + hex.EncodeToString(sum[:])[:16]
}

// Track sends one event in the background and returns a function that waits
// for it, at most timeout. It never fails and never blocks longer than that.
func Track(event string, props map[string]any, timeout time.Duration) (wait func()) {
	if !Enabled() {
		return func() {}
	}
	p := map[string]any{"$lib": "meetstream-cli", "os": runtime.GOOS, "arch": runtime.GOARCH}
	for k, v := range props {
		p[k] = v
	}
	body, err := json.Marshal(map[string]any{"api_key": Key, "event": event, "distinct_id": AnonID(), "properties": p})
	if err != nil {
		return func() {}
	}
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	return func() {
		select {
		case <-done:
		case <-ctx.Done():
		}
		cancel()
	}
}
