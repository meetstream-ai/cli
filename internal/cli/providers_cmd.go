package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/ui"
)

// transcriptionProvider is one choice for --transcription or --live-transcription.
type transcriptionProvider struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Key: "none" (MeetStream supplies it) or "dashboard" (the user's own key, saved in the dashboard).
	Key string `json:"key"`
	// MapsTo is the provider name the API takes, when it differs from ID (live mode).
	MapsTo string `json:"maps_to,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Providers the API accepts today (internal/api createProvider). This list is
// built in until the API serves one (plan decision D2). Defaults are not
// changed here: plan decision D1 belongs to Nav.
var postCallProviders = []transcriptionProvider{
	{ID: "meetstream", Label: "MeetStream", Key: "none", Note: "in-house; per-hour add-on"},
	{ID: "deepgram", Label: "Deepgram", Key: "dashboard"},
	{ID: "assemblyai", Label: "AssemblyAI", Key: "dashboard"},
	{ID: "sarvam", Label: "Sarvam", Key: "dashboard", Note: "Indian languages"},
	{ID: "smallest", Label: "Smallest", Key: "dashboard"},
	{ID: "jigsawstack", Label: "Interfaze (JigsawStack)", Key: "dashboard"},
	{ID: "meeting_captions", Label: "Meeting captions", Key: "none", Note: "the platform's own captions"},
}

var liveProviders = []transcriptionProvider{
	{ID: "meetstream", Label: "MeetStream", Key: "none", MapsTo: "meetstream_streaming"},
	{ID: "deepgram", Label: "Deepgram", Key: "dashboard", MapsTo: "deepgram_streaming"},
	{ID: "assemblyai", Label: "AssemblyAI", Key: "dashboard", MapsTo: "assemblyai_streaming"},
	{ID: "smallest", Label: "Smallest", Key: "dashboard", MapsTo: "smallest_streaming"},
}

func providerIDs(list []transcriptionProvider) []string {
	var out []string
	for _, p := range list {
		out = append(out, p.ID)
	}
	return out
}

// livesProvider maps a --live-transcription value to the API's streaming name.
// It also accepts the API name itself (meetstream_streaming).
func liveProvider(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, p := range liveProviders {
		if v == p.ID || v == p.MapsTo {
			return p.MapsTo, nil
		}
	}
	return "", usageErr("unknown live transcription provider "+v, "choose: "+strings.Join(providerIDs(liveProviders), ", ")+" (meetstream transcription providers)")
}

// postCallProvider checks a --transcription value. Unknown names pass through
// (the API may know newer providers) with a warning on stderr.
func postCallProvider(v string) (name string, known bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, p := range postCallProviders {
		if v == p.ID {
			return v, true
		}
	}
	return v, false
}

// isStreamingOnly is true for providers that make no post-call transcript.
func isStreamingOnly(p string) bool {
	return strings.HasSuffix(p, "_streaming") || p == "meeting_captions"
}

func newTranscriptionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "transcription", Short: "Transcription providers you can choose", GroupID: "meet"}
	cmd.AddCommand(&cobra.Command{
		Use: "providers", Short: "List providers for --transcription (after the call) and --live-transcription", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := map[string]any{"post_call": postCallProviders, "live": liveProviders,
				"defaults": map[string]string{"join": "none (no transcript unless you pass --transcription)", "transcribe": "deepgram"},
				"source":   "built into this CLI version; keys marked dashboard go under Integrations > Transcription"}
			return emit(out, func() {
				fmt.Fprintln(ui.Out, ui.Section("After the call")+ui.DimStyle.Render("   --transcription <id>"))
				fmt.Fprintln(ui.Out, providerTable(postCallProviders))
				fmt.Fprintln(ui.Out)
				fmt.Fprintln(ui.Out, ui.Section("Live, while the meeting runs")+ui.DimStyle.Render("   --live-transcription <id>"))
				fmt.Fprintln(ui.Out, providerTable(liveProviders))
				fmt.Fprintln(ui.Out)
				ui.Note("\"your key\" = save the provider key in the dashboard: Integrations > Transcription.")
			})
		},
	})
	return cmd
}

func providerTable(list []transcriptionProvider) string {
	rows := [][]string{}
	for _, p := range list {
		key := "no key needed"
		if p.Key == "dashboard" {
			key = "your key"
		}
		rows = append(rows, []string{p.ID, p.Label, key, p.Note})
	}
	return ui.Table([]ui.Column{{Title: "ID"}, {Title: "Provider"}, {Title: "Key"}, {Title: "Note"}}, rows, ui.Width())
}
