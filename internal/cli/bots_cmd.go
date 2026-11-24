package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/ui"
	"github.com/spf13/cobra"
)

func apiClient(cmd *cobra.Command) (*api.Client, error) {
	key, _ := config.ResolveKey()
	if key == "" {
		return nil, usageErr("not signed in", "run: meetstream login")
	}
	return api.New(config.APIURL(), key, cmd.Root().Version), nil
}
func apiError(err error) error {
	if err == nil {
		return nil
	}
	var ue *api.UsageError
	if errors.As(err, &ue) {
		return usageErr(ue.Error(), "")
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		if ae.Status == 401 || ae.Status == 403 {
			return usageErr(ae.Detail(), "run: meetstream login")
		}
		return &ExitError{Code: 1, Message: ae.Detail()}
	}
	return err
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func text(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func first(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := text(m[k]); s != "" {
			return s
		}
	}
	return ""
}

// humanData prints any API answer as plain rows and tables (never JSON).
func humanData(title string, v any) { fmt.Fprintln(ui.Out, renderData(title, v, ui.Width())) }

// viewFor is the human view of one command's answer, by command path.
type viewFunc func(v any) string

func resultView(v any, err error, view viewFunc) error {
	if err != nil {
		return apiError(err)
	}
	return emit(v, func() { fmt.Fprintln(ui.Out, view(v)) })
}

func result(title string, v any, err error) error {
	if err != nil {
		return apiError(err)
	}
	return emit(v, func() { humanData(title, v) })
}
func confirmDelete(cmd *cobra.Command, yes bool) (bool, error) {
	if yes {
		return true, nil
	}
	if g.NoInput {
		return false, usageErr("refusing to delete without confirmation", "pass --yes")
	}
	fmt.Fprint(os.Stderr, "Permanently delete? y/N: ")
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return false, usageErr("confirmation required", "pass --yes")
	}
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}
func attrs(items []string, flag string) (map[string]string, error) {
	m := map[string]string{}
	for _, item := range items {
		k, v, ok := strings.Cut(item, "=")
		if !ok {
			return nil, usageErr(flag+" expects key=value", "")
		}
		m[k] = v
	}
	return m, nil
}
func newJoinCmd() *cobra.Command { return createCmd("join") }
func createCmd(name string) *cobra.Command {
	var o api.CreateOpts
	var attr, param []string
	var idem, agentAlias, storage string
	var own, noOwn, noStrict, follow bool
	var transcription, liveTranscription string
	cmd := &cobra.Command{Use: name + " <meeting-url>", Short: "Send a bot to a meeting", GroupID: "meet", Args: cobra.ExactArgs(1)}
	f := cmd.Flags()
	f.StringVarP(&o.Name, "name", "n", "MeetStream Bot", "bot display name")
	f.BoolVar(&o.Video, "video", false, "record video")
	f.StringVar(&o.VideoLayout, "video-layout", "", "mixed video layout")
	f.StringVar(&transcription, "transcription", "", "transcript after the call: meetstream, deepgram, assemblyai, ... (meetstream transcription providers)")
	f.StringVar(&liveTranscription, "live-transcription", "", "live transcript while the meeting runs: meetstream, deepgram, assemblyai, smallest")
	f.BoolVar(&follow, "follow", false, "stay and print each status change, then wait for the transcript")
	f.StringVarP(&o.Transcript, "transcript", "t", "", "same as --transcription (raw API provider name)")
	f.StringVarP(&o.Language, "language", "l", "", "transcription language")
	f.StringVarP(&o.Callback, "callback", "c", "", "lifecycle webhook")
	f.StringVar(&o.JoinAt, "join-at", "", "join at")
	f.StringVar(&o.BotMessage, "bot-message", "", "bot message")
	f.StringVar(&o.ImageURL, "image-url", "", "image url")
	f.StringVar(&o.RetentionHours, "retention-hours", "", "retention hours")
	f.StringVar(&o.ZoomZakURL, "zoom-zak-url", "", "zoom zak url")
	f.StringVar(&o.ZoomObfURL, "zoom-obf-url", "", "zoom obf url")
	f.StringVar(&o.GoogleLoginDomain, "google-login-domain", "", "google login domain")
	f.StringVar(&o.TeamsLoginDomain, "teams-login-domain", "", "teams login domain")
	f.StringVar(&o.SignInEmail, "sign-in-email", "", "sign in email")
	f.StringVar(&o.AgentConfigID, "agent-config", "", "agent config")
	f.StringVar(&o.LiveTranscriptWebhook, "live-transcript-webhook", "", "live transcript webhook")
	f.StringVar(&o.LiveAudioWs, "live-audio-ws", "", "live audio ws")
	f.StringVar(&o.LiveVideoWs, "live-video-ws", "", "live video ws")
	f.StringVar(&o.SocketWs, "socket-ws", "", "socket ws")
	f.BoolVar(&o.SeparateAudio, "separate-audio", false, "separate-audio")
	f.BoolVar(&o.SeparateVideo, "separate-video", false, "separate-video")
	f.BoolVar(&o.ZoomObf, "zoom-obf", false, "zoom-obf")
	f.StringVar(&agentAlias, "agent-config-id", "", "same as --agent-config")
	_ = f.MarkHidden("agent-config-id")
	f.StringVar(&storage, "storage-id", "", "storage destination")
	f.BoolVar(&own, "zoom-use-own-app", false, "use your Zoom app")
	f.BoolVar(&noOwn, "no-zoom-use-own-app", false, "disable your Zoom app")
	f.BoolVar(&noStrict, "no-strict-email", false, "allow another account")
	f.StringArrayVar(&attr, "attr", nil, "custom attribute key=value")
	f.StringArrayVar(&param, "agent-param", nil, "agent parameter key=value")
	f.StringVar(&idem, "idempotency-key", "", "safe retry key")
	everyone, waiting, max := 60.0, 300.0, 14400.0
	f.Float64Var(&everyone, "everyone-left-timeout", 60, "empty meeting timeout")
	f.Float64Var(&waiting, "waiting-room-timeout", 300, "waiting room timeout")
	f.Float64Var(&max, "max-recording-seconds", 14400, "recording timeout")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		o.MeetingLink = args[0]
		o.EveryoneLeftTimeout = &everyone
		o.WaitingRoomTimeout = &waiting
		o.MaxRecordingSeconds = &max
		if f.Changed("storage-id") {
			o.StorageID = &storage
		}
		if f.Changed("zoom-use-own-app") {
			o.ZoomUseOwnApp = &own
		}
		if noOwn {
			v := false
			o.ZoomUseOwnApp = &v
		}
		if noStrict {
			v := false
			o.StrictEmail = &v
		}
		if o.AgentConfigID == "" {
			o.AgentConfigID = agentAlias
		}
		if err := applyTranscriptionFlags(&o, transcription, liveTranscription); err != nil {
			return err
		}
		o.Attrs, err = attrs(attr, "--attr")
		if err != nil {
			return err
		}
		o.AgentConfigParams, err = attrs(param, "--agent-param")
		if err != nil {
			return err
		}
		payload, err := api.BuildCreateBotPayload(o)
		if err != nil {
			return apiError(err)
		}
		if o.AgentConfigID != "" && (o.SocketWs != "" || o.LiveAudioWs != "") {
			fmt.Fprintln(os.Stderr, "⚠ A voice agent needs only --agent-config. --socket-ws / --live-audio-ws are for your own bridge and are the usual cause of a silent agent.")
		}
		for _, w := range api.SignedInWarnings(o.MeetingLink, o.GoogleLoginDomain, o.TeamsLoginDomain) {
			fmt.Fprintln(os.Stderr, "⚠ "+w)
		}
		status, v, err := c.CreateBot(cmd.Context(), payload, idem)
		if follow && err == nil {
			return followBot(cmd, c, v, o, status == 507)
		}
		return resultView(v, err, func(v any) string { return renderJoinResult(v, o.Name, o.MeetingLink, status == 507) })
	}
	// Commander accepts multiple key=value entries after these flags.
	runCreate := cmd.RunE
	cmd.DisableFlagParsing = true
	cmd.Args = nil
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		normalized := []string{}
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if arg == "--no-zoom-use-own-app" {
				arg = "--zoom-use-own-app=false"
			}
			normalized = append(normalized, arg)
			if arg == "--" {
				normalized = append(normalized, args[i+1:]...)
				break
			}
			if arg != "--attr" && arg != "--agent-param" {
				continue
			}
			if i+1 >= len(args) {
				continue
			}
			i++
			normalized = append(normalized, args[i])
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				normalized = append(normalized, arg, args[i])
			}
		}
		cmd.DisableFlagParsing = false
		err := cmd.ParseFlags(normalized)
		cmd.DisableFlagParsing = true
		if err != nil {
			return usageErr(err.Error(), "")
		}
		help, _ := cmd.Flags().GetBool("help")
		if help {
			return cmd.Help()
		}
		if err := cobra.ExactArgs(1)(cmd, cmd.Flags().Args()); err != nil {
			return usageErr(err.Error(), "")
		}
		ui.Setup(g.NoColor, g.JSON)
		ui.Out = os.Stderr
		return runCreate(cmd, cmd.Flags().Args())
	}
	return cmd
}

