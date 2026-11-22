package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// UsageError is a bad flag combination. The CLI exits 2.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

func usage(msg string) error { return &UsageError{Msg: msg} }

var (
	videoLayouts = map[string]bool{"speaker_view": true, "grid_view": true, "speaker_view_default": true, "grid_view_default": true}
	storageIDRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	teamsHosts   = []string{"teams.microsoft.com", "teams.live.com", "teams.cloud.microsoft"}
	googleHosts  = []string{"meet.google.com"}
)

// CreateOpts is the CLI-shaped input to BuildCreateBotPayload. Nil pointers mean "not passed".
type CreateOpts struct {
	MeetingLink             string
	Name                    string
	Video                   bool
	VideoLayout             string
	Transcript              string
	Language                string
	Callback                string
	JoinAt                  string
	BotMessage              string
	ImageURL                string
	RetentionHours          string
	SeparateAudio           bool
	SeparateVideo           bool
	ZoomObf                 bool
	ZoomZakURL              string
	ZoomObfURL              string
	ZoomUseOwnApp           *bool
	StorageID               *string
	GoogleLoginDomain       string
	TeamsLoginDomain        string
	SignInEmail             string
	StrictEmail             *bool
	AgentConfigID           string
	AgentConfigParams       map[string]string
	LiveTranscriptWebhook   string
	LiveAudioWs             string
	LiveVideoWs             string
	SocketWs                string
	Attrs                   map[string]string
	EveryoneLeftTimeout     *float64
	WaitingRoomTimeout      *float64
	MaxRecordingSeconds     *float64
	PermissionDeniedTimeout *float64
}

// BuildCreateBotPayload maps create/join flags onto the create_bot JSON body.
// Field names match the Node client's buildCreateBotPayload.
func BuildCreateBotPayload(o CreateOpts) (map[string]any, error) {
	name := o.Name
	if name == "" {
		name = "MeetStream Bot"
	}
	payload := map[string]any{
		"meeting_link":   o.MeetingLink,
		"bot_name":       name,
		"video_required": o.Video,
	}
	if o.BotMessage != "" {
		payload["bot_message"] = o.BotMessage
	}
	if o.ImageURL != "" {
		payload["bot_image_url"] = o.ImageURL
	}
	if o.Callback != "" {
		payload["callback_url"] = o.Callback
	}
	if o.JoinAt != "" {
		payload["join_at"] = o.JoinAt
	}
	if o.AgentConfigID != "" {
		payload["agent_config_id"] = o.AgentConfigID
	}
	if len(o.AgentConfigParams) > 0 {
		payload["agent_config_params"] = o.AgentConfigParams
	}
	if o.SeparateAudio {
		payload["audio_separate_streams"] = true
	}
	if o.SeparateVideo {
		payload["video_separate_streams"] = true
	}
	if o.Video {
		layout := strings.ToLower(o.VideoLayout)
		if layout == "" {
			layout = "speaker_view"
		}
		if !videoLayouts[layout] {
			return nil, usage("--video-layout must be speaker_view or grid_view (Google Meet and Teams also accept speaker_view_default or grid_view_default).")
		}
		setRecording(payload, "video_layout", layout)
	} else if o.VideoLayout != "" {
		return nil, usage("--video-layout needs --video (an audio-only bot records no mixed video).")
	}
	if o.ZoomObf {
		return nil, fmt.Errorf("--zoom-obf was removed: the API rejects use_zoom_obf. Use --zoom-obf-url <https-url> or --zoom-zak-url <https-url>.")
	}
	if o.ZoomUseOwnApp != nil && !*o.ZoomUseOwnApp && (o.ZoomZakURL != "" || o.ZoomObfURL != "") {
		return nil, usage("--zoom-zak-url and --zoom-obf-url need your own Zoom app; they cannot be used with --no-zoom-use-own-app.")
	}
	if o.ZoomZakURL != "" || o.ZoomObfURL != "" {
		zoom := map[string]any{}
		if o.ZoomZakURL != "" {
			zoom["zak_url"] = o.ZoomZakURL
		}
		if o.ZoomObfURL != "" {
			zoom["obf_url"] = o.ZoomObfURL
		}
		payload["zoom"] = zoom
	}
	if o.ZoomUseOwnApp != nil {
		z, _ := payload["zoom"].(map[string]any)
		if z == nil {
			z = map[string]any{}
		}
		z["use_own_app"] = *o.ZoomUseOwnApp
		payload["zoom"] = z
	}
	if o.StorageID != nil {
		if !storageIDRe.MatchString(*o.StorageID) {
			return nil, usage("--storage-id must be 1-80 letters, numbers, underscores or hyphens.")
		}
		payload["storage_id"] = *o.StorageID
	}
	signed, err := buildSignedInBlock(o)
	if err != nil {
		return nil, err
	}
	if signed != nil {
		payload[signed.key] = signed.block
	}
	if o.LiveTranscriptWebhook != "" {
		payload["live_transcription_required"] = map[string]any{"webhook_url": o.LiveTranscriptWebhook}
	}
	if o.LiveAudioWs != "" {
		payload["live_audio_required"] = map[string]any{"websocket_url": o.LiveAudioWs}
	}
	if o.LiveVideoWs != "" {
		payload["live_video_required"] = map[string]any{"websocket_url": o.LiveVideoWs}
	}
	if o.SocketWs != "" {
		payload["socket_connection_url"] = map[string]any{"websocket_url": o.SocketWs}
	}
	if len(o.Attrs) > 0 {
		payload["custom_attributes"] = o.Attrs
	}
	if o.Transcript != "" {
		setRecording(payload, "transcript", map[string]any{"provider": createProvider(o.Transcript, o.Language)})
	}
	if o.RetentionHours != "" {
		n, err := strconv.ParseFloat(o.RetentionHours, 64)
		if err != nil {
			return nil, usage("--retention-hours must be a number.")
		}
		setRecording(payload, "retention", map[string]any{"type": "timed", "hours": n})
	}
	perm := numOr(o.PermissionDeniedTimeout, 60)
	if perm < 60 {
		perm = 60
	}
	payload["automatic_leave"] = map[string]any{
		"waiting_room_timeout":                numOr(o.WaitingRoomTimeout, 300),
		"everyone_left_timeout":               numOr(o.EveryoneLeftTimeout, 60),
		"in_call_recording_timeout":           numOr(o.MaxRecordingSeconds, 14400),
		"recording_permission_denied_timeout": perm,
	}
	return payload, nil
}

