package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meetstream-ai/cli/internal/ui"
	"github.com/spf13/cobra"
)

func webhookHandler(path, forward string, jsonLines bool, out, diagnostics io.Writer) http.Handler {
	var mu sync.Mutex
	client := &http.Client{Timeout: 10 * time.Second}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" {
			fmt.Fprint(w, "meetstream listen")
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "could not read event", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		var payload any
		if json.Unmarshal(raw, &payload) != nil {
			payload = map[string]any{"raw": string(raw)}
		}
		func() {
			mu.Lock()
			defer mu.Unlock()
			if jsonLines {
				_ = json.NewEncoder(out).Encode(payload)
				return
			}
			fmt.Fprintln(out, renderWebhookEvent(object(payload), time.Now().Format("15:04:05")))
		}()
		if forward != "" {
			req, err := http.NewRequest("POST", forward, bytes.NewReader(raw))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
				var res *http.Response
				res, err = client.Do(req)
				if res != nil {
					_, _ = io.Copy(io.Discard, res.Body)
					res.Body.Close()
					if res.StatusCode/100 != 2 {
						err = fmt.Errorf("HTTP %d", res.StatusCode)
					}
				}
			}
			if err != nil {
				mu.Lock()
				fmt.Fprintln(diagnostics, "forward failed")
				mu.Unlock()
			}
		}
	})
}

// serveWebhooks accepts an already-bound listener so tests can use port 0.
func serveWebhooks(ctx context.Context, ln net.Listener, handler http.Handler) error {
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(stop); err != nil {
			_ = server.Close()
		}
		<-done
		return &ExitError{Code: 130, Message: "interrupted", Silent: true}
	}
}

func newListenCmd() *cobra.Command {
	var port int
	var host, path, forward string
	cmd := &cobra.Command{Use: "listen", Short: "Receive local webhooks (expose with a tunnel)", GroupID: "tool", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if port < 0 || port > 65535 {
			return usageErr("port must be between 0 and 65535", "")
		}
		if !strings.HasPrefix(path, "/") {
			return usageErr("path must start with /", "")
		}
		if forward != "" {
			u, err := url.Parse(forward)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return usageErr("forward must be an HTTP(S) URL", "")
			}
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Listening on http://%s%s · expose with a tunnel · Ctrl-C to stop\n", ln.Addr(), path)
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		out := ui.Out
		if g.JSON {
			out = os.Stdout
		}
		return serveWebhooks(ctx, ln, webhookHandler(path, forward, g.JSON, out, os.Stderr))
	}}
	f := cmd.Flags()
	f.IntVarP(&port, "port", "p", 3333, "local port")
	f.StringVar(&host, "host", "127.0.0.1", "bind address (0.0.0.0 must be explicit)")
	f.StringVar(&path, "path", "/webhook", "webhook path")
	f.StringVar(&forward, "forward", "", "also forward events to this URL")
	return cmd
}

// eventNames are webhook events in plain words.
var eventNames = map[string]string{
	"bot.joining": "Bot is joining", "bot.inmeeting": "Bot is in the meeting", "bot.joined": "Bot is in the meeting",
	"bot.stopped": "Bot left the meeting", "bot.left": "Bot left the meeting", "bot.recording": "Bot is recording",
	"transcription.processed": "Transcript ready", "audio.processed": "Audio ready", "video.processed": "Video ready",
	"bot.waiting_room": "Bot is in the waiting room", "bot.denied": "Bot was not let in", "bot.notallowed": "Bot was not let in",
	"bot.kicked": "Bot was removed", "bot.failed": "Bot failed",
}

// renderWebhookEvent is one webhook as a readable line, with the next command
// when there is one.
func renderWebhookEvent(m map[string]any, clock string) string {
	event := first(m, "event")
	name := eventNames[strings.ToLower(event)]
	switch {
	case name == "" && event == "":
		name = "Event without a name"
	case name == "":
		name = event
	}
	bits := []string{ui.DimStyle.Render(clock), fmt.Sprintf("%-26s", name)}
	if s := text(m["bot_status"]); s != "" {
		bits = append(bits, statusPill(s))
	}
	if code, ok := m["status_code"]; ok {
		bits = append(bits, ui.DimStyle.Render("code "+plain(code)))
	}
	id := text(m["bot_id"])
	if id != "" {
		bits = append(bits, ui.DimStyle.Render(shortID(id)))
	}
	lines := []string{strings.Join(bits, "  ")}
	pad := "          "
	if message := text(m["message"]); message != "" {
		lines = append(lines, pad+message)
	}
	if event == "transcription.processed" && id != "" {
		lines = append(lines, pad+"Read it: "+ui.CmdStyle.Render("meetstream transcript "+id))
	}
	if event == "bot.stopped" {
		reason := first(m, "bot_event")
		if reason == "" {
			reason = map[string]string{"notallowed": "bot.notallowed", "denied": "bot.denied", "error": "bot.failed", "failed": "bot.failed"}[strings.ToLower(text(m["bot_status"]))]
		}
		switch reason {
		case "bot.kicked", "bot.notallowed", "bot.denied", "bot.failed":
			lines = append(lines, pad+ui.DimStyle.Render("Ended early: ")+strings.ToLower(eventNames[reason]))
		}
	}
	return strings.Join(lines, "\n")
}
