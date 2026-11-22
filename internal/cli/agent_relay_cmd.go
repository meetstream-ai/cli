package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/agentkit/relay"
	"github.com/meetstream-ai/cli/internal/login"
	"github.com/meetstream-ai/cli/internal/ui"
)

// `meetstream agent login|logout|status`: this computer's sign-in for the
// MeetStream relay (MS-2366). No API key and no pairing token: the relay
// knows this computer by its device grant.

const relayRevokeTimeout = 10 * time.Second

func newAgentLoginCmd(info BuildInfo) *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Connect this computer to MeetStream so MIA can reach your agent (no tunnel)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store := relay.NewStore()
			opts := relay.LoginOptions{
				ShowURL: func(u string) {
					if g.JSON {
						fmt.Fprintln(os.Stderr, "Open this link to sign in: "+u)
						return
					}
					ui.Header(info.Version, "Connect this computer to MeetStream")
					fmt.Fprintln(ui.Out)
					ui.Note("If the browser doesn't open, open this link:")
					ui.Command(u)
					fmt.Fprintln(ui.Out)
				},
				OpenBrowser: login.Open,
			}
			if noBrowser {
				opts.OpenBrowser = nil
			}
			t, err := relay.Login(cmd.Context(), opts)
			if err != nil {
				return &ExitError{Code: 1, Message: err.Error(), Hint: "the relay is on for test stacks first; set MEETSTREAM_AUTH_URL and MEETSTREAM_RELAY_URL to use one"}
			}
			creds := &relay.Credentials{DeviceID: t.DeviceID, RefreshToken: t.RefreshToken, AuthURL: relay.AuthURL(), RelayURL: relay.RelayURL()}
			if err := store.Save(creds); err != nil {
				return err
			}
			return emit(map[string]any{"signed_in": true, "device_id": creds.DeviceID, "device_name": relay.DeviceName(), "store": store.Where()}, func() {
				ui.OK("This computer is connected as " + ui.BoldStyle.Render(relay.DeviceName()) + " (" + creds.DeviceID + ")")
				ui.Note("Sign-in saved in " + store.Where() + ".")
				fmt.Fprintln(ui.Out)
				ui.Note("Next:")
				ui.Command("meetstream agent serve")
			})
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the sign-in link instead of opening a browser")
	return cmd
}

func newAgentLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use: "logout", Short: "Disconnect this computer from MeetStream", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store := relay.NewStore()
			creds, err := store.Load()
			if err != nil {
				return err
			}
			if creds != nil {
				ctx, cancel := context.WithTimeout(cmd.Context(), relayRevokeTimeout)
				relay.Revoke(ctx, creds.AuthURL, creds.RefreshToken)
				cancel()
			}
			store.Delete()
			return emit(map[string]any{"signed_out": creds != nil}, func() {
				if creds != nil {
					ui.OK("This computer is disconnected from MeetStream.")
				} else {
					ui.Note("This computer was not connected.")
				}
			})
		},
	}
}

func newAgentStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Show whether this computer is connected to MeetStream", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store := relay.NewStore()
			creds, err := store.Load()
			if err != nil {
				return err
			}
			out := map[string]any{"signed_in": creds != nil, "store": store.Where()}
			if creds != nil {
				out["device_id"], out["auth_url"], out["relay_url"], out["signed_in_at"] = creds.DeviceID, creds.AuthURL, creds.RelayURL, creds.SignedInAt
			}
			if err := emit(out, func() {
				if creds == nil {
					ui.Fail("This computer is not connected. Run " + ui.CmdStyle.Render("meetstream agent login"))
					return
				}
				ui.Box("Connected to MeetStream", [][2]string{{"device", relay.DeviceName() + " (" + creds.DeviceID + ")"}, {"relay", creds.RelayURL}, {"since", orDash(creds.SignedInAt)}, {"stored in", store.Where()}})
			}); err != nil {
				return err
			}
			if creds == nil {
				return &ExitError{Code: 2, Message: "not connected", Hint: "run: meetstream agent login"}
			}
			return nil
		},
	}
}
