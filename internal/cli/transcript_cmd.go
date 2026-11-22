package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/meetstream-ai/cli/internal/ui"
	"github.com/spf13/cobra"
)

func transcriptText(v any) string {
	items, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok {
			return s
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		return string(b)
	}
	lines := []string{}
	str := func(v any) string { s, _ := v.(string); return strings.TrimSpace(s) }
	for _, it := range items {
		m := object(it)
		speaker := str(object(m["participant"])["name"])
		if speaker == "" {
			speaker = str(m["speaker"])
		}
		if speaker == "" {
			speaker = "Unknown"
		}
		words, _ := m["words"].([]any)
		parts := []string{}
		for _, w := range words {
			wm := object(w)
			var v any
			for _, k := range []string{"text", "punctuated_word", "word"} {
				if wm[k] != nil {
					v = wm[k]
					break
				}
			}
			if s := str(v); s != "" {
				parts = append(parts, s)
			}
		}
		line := strings.Join(parts, " ")
		if line == "" {
			line = str(m["transcript"])
		}
		if line == "" {
			line = str(m["text"])
		}
		if line != "" {
			lines = append(lines, speaker+": "+line)
		}
	}
	return strings.Join(lines, "\n")
}
func newTranscriptCmd() *cobra.Command {
	var wait, raw bool
	var timeout float64
	cmd := &cobra.Command{Use: "transcript <bot-id>", Short: "Print a meeting transcript", GroupID: "meet", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		res, err := c.GetTranscript(cmd.Context(), args[0], api.TranscriptOpt{Wait: wait, Raw: raw, Timeout: time.Duration(timeout * float64(time.Second))})
		if failed := api.FailedTranscript(err); failed != nil {
			if e := emit(map[string]any{"error": "transcript_failed", "reason": failed.Reason, "transcript_status": failed.TranscriptStatus, "transcript_id": failed.TranscriptID}, func() {
				ui.Fail("Transcript failed: " + failed.Reason)
				ui.Note("It will not become ready. Fix the cause, then run: meetstream transcribe " + args[0])
			}); e != nil {
				return e
			}
			return &ExitError{Code: 4, Message: "Transcript failed: " + failed.Reason, Silent: true}
		}
		if err != nil {
			return apiError(err)
		}
		if res.Transcript == nil {
			return &ExitError{Code: 3, Message: "Transcript not ready yet", Hint: "meetstream transcript " + args[0] + " --wait"}
		}
		if raw && !g.JSON {
			return json.NewEncoder(os.Stdout).Encode(res.Transcript)
		}
		return emit(res.Transcript, func() { fmt.Fprintln(ui.Out, transcriptText(res.Transcript)) })
	}}
	cmd.Flags().BoolVarP(&wait, "wait", "w", false, "poll until ready")
	cmd.Flags().BoolVar(&raw, "raw", false, "raw provider output")
	cmd.Flags().Float64Var(&timeout, "timeout", 600, "maximum wait seconds")
	return cmd
}
func newTranscriptionsCmd() *cobra.Command {
	cmd := botAction("transcriptions <bot-id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		return c.Transcriptions(cmd.Context(), args[0])
	})
	cmd.GroupID = "meet"
	return cmd
}
func newTranscribeCmd() *cobra.Command {
	var provider, language, callback string
	cmd := botAction("transcribe <bot-id>", 1, func(cmd *cobra.Command, c *api.Client, args []string) (any, error) {
		return c.Transcribe(cmd.Context(), args[0], provider, language, callback)
	})
	cmd.GroupID = "meet"
	cmd.Flags().StringVarP(&provider, "provider", "p", "deepgram", "transcription provider")
	cmd.Flags().StringVarP(&language, "language", "l", "", "language")
	cmd.Flags().StringVarP(&callback, "callback", "c", "", "callback URL")
	return cmd
}
