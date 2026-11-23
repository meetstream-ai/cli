package cli

import (
	"errors"
	"net/url"
	"strings"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/spf13/cobra"
)

func newCalendarCmd() *cobra.Command {
	cal := &cobra.Command{Use: "calendar", Short: "Google Calendar auto-join", GroupID: "meet"}
	var clientID, secret, token string
	var own bool
	var hosted calendarConnectOptions
	legacyFlags := []string{"client-id", "client-secret", "refresh-token"}
	connect := &cobra.Command{
		Use:   "connect",
		Short: "Connect your calendar (one-click sign-in, or --own-app)",
		Long: `Connect your Google calendar with one click: sign in in your browser
and MIA auto-joins your meetings. --join-rule accepted (default) joins meetings
you host or accepted; --join-rule all joins every meeting with a link.

Bring your own Google OAuth app instead with --own-app --client-id ... --client-secret ... --refresh-token ...`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			legacy := own
			for _, f := range legacyFlags {
				legacy = legacy || cmd.Flags().Changed(f)
			}
			if !legacy {
				res, err := runCalendarConnect(cmd, hosted)
				if errors.Is(err, errCalendarSkipped) {
					return &ExitError{Code: 130, Message: "calendar connect cancelled"}
				}
				if err != nil {
					return err
				}
				return emit(res, func() {})
			}
			for _, f := range []string{"provider", "join-rule", "open"} {
				if cmd.Flags().Changed(f) {
					return usageErr("--"+f+" is for one-click connect, not --own-app", "drop --own-app and the client flags, or drop --"+f)
				}
			}
			var missing []string
			for _, f := range legacyFlags {
				if !cmd.Flags().Changed(f) {
					missing = append(missing, `"`+f+`"`)
				}
			}
			if len(missing) > 0 {
				return usageErr("required flag(s) "+strings.Join(missing, ", ")+" not set", "")
			}
			c, err := apiClient(cmd)
			if err != nil {
				return err
			}
			body := map[string]any{"google_client_id": clientID, "google_client_secret": secret, "google_refresh_token": token}
			v, err := requestJSON(cmd, c, "POST", "/calendar/create_calendar", body)
			v, err = safeCredentialResult(v, err, body)
			return result(cmd.Name(), v, err)
		},
	}
	connect.Flags().StringVar(&hosted.Provider, "provider", "", "google (asked when omitted; microsoft is in preview)")
	connect.Flags().StringVar(&hosted.JoinRule, "join-rule", "accepted", "accepted: meetings you host or accepted; all: every meeting with a link")
	connect.Flags().BoolVar(&hosted.Open, "open", false, "open the browser even with --no-input")
	connect.Flags().BoolVar(&own, "own-app", false, "use your own Google OAuth app (refresh-token flow)")
	for flag, ptr := range map[string]*string{"client-id": &clientID, "client-secret": &secret, "refresh-token": &token} {
		connect.Flags().StringVar(ptr, flag, "", flag+" (with --own-app)")
	}
	cal.AddCommand(connect)
	for _, action := range []string{"list", "events", "schedule", "unschedule", "reschedule", "cancel"} {
		n, use := 0, action
		switch action {
		case "schedule", "unschedule":
			n = 1
			use += " <event-id>"
		case "reschedule":
			n = 2
			use += " <bot-id> <iso8601>"
		case "cancel":
			n = 1
			use += " <bot-id>"
		}
		sub := botAction(use, n, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
			method, path := "GET", "/calendar/calendars"
			var body any
			switch action {
			case "events":
				path = "/calendar/events"
			case "schedule", "unschedule":
				method = "POST"
				if action == "unschedule" {
					method = "DELETE"
				}
				path = "/calendar/schedule/" + url.PathEscape(args[0])
			case "reschedule":
				method = "PATCH"
				path = "/calendar/scheduled_bots/" + url.PathEscape(args[0])
				body = map[string]any{"scheduled_join_time": args[1]}
			case "cancel":
				method = "DELETE"
				path = "/calendar/scheduled_bots/" + url.PathEscape(args[0])
			}
			return requestJSON(cmd, c, method, path, body)
		})
		sub.Short = shortFor(action) + " calendar data"
		cal.AddCommand(sub)
	}
	var name string
	var video bool
	auto := botAction("auto-join <on|off|enable|disable>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		mode := map[string]string{"on": "enable", "off": "disable", "enable": "enable", "disable": "disable"}[args[0]]
		if mode == "" {
			return nil, usageErr("auto-join expects on, off, enable or disable", "")
		}
		cfg := map[string]any{"bot_name": name, "audio_required": true, "video_required": video, "automatic_leave": map[string]any{"everyone_left_timeout": 60}}
		return requestJSON(cmd, c, "POST", "/calendar/auto-schedule/"+mode, map[string]any{"default_bot_config": cfg})
	})
	auto.Short = "Enable or disable auto-join for every calendar meeting"
	auto.Flags().StringVarP(&name, "name", "n", "Notetaker", "default bot name")
	auto.Flags().BoolVar(&video, "video", false, "record video")
	cal.AddCommand(auto)
	return cal
}
