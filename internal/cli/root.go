// Package cli is the meetstream command tree.
//
// Conventions on every command (CLI-Anything):
//   - --json      machine output on stdout; human text goes to stderr
//   - --no-input  never prompt; fail with exit 2 and a hint instead
//   - exit codes  0 ok, 1 runtime/API error, 2 usage or missing auth,
//     3 not ready / not available, 4 transcript failed, 130 interrupted
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/telemetry"
	"github.com/meetstream-ai/cli/internal/ui"
)

type BuildInfo struct{ Version, Commit, Date string }

// Global flags, shared by every command.
type globals struct {
	JSON    bool
	NoInput bool
	NoColor bool
}

var g globals

// ExitError carries an exit code and an optional hint for the user.
type ExitError struct {
	Code    int
	Message string
	Hint    string
	Silent  bool // The command already emitted its result; do not print a second error.
}

func (e *ExitError) Error() string { return e.Message }

func usageErr(msg, hint string) error { return &ExitError{Code: 2, Message: msg, Hint: hint} }

// emit prints v as JSON on stdout when --json is set, else calls human().
func emit(v any, human func()) error {
	if g.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	human()
	return nil
}

func NewRoot(info BuildInfo) *cobra.Command {
	root := &cobra.Command{
		Use:   "meetstream",
		Short: "MeetStream from your terminal: meeting bots, transcripts and your own agent in the call",
		Long: ui.TitleStyle.Render("◆ MeetStream") + " " + ui.DimStyle.Render("v"+info.Version) + `
Send bots to Zoom, Google Meet and Microsoft Teams, read transcripts,
and bring your own agent CLI into the meeting.

Docs: https://docs.meetstream.ai   Keys: https://app.meetstream.ai/api-keys`,
		Version:       info.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			ui.Setup(g.NoColor, g.JSON)
			ui.Out = os.Stderr
			// Anonymous: the command name and version only (MEETSTREAM_TELEMETRY=0 turns it off).
			path := strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()))
			if path == "" {
				path = "home"
			}
			telemetryWait = telemetry.Track("cli_command_run", map[string]any{"command": cmd.Name(), "command_path": path, "version": info.Version}, 800*time.Millisecond)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.JSON || g.NoInput || !ui.Interactive() {
				return cmd.Help()
			}
			return runHome(cmd, info)
		},
	}
	root.SetVersionTemplate("meetstream {{.Version}}\n")
	pf := root.PersistentFlags()
	pf.BoolVar(&g.JSON, "json", false, "machine-readable JSON on stdout")
	pf.BoolVar(&g.NoInput, "no-input", false, "never prompt; fail instead (for scripts and agents)")
	pf.BoolVar(&g.NoColor, "no-color", false, "plain output without colour (also NO_COLOR=1)")

	root.AddGroup(
		&cobra.Group{ID: "start", Title: "Get started:"},
		&cobra.Group{ID: "meet", Title: "Meetings:"},
		&cobra.Group{ID: "agent", Title: "Your agent in the call:"},
		&cobra.Group{ID: "tool", Title: "Tools:"},
	)

	root.AddCommand(
		newLoginCmd(info), newAuthCmd(info), newInitCmd(), newEnvCmd(),
		newJoinCmd(), newTranscriptionCmd(), newBotsCmd(), newLegacyBotCmd(), newTranscriptionsCmd(), newTranscribeCmd(), newTranscriptCmd(), newRecordingsCmd(), newBalanceCmd(), newMiaCmd(),
		newAgentCmd(info), newSetupShortcut(info), newBridgeCmd(info),
		newLoginsCmd(), newCalendarCmd(), newListenCmd(), newTriggerCmd(), newMockCmd(), newMCPCmd(), newAICmd(), newCommandsCmd(), newAPICmd(),
		newDoctorCmd(info), newDocsCmd(), newSkillCmd(), newVersionCmd(info),
	)
	root.SetHelpCommandGroupID("tool")
	root.SetCompletionCommandGroupID("tool")
	return root
}

// telemetryWait waits (briefly) for the usage event sent at command start.
var telemetryWait = func() {}

// Execute runs the CLI and returns the process exit code.
func Execute(info BuildInfo) int {
	root := NewRoot(info)
	err := root.Execute()
	telemetryWait()
	if err == nil {
		return 0
	}
	code, hint := 1, ""
	var ee *ExitError
	if errors.As(err, &ee) {
		code, hint = ee.Code, ee.Hint
		if ee.Silent {
			return code
		}
	} else if isUsageError(err) {
		code = 2
	}
	if g.JSON {
		out := map[string]any{"error": map[string]any{"message": err.Error(), "exit_code": code}}
		if hint != "" {
			out["error"].(map[string]any)["hint"] = hint
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(os.Stdout, string(b))
		return code
	}
	ui.Out = os.Stderr
	ui.Fail(err.Error())
	if hint != "" {
		ui.Note(hint)
	}
	return code
}

func isUsageError(err error) bool {
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "accepts ", "requires ", "requires at least", "required flag"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}
