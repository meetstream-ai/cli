package cli

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/mock"
	"github.com/meetstream-ai/cli/internal/ui"
)

func newMockCmd() *cobra.Command {
	var port int
	var host, scenario string
	var step float64
	var list bool
	cmd := &cobra.Command{
		Use: "mock", Short: "Run a local fake MeetStream API: no bots join, nothing is charged", GroupID: "tool", Args: cobra.NoArgs,
		Long: `Run a local fake MeetStream API for building and testing.

Point the CLI or your app at it with MEETSTREAM_API_URL. Each bot you create
plays the chosen scenario: status changes, webhooks to its callback_url (and
live transcript to live_transcription_required.webhook_url), then a transcript.
No bot joins a meeting and nothing is charged. Any API key is accepted.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if list {
				rows := []map[string]string{}
				for _, n := range mock.ScenarioNames() {
					rows = append(rows, map[string]string{"name": n, "description": mock.Scenarios[n].Description})
				}
				return emit(map[string]any{"scenarios": rows}, func() {
					for _, r := range rows {
						fmt.Fprintf(ui.Out, "  %-22s %s\n", r["name"], ui.DimStyle.Render(r["description"]))
					}
				})
			}
			sc, ok := mock.Scenarios[scenario]
			if !ok {
				return usageErr("unknown scenario "+scenario, "choose: "+strings.Join(mock.ScenarioNames(), ", "))
			}
			if host != "127.0.0.1" && host != "localhost" && host != "::1" {
				return usageErr("the mock binds to loopback only", "use --host 127.0.0.1")
			}
			ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			srv := mock.New(ctx, sc, time.Duration(step*float64(time.Second)))
			base := "http://" + ln.Addr().String() + "/api/v1"
			if g.JSON {
				srv.Log = func(format string, args ...any) {
					emitLine(map[string]any{"event": "log", "line": fmt.Sprintf(format, args...)})
				}
				emitLine(map[string]any{"event": "ready", "url": base, "scenario": sc.Name})
			} else {
				fmt.Fprintln(ui.Out, ui.TitleStyle.Render("◆ Mock MeetStream API")+" "+ui.DimStyle.Render("no bots join · nothing is charged"))
				fmt.Fprintln(ui.Out, "  "+ui.CmdStyle.Render("export MEETSTREAM_API_URL="+base))
				fmt.Fprintln(ui.Out, "  Scenario "+ui.BoldStyle.Render(sc.Name)+": "+ui.DimStyle.Render(sc.Description))
				fmt.Fprintln(ui.Out, ui.DimStyle.Render("  Other scenarios: "+strings.Join(mock.ScenarioNames(), " · ")))
				fmt.Fprintln(ui.Out)
				srv.Log = func(format string, args ...any) {
					fmt.Fprintln(ui.Out, ui.DimStyle.Render("  "+time.Now().Format("15:04:05")+"  ")+fmt.Sprintf(format, args...))
				}
			}
			return serveMock(ctx, ln, srv)
		},
	}
	f := cmd.Flags()
	f.IntVarP(&port, "port", "p", 4010, "local port")
	f.StringVar(&host, "host", "127.0.0.1", "bind address (loopback only)")
	f.StringVar(&scenario, "scenario", "notetaker-happy", "scripted run for each new bot (see --list)")
	f.Float64Var(&step, "step", 2, "seconds between lifecycle events")
	f.BoolVar(&list, "list", false, "list the scenarios")
	return cmd
}

func serveMock(ctx context.Context, ln net.Listener, h http.Handler) error {
	server := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
		return &ExitError{Code: 130, Message: "interrupted", Silent: true}
	}
}

// isLocalAPI is true when the CLI talks to a mock or dev server on this computer.
func isLocalAPI(apiURL string) bool {
	for _, h := range []string{"//127.0.0.1", "//localhost", "//[::1]"} {
		if strings.Contains(apiURL, h) {
			return true
		}
	}
	return false
}