type botGet func(context.Context, string) (any, error)

func botPart(c *api.Client, part string) botGet {
	return map[string]botGet{"detail": c.BotDetail, "audio": c.BotAudio, "video": c.BotVideo, "streams": c.RecordingStreams, "audio-streams": c.AudioStreams, "chats": c.Chats, "screenshots": c.Screenshots, "participants": c.Participants}[part]
}
func botAction(use string, n int, action func(*cobra.Command, *api.Client, []string) (any, error)) *cobra.Command {
	return &cobra.Command{Use: use, Short: shortFor(strings.Fields(use)[0]), Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		v, err := action(cmd, c, args)
		return resultView(v, err, func(v any) string { return humanView(cmd, args, v) })
	}}
}

var terminalStatus = regexp.MustCompile("(?i)stopped|done|error|failed|notallowed|denied")

func newBotsCmd() *cobra.Command      { return botsCmd(false) }
func newLegacyBotCmd() *cobra.Command { return botsCmd(true) }
func botsCmd(legacy bool) *cobra.Command {
	name := "bots"
	if legacy {
		name = "bot"
	}
	cmd := &cobra.Command{Use: name, Short: "Manage meeting bots", GroupID: "meet", Hidden: legacy}
	list := newBotsListCmd()
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{Use: "tail [id]", Short: "Planned: live bot events over the relay", Args: cobra.MaximumNArgs(1), RunE: func(*cobra.Command, []string) error {
		return usageErr("bots tail is planned", "use meetstream listen with a tunnel until the relay carries events")
	}})
	var watch bool
	status := &cobra.Command{Use: "status <id>", Short: "Show bot status", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		for {
			v, err := c.BotStatus(cmd.Context(), args[0])
			if err != nil {
				return apiError(err)
			}
			if err := emit(v, func() { fmt.Fprintln(ui.Out, renderBotStatus(v, time.Now().Format("15:04:05"))) }); err != nil {
				return err
			}
			if !watch || terminalStatus.MatchString(text(object(v)["status"])) {
				return nil
			}
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-time.After(5 * time.Second):
			}
		}
	}}
	status.Flags().BoolVarP(&watch, "watch", "w", false, "poll every 5 seconds")
	cmd.AddCommand(status)
	if legacy {
		create := createCmd("create")
		create.GroupID = ""
		cmd.AddCommand(create)
		for _, part := range []string{"detail", "audio", "video", "streams", "audio-streams", "chats", "screenshots", "participants"} {
			cmd.AddCommand(botAction(part+" <id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
				return botPart(c, part)(cmd.Context(), args[0])
			}))
		}
	} else {
		var part string
		get := botAction("get <id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
			fn := botPart(c, part)
			if fn == nil {
				return nil, usageErr("unknown part: "+part, "")
			}
			return fn(cmd.Context(), args[0])
		})
		get.Flags().StringVar(&part, "part", "detail", "detail|audio|video|streams|audio-streams|chats|screenshots|participants")
		cmd.AddCommand(get)
	}
	var download bool
	timeline := botAction("timeline <id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		v, err := c.SpeakerTimeline(cmd.Context(), args[0])
		if err != nil {
			return nil, err
		}
		u := text(object(v)["speaker_timeline_url"])
		if u != "" {
			if download {
				return c.DownloadJSON(cmd.Context(), u)
			}
			size := text(object(v)["size_bytes"])
			if size == "" {
				size = "too many"
			}
			fmt.Fprintf(os.Stderr, "⚠ The timeline is %s bytes, so the API returned a download link (valid 1 hour). Run again with --download to fetch it.\n", size)
		}
		return v, nil
	})
	timeline.Flags().BoolVar(&download, "download", false, "fetch timeline download")
	cmd.AddCommand(timeline)
	cmd.AddCommand(&cobra.Command{Use: "summary <id>", Short: "Fetch transcript to summarize", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		return usageErr("MeetStream has no meeting summary endpoint. Fetch the transcript and summarize it: meetstream transcript "+args[0], "")
	}})
	cmd.AddCommand(botAction("remove <id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		return c.RemoveBot(cmd.Context(), args[0])
	}))
	var yes bool
	del := botAction("delete <id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		ok, err := confirmDelete(cmd, yes)
		if err != nil {
			return nil, err
		}
		if !ok {
			return map[string]any{"deleted": false}, nil
		}
		return c.DeleteBotData(cmd.Context(), args[0])
	})
	del.Flags().BoolVar(&yes, "yes", false, "skip confirmation")
	cmd.AddCommand(del)
	sayName, imageName := "say", "show-image"
	if legacy {
		sayName, imageName = "send-message", "send-image"
	}
	say := botAction(sayName+" <id> <message...>", 2, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		return c.SendMessage(cmd.Context(), args[0], strings.Join(args[1:], " "))
	})
	say.Args = cobra.MinimumNArgs(2)
	cmd.AddCommand(say)
	var duration float64
	image := botAction(imageName+" <id> <img-url>", 2, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		return c.SendImage(cmd.Context(), args[0], args[1], duration)
	})
	image.Flags().Float64VarP(&duration, "duration", "d", 0, "display duration in seconds")
	cmd.AddCommand(image)
	if legacy {
		for _, pair := range [][2]string{{"raise-hand", "raise_hand"}, {"lower-hand", "lower_hand"}} {
			cmd.AddCommand(botAction(pair[0]+" <id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
				return c.SendCommand(cmd.Context(), args[0], pair[1])
			}))
		}
	} else {
		cmd.AddCommand(botAction("hand <id> <up|down>", 2, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
			action := map[string]string{"up": "raise_hand", "down": "lower_hand"}[args[1]]
			if action == "" {
				return nil, usageErr("hand expects up or down", "")
			}
			return c.SendCommand(cmd.Context(), args[0], action)
		}))
	}
	return cmd
}
func newRecordingsCmd() *cobra.Command {
	cmd := botAction("recordings <id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		out := map[string]any{"bot_id": args[0]}
		for _, part := range []string{"audio", "video", "streams", "audio-streams"} {
			v, err := botPart(c, part)(cmd.Context(), args[0])
			if err != nil {
				var ae *api.Error
				if (part == "streams" || part == "audio-streams") && errors.As(err, &ae) && ae.Status == 404 {
					continue
				}
				return nil, err
			}
			key := strings.ReplaceAll(part, "-", "_")
			out[key] = v
		}
		return out, nil
	})
	cmd.GroupID = "meet"
	return cmd
}

