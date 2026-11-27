package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/ui"
)

// applyTranscriptionFlags turns --transcription / --live-transcription into the
// create_bot fields (--live-transcription sends transcription_mode sentence
// unless --transcription-mode says otherwise), and fills the callback from a running `meetstream listen`
// when -c is not given. The old flags (-t, --live-transcript-webhook) keep
// working unchanged.
func applyTranscriptionFlags(o *api.CreateOpts, transcription, live string) error {
	if transcription != "" && o.Transcript != "" && !strings.EqualFold(transcription, o.Transcript) {
		return usageErr("--transcription and -t name different providers", "use one: --transcription "+transcription)
	}
	if transcription != "" && live != "" {
		return usageErr("--transcription and --live-transcription cannot be used together",
			"a bot runs one provider. For a post-call transcript of a live bot, run after the meeting: meetstream transcribe <bot-id> --transcription "+transcription)
	}
	if transcription != "" {
		name, known := postCallProvider(transcription)
		if isStreamingOnly(name) && name != "meeting_captions" {
			return usageErr(name+" is a live provider", "use --live-transcription "+strings.TrimSuffix(name, "_streaming"))
		}
		if !known {
			fmt.Fprintln(os.Stderr, "⚠ "+name+" is not in this CLI's provider list; sending it to the API as given (meetstream transcription providers).")
		}
		o.Transcript = name
	}
	if o.TranscriptionMode != "" {
		o.TranscriptionMode = strings.ToLower(strings.TrimSpace(o.TranscriptionMode))
		if !api.TranscriptionModes[o.TranscriptionMode] {
			return usageErr("--transcription-mode must be raw, sentence or word_level", "sentence (the default) sends one POST per finished sentence")
		}
		if live == "" && !strings.HasSuffix(o.Transcript, "_streaming") {
			return usageErr("--transcription-mode needs --live-transcription", "meetstream join <meeting-url> --live-transcription meetstream --transcription-mode "+o.TranscriptionMode)
		}
	}
	session := loadListenSession()
	hook := session.callbackURL(config.APIURL())
	if live != "" {
		mapped, err := liveProvider(live)
		if err != nil {
			return err
		}
		if o.Transcript != "" && o.Transcript != mapped {
			return usageErr("-t and --live-transcription name different providers", "use only --live-transcription "+live)
		}
		o.Transcript = mapped
		if o.TranscriptionMode == "" {
			// sentence: one POST per finished sentence. The API default (raw) also sends interim results.
			o.TranscriptionMode = "sentence"
		}
		if o.LiveTranscriptWebhook == "" {
			if hook == "" {
				return usageErr("live transcription needs a webhook the API can reach",
					"start one in another terminal: meetstream listen --tunnel cloudflared   (or pass --live-transcript-webhook https://...)")
			}
			o.LiveTranscriptWebhook = hook
			fmt.Fprintln(os.Stderr, ui.DimStyle.Render("  Live transcript → your listen session ("+hook+")"))
		}
	}
	if o.Callback == "" && hook != "" {
		o.Callback = hook
		fmt.Fprintln(os.Stderr, ui.DimStyle.Render("  Bot events → your listen session ("+hook+")"))
	}
	return nil
}

// statusWords are bot statuses in plain words for --follow.
var statusWords = map[string]string{
	"joining": "joining", "inwaitingroom": "waiting room — admit the bot in the meeting", "inmeeting": "in the meeting",
	"recording": "recording", "leaving": "leaving", "stopped": "left the meeting", "notallowed": "not admitted (waiting room timeout)",
	"denied": "the host refused entry", "failed": "failed", "error": "failed",
	"kicked": "removed from the meeting", "cancelled": "cancelled before it joined", "mediaexpired": "the recording expired",
}

