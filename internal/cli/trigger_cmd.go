package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/mock"
	"github.com/meetstream-ai/cli/internal/ui"
)

// emitLine writes one compact JSON object per line (NDJSON) for streaming output.
func emitLine(v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintln(os.Stdout, string(b))
}

func newTriggerCmd() *cobra.Command {
	var to, botID string
	var list bool
	cmd := &cobra.Command{
		Use: "trigger <event>", Short: "Send a sample webhook event to your app (no meeting, no charge)", GroupID: "tool",
		Args: cobra.MaximumNArgs(1),
		Long: `Send a sample webhook in the production envelope to a URL, to test a handler.

Without --forward-to, it goes to the running "meetstream listen" session.
Names: bot.kicked, bot.notallowed, bot.denied and bot.failed are sent as
event "bot.stopped" with that bot_event, as production does. live.transcript
is one live-transcript segment.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if list || len(args) == 0 {
				names := mock.SampleNames()
				if len(args) == 0 && !list {
					return usageErr("name an event", "choose: "+strings.Join(names, ", "))
				}
				return emit(map[string]any{"events": names}, func() {
					for _, n := range names {
						fmt.Fprintln(ui.Out, "  "+n)
					}
				})
			}
			payload, ok := mock.Samples(botID)[args[0]]
			if !ok {
				return usageErr("unknown event "+args[0], "meetstream trigger --list")
			}
			if to == "" {
				if s := loadListenSession(); s != nil {
					to = s.LocalURL
				}
			}
			if to == "" {
				return usageErr("no target", "pass --forward-to http://localhost:8000/webhook, or start: meetstream listen")
			}
			start := time.Now()
			code, err := mock.Post(cmd.Context(), &http.Client{Timeout: 10 * time.Second}, to, payload)
			if err != nil {
				return &ExitError{Code: 1, Message: "could not reach " + to, Hint: "is your app running?"}
			}
			ms := time.Since(start).Milliseconds()
			if err := emit(map[string]any{"sent": args[0], "event": payload["event"], "bot_id": botID, "to": to, "status": code, "ms": ms}, func() {
				line := fmt.Sprintf("Sent sample %s %s → %d in %d ms", args[0], ui.DimStyle.Render("(bot "+botID+")"), code, ms)
				if code/100 == 2 {
					ui.OK(line)
				} else {
					ui.Fail(line)
				}
			}); err != nil {
				return err
			}
			if code/100 != 2 {
				return &ExitError{Code: 1, Message: fmt.Sprintf("your app answered %d", code), Silent: true}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&to, "forward-to", "", "URL of your webhook handler (default: the running listen session)")
	f.StringVar(&botID, "bot", "test_bot_7f3c", "bot_id to put in the sample")
	f.BoolVar(&list, "list", false, "list the sample events")
	return cmd
}