// shorts are the help lines, worded as in the Node CLI.
var shorts = map[string]string{
	"list":           "List your bots",
	"get":            "Show a bot's details or one part of its data",
	"detail":         "Bot details",
	"audio":          "Audio recording link",
	"video":          "Video recording link",
	"streams":        "Per-participant video streams (needs --separate-video at create)",
	"audio-streams":  "Per-participant audio streams (needs --separate-audio at create)",
	"chats":          "In-meeting chat messages",
	"screenshots":    "Screenshots captured during the meeting",
	"participants":   "Participant list",
	"timeline":       "Speaker timeline (--download fetches the JSON file)",
	"remove":         "Make the bot leave the meeting",
	"delete":         "Delete a bot's stored data (asks first; --yes to skip)",
	"send-message":   "Post a chat message from the bot",
	"say":            "Post a chat message from the bot",
	"send-image":     "Show an image or GIF as the bot's video",
	"show-image":     "Show an image or GIF as the bot's video",
	"raise-hand":     "Raise the bot's hand in the live meeting",
	"lower-hand":     "Lower the bot's hand in the live meeting",
	"hand":           "Raise or lower the bot's hand (up|down)",
	"recordings":     "Audio and video recording links",
	"transcribe":     "Start a new transcription of a finished meeting",
	"transcriptions": "List a bot's transcription runs",
}

func shortFor(name string) string {
	if s, ok := shorts[name]; ok {
		return s
	}
	return name
}