// followBot prints status changes until the bot ends, then waits for the
// transcript. With --json it prints one JSON object per line (NDJSON).
func followBot(cmd *cobra.Command, c *api.Client, created any, o api.CreateOpts, replay bool) error {
	id := pickStr(object(created), "bot_id", "BotID", "id")
	if id == "" {
		return resultView(created, nil, func(v any) string { return renderJoinResult(v, o.Name, o.MeetingLink, replay) })
	}
	say := func(event map[string]any, human string) {
		if g.JSON {
			if event != nil {
				emitLine(event)
			}
		} else if human != "" {
			fmt.Fprintln(ui.Out, human)
		}
	}
	clock := func() string { return ui.DimStyle.Render("  " + time.Now().Format("15:04:05") + "  ") }
	say(map[string]any{"event": "sent", "bot_id": id, "replay": replay},
		ui.TitleStyle.Render("◆")+" Bot "+ui.BoldStyle.Render(id)+" sent to "+orDefault(platformName("", o.MeetingLink), "the meeting"))
	interval := 5 * time.Second
	if isLocalAPI(config.APIURL()) {
		interval = 500 * time.Millisecond
	}
	last, final := "", ""
	for {
		v, err := c.BotStatus(cmd.Context(), id)
		if err != nil {
			return apiError(err)
		}
		st := text(object(v)["status"])
		if st != last && st != "" {
			last = st
			words := statusWords[strings.ToLower(st)]
			if words == "" {
				words = st
			}
			say(map[string]any{"event": "status", "bot_id": id, "status": st}, clock()+fmt.Sprintf("%-14s", st)+" "+ui.DimStyle.Render(words))
		}
		if terminalStatus.MatchString(st) {
			final = st
			break
		}
		select {
		case <-cmd.Context().Done():
			return &ExitError{Code: 130, Message: "interrupted", Silent: true}
		case <-time.After(interval):
		}
	}
	ended := strings.ToLower(final)
	if ended != "stopped" && ended != "done" && ended != "kicked" {
		say(map[string]any{"event": "ended", "bot_id": id, "status": final}, "")
		return &ExitError{Code: 1, Message: "the bot ended without recording: " + statusWords[ended], Hint: "meetstream bots get " + id + " --part detail"}
	}
	switch {
	case o.Transcript == "":
		say(map[string]any{"event": "done", "bot_id": id, "transcript": "not requested"},
			ui.OKLine("Done. No transcript was requested; make one: "+ui.CmdStyle.Render("meetstream transcribe "+id+" --transcription meetstream")))
		return nil
	case isStreamingOnly(o.Transcript):
		say(map[string]any{"event": "done", "bot_id": id, "transcript": "live only"},
			ui.OKLine("Done. A live provider makes no post-call transcript; make one: "+ui.CmdStyle.Render("meetstream transcribe "+id+" --transcription meetstream")))
		return nil
	}
	say(nil, clock()+fmt.Sprintf("%-14s", "transcript")+" "+ui.DimStyle.Render("waiting for "+o.Transcript+" …"))
	res, err := c.GetTranscript(cmd.Context(), id, api.TranscriptOpt{Wait: true, Timeout: 15 * time.Minute, Interval: interval})
	if failed := api.FailedTranscript(err); failed != nil {
		say(map[string]any{"event": "transcript_failed", "bot_id": id, "reason": failed.Reason}, "")
		return &ExitError{Code: 4, Message: "Transcript failed: " + failed.Reason, Hint: "retry with another provider: meetstream transcribe " + id + " --transcription meetstream"}
	}
	if err != nil {
		return apiError(err)
	}
	n := 0
	if arr, ok := res.Transcript.([]any); ok {
		n = len(arr)
	}
	say(map[string]any{"event": "transcript_ready", "bot_id": id, "segments": n, "next": "meetstream transcript " + id + " --json"},
		ui.OKLine(fmt.Sprintf("Transcript ready (%s, %d segments). Read it: %s", o.Transcript, n, ui.CmdStyle.Render("meetstream transcript "+id))))
	return nil
}