func numOr(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func setRecording(payload map[string]any, key string, val any) {
	rc, _ := payload["recording_config"].(map[string]any)
	if rc == nil {
		rc = map[string]any{}
	}
	rc[key] = val
	payload["recording_config"] = rc
}

func createProvider(p, language string) map[string]any {
	switch p {
	case "deepgram":
		return map[string]any{"deepgram": map[string]any{"model": "nova-3", "language": orStr(language, "en"), "diarize": true}}
	case "assemblyai":
		return map[string]any{"assemblyai": map[string]any{"speech_models": []any{"best"}, "language_code": orStr(language, "en_us"), "speaker_labels": true}}
	case "sarvam":
		return map[string]any{"sarvam": map[string]any{"model": "saarika:v2", "language_code": orStr(language, "en-IN"), "mode": "batch", "with_diarization": true}}
	case "meetstream":
		return map[string]any{"meetstream": map[string]any{"language": orStr(language, "auto"), "translate": false}}
	case "jigsawstack":
		return map[string]any{"jigsawstack": map[string]any{"language": orStr(language, "auto"), "translate": false, "by_speaker": true}}
	case "meeting_captions":
		return map[string]any{"meeting_captions": map[string]any{}}
	case "deepgram_streaming":
		return map[string]any{"deepgram_streaming": map[string]any{"model": "nova-3", "language": orStr(language, "en")}}
	case "assemblyai_streaming":
		return map[string]any{"assemblyai_streaming": map[string]any{}}
	case "smallest":
		return map[string]any{"smallest": map[string]any{"model": "pulse", "language": orStr(language, "en"), "diarize": true}}
	case "smallest_streaming":
		return map[string]any{"smallest_streaming": map[string]any{"model": "pulse", "language": orStr(language, "en")}}
	default:
		return map[string]any{p: map[string]any{}}
	}
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

type signedBlock struct {
	key   string
	block map[string]any
}

func buildSignedInBlock(o CreateOpts) (*signedBlock, error) {
	if o.GoogleLoginDomain != "" && o.TeamsLoginDomain != "" {
		return nil, usage("Use either --google-login-domain or --teams-login-domain, not both (one meeting, one platform).")
	}
	domain := o.GoogleLoginDomain
	if o.TeamsLoginDomain != "" {
		domain = o.TeamsLoginDomain
	}
	strictFalse := o.StrictEmail != nil && !*o.StrictEmail
	if domain == "" {
		if o.SignInEmail != "" {
			return nil, usage("--sign-in-email needs --google-login-domain or --teams-login-domain.")
		}
		if strictFalse {
			return nil, usage("--no-strict-email only applies together with --sign-in-email and a login domain flag.")
		}
		return nil, nil
	}
	if strictFalse && o.SignInEmail == "" {
		return nil, usage("--no-strict-email only applies together with --sign-in-email.")
	}
	isTeams := o.TeamsLoginDomain != ""
	key := "google_meet"
	domainKey := "google_login_domain"
	if isTeams {
		key = "teams"
		domainKey = "teams_login_domain"
	}
	block := map[string]any{"login_required": true, domainKey: domain}
	if o.SignInEmail != "" {
		block["sign_in_email"] = o.SignInEmail
		block["strict_email"] = !strictFalse
	}
	return &signedBlock{key: key, block: block}, nil
}

// SignedInWarnings are non-fatal. The request is still sent.
func SignedInWarnings(meetingLink, googleDomain, teamsDomain string) []string {
	var warnings []string
	host := linkHost(meetingLink)
	if teamsDomain != "" {
		if !hostIn(host, teamsHosts) {
			shown := host
			if shown == "" {
				shown = meetingLink
			}
			warnings = append(warnings, fmt.Sprintf(`--teams-login-domain is set but the meeting link host is "%s", not a Microsoft Teams host (teams.microsoft.com / teams.live.com). Sending anyway.`, shown))
		} else if hostIn(host, []string{"teams.live.com"}) {
			warnings = append(warnings, "teams.live.com is personal Teams: signed-in bots only work with Microsoft 365 work/school Teams meetings.")
		}
		warnings = append(warnings, "Signed-in Teams bots show the Microsoft account's own display name and picture; --name and --image-url are not applied.")
	}
	if googleDomain != "" && !hostIn(host, googleHosts) {
		shown := host
		if shown == "" {
			shown = meetingLink
		}
		warnings = append(warnings, fmt.Sprintf(`--google-login-domain is set but the meeting link host is "%s", not meet.google.com. Sending anyway.`, shown))
	}
	return warnings
}

func linkHost(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func hostIn(host string, list []string) bool {
	for _, h := range list {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

func (c *Client) CreateBot(ctx context.Context, payload any, idempotencyKey string) (int, any, error) {
	var headers map[string]string
	if idempotencyKey != "" {
		headers = map[string]string{"Idempotency-Key": idempotencyKey}
	}
	return c.doJSON(ctx, http.MethodPost, "/bots/create_bot", payload, headers)
}

func (c *Client) ListBots(ctx context.Context) (any, error) {
	return c.getJSON(ctx, "/bots")
}

func (c *Client) BotStatus(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/status")
}

func (c *Client) BotDetail(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/detail")
}

func (c *Client) BotAudio(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/get_audio")
}

func (c *Client) BotVideo(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/get_video")
}

func (c *Client) RecordingStreams(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/get_recording_streams")
}

func (c *Client) AudioStreams(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/get_audio_streams")
}

func (c *Client) SpeakerTimeline(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/get_speaker_timeline")
}

// DownloadJSON fetches a pre-signed URL with no Authorization header.
func (c *Client) DownloadJSON(ctx context.Context, rawURL string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		return nil, &Error{Status: res.StatusCode, Body: string(raw), Method: http.MethodGet, Path: "speaker_timeline_url"}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw), nil
	}
	return v, nil
}

func (c *Client) Chats(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/get_chats")
}

func (c *Client) Screenshots(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/screenshots")
}

func (c *Client) Participants(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/get_participants")
}

func (c *Client) RemoveBot(ctx context.Context, id string) (any, error) {
	return c.getJSON(ctx, "/bots/"+id+"/remove_bot")
}

func (c *Client) DeleteBotData(ctx context.Context, id string) (any, error) {
	var out any
	err := c.Do(ctx, http.MethodDelete, "/bots/"+id+"/delete", nil, &out)
	return out, err
}

func (c *Client) SendMessage(ctx context.Context, id, message string) (any, error) {
	_, data, err := c.doJSON(ctx, http.MethodPost, "/bots/"+id+"/send_message", map[string]any{"message": message}, nil)
	return data, err
}

func (c *Client) SendImage(ctx context.Context, id, imgURL string, displayDuration float64) (any, error) {
	body := map[string]any{"img_url": imgURL}
	if displayDuration != 0 {
		body["display_duration"] = displayDuration
	}
	_, data, err := c.doJSON(ctx, http.MethodPost, "/bots/"+id+"/send_image", body, nil)
	return data, err
}

func (c *Client) SendCommand(ctx context.Context, id, command string) (any, error) {
	_, data, err := c.doJSON(ctx, http.MethodPost, "/bots/"+id+"/send_command", map[string]any{"command": command}, nil)
	return data, err
}
