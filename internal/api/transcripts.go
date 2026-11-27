package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"
)

// TranscriptOpt controls get-transcript polling. Zero Timeout/Interval use the CLI defaults.
type TranscriptOpt struct {
	Raw      bool
	Wait     bool
	Timeout  time.Duration
	Interval time.Duration
}

// TranscriptResult is a resolved transcript. Transcript is nil when it is not ready.
type TranscriptResult struct {
	TranscriptID string
	Transcript   any
}

func (c *Client) Transcriptions(ctx context.Context, botID string) (any, error) {
	return c.getJSON(ctx, "/bots/"+botID+"/transcriptions")
}

// TranscribeProvider is the body of POST /bots/{id}/transcribe (not the create_bot shape).
func TranscribeProvider(name, language string) map[string]any {
	if name == "" {
		name = "deepgram"
	}
	switch name {
	case "deepgram":
		return map[string]any{"deepgram": map[string]any{"model": "nova-3", "language": orStr(language, "en")}}
	case "assemblyai":
		return map[string]any{"assemblyai": map[string]any{"speech_models": []any{"best"}, "language_code": orStr(language, "en_us")}}
	case "sarvam":
		return map[string]any{"sarvam": map[string]any{"model": "saarika:v2", "language_code": orStr(language, "en-IN"), "mode": "batch"}}
	case "meetstream":
		return map[string]any{"meetstream": map[string]any{"language": orStr(language, "auto"), "translate": false}}
	case "jigsawstack":
		return map[string]any{"jigsawstack": map[string]any{"language": orStr(language, "auto"), "translate": false}}
	case "smallest":
		return map[string]any{"smallest": map[string]any{"model": "pulse", "language": orStr(language, "en")}}
	default:
		return map[string]any{name: map[string]any{}}
	}
}

func (c *Client) Transcribe(ctx context.Context, botID, provider, language, callback string) (any, error) {
	body := map[string]any{"provider": TranscribeProvider(provider, language)}
	if callback != "" {
		body["callback_url"] = callback
	}
	_, data, err := c.doJSON(ctx, "POST", "/bots/"+botID+"/transcribe", body, nil)
	return data, err
}

func (c *Client) GetTranscriptByID(ctx context.Context, transcriptID string, raw bool) (any, error) {
	data, _, err := c.getTranscriptStatus(ctx, transcriptID, raw)
	return data, err
}

// getTranscriptStatus also returns the HTTP status: the API answers 202 while the transcript is
// still being processed (a JSON message, not a transcript), so 202 means "not ready yet".
func (c *Client) getTranscriptStatus(ctx context.Context, transcriptID string, raw bool) (any, int, error) {
	q := "false"
	if raw {
		q = "true"
	}
	status, data, err := c.doJSON(ctx, "GET", "/transcript/"+transcriptID+"/get_transcript?raw="+q, nil, nil)
	return data, status, err
}

// TerminalBotStatus matches whole bot status values after which the bot does
// nothing more. RecordingPermissionDenied is not terminal.
var TerminalBotStatus = regexp.MustCompile("^(?i)(stopped|kicked|done|error|failed|notallowed|denied|cancelled|mediaexpired)$")

func (c *Client) botStatusValue(ctx context.Context, botID string) (string, error) {
	data, err := c.BotStatus(ctx, botID)
	if err != nil {
		return "", err
	}
	m, _ := data.(map[string]any)
	return asString(m["status"]), nil
}

// getBotTranscript is GET /bots/{id}/get_transcript. It answers 409 for a bot
// that was not admitted (no transcript) and 202 while one is processing.
func (c *Client) getBotTranscript(ctx context.Context, botID string, raw bool) (any, int, error) {
	q := "false"
	if raw {
		q = "true"
	}
	status, data, err := c.doJSON(ctx, "GET", "/bots/"+botID+"/get_transcript?raw="+q, nil, nil)
	return data, status, err
}

// ResolveTranscriptID reads detail, then /transcriptions. Detail errors are ignored.
func (c *Client) ResolveTranscriptID(ctx context.Context, botID string) (string, error) {
	if data, err := c.BotDetail(ctx, botID); err == nil {
		if tid := transcriptIDFromDetail(data); tid != "" {
			return tid, nil
		}
	}
	data, err := c.Transcriptions(ctx, botID)
	if err != nil {
		return "", err
	}
	return transcriptIDFromList(data), nil
}

func transcriptIDFromDetail(data any) string {
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	if bd, ok := m["bot_details"].(map[string]any); ok {
		if s := asString(bd["transcript_id"]); s != "" {
			return s
		}
	}
	return asString(m["transcript_id"])
}

func transcriptIDFromList(data any) string {
	m, _ := data.(map[string]any)
	items, _ := m["transcriptions"].([]any)
	var first any
	var matched any
	for _, it := range items {
		row, _ := it.(map[string]any)
		if first == nil {
			first = row
		}
		if transcriptDone.MatchString(asString(row["status"])) {
			matched = row
			break
		}
	}
	done, _ := matched.(map[string]any)
	if done == nil {
		done, _ = first.(map[string]any)
	}
	if done == nil {
		return ""
	}
	return asString(done["transcript_id"])
}

var transcriptDone = regexp.MustCompile(`(?i)success|completed`)

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// GetTranscript fetches a bot's transcript, polling only when Wait is set.
// A 422 is returned immediately and is never polled.
func (c *Client) GetTranscript(ctx context.Context, botID string, opt TranscriptOpt) (*TranscriptResult, error) {
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 600 * time.Second
	}
	interval := opt.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var tid string
	askedBot := false
	for {
		var err error
		tid, err = c.ResolveTranscriptID(ctx, botID)
		if err != nil {
			return nil, err
		}
		if tid == "" && opt.Wait && !askedBot {
			// No transcript_id. When the bot has ended, ask the bot once: a 409 means it was
			// not admitted and there will never be a transcript, so stop instead of polling.
			if st, serr := c.botStatusValue(ctx, botID); serr == nil && TerminalBotStatus.MatchString(st) {
				askedBot = true
				data, status, gerr := c.getBotTranscript(ctx, botID, opt.Raw)
				var ae *Error
				switch {
				case errors.As(gerr, &ae) && ae.Status == 409:
					return nil, gerr
				case gerr == nil && status != 202:
					if seg := unwrapTranscript(data); transcriptReady(seg) {
						return &TranscriptResult{Transcript: seg}, nil
					}
				}
			}
		}
		if tid != "" {
			data, status, gerr := c.getTranscriptStatus(ctx, tid, opt.Raw)
			if gerr != nil {
				if !opt.Wait || shouldStopTranscriptPoll(gerr) {
					return nil, gerr
				}
			} else if status == 202 {
				// Still being processed: not a transcript yet.
			} else {
				seg := unwrapTranscript(data)
				if transcriptReady(seg) {
					return &TranscriptResult{TranscriptID: tid, Transcript: seg}, nil
				}
			}
		}
		if !opt.Wait || time.Now().After(deadline) {
			if !opt.Wait {
				return &TranscriptResult{TranscriptID: tid, Transcript: nil}, nil
			}
			secs := int(math.Round(timeout.Seconds()))
			return nil, fmt.Errorf("timed out after %ds waiting for transcript of bot %s", secs, botID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func shouldStopTranscriptPoll(err error) bool {
	var ae *Error
	if !errors.As(err, &ae) {
		return false
	}
	if ae.Status == 422 {
		return true
	}
	return ae.Status != 0 && ae.Status != 404 && ae.Status != 400
}

func unwrapTranscript(data any) any {
	if _, ok := data.([]any); ok {
		return data
	}
	if m, ok := data.(map[string]any); ok {
		if msg, ok := m["message"].([]any); ok {
			return msg
		}
	}
	return data
}

func transcriptReady(seg any) bool {
	if seg == nil {
		return false
	}
	if arr, ok := seg.([]any); ok {
		return len(arr) > 0
	}
	return true
}

// TranscriptFailure is a final 422. It must not be polled.
type TranscriptFailure struct {
	Reason           string
	TranscriptStatus string
	TranscriptID     any
}

var failedPrefix = regexp.MustCompile(`(?i)^Transcript processing failed:?\s*`)

// FailedTranscript reads a 422 body that carries transcript_status. Otherwise nil.
func FailedTranscript(err error) *TranscriptFailure {
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 422 {
		return nil
	}
	var body map[string]any
	if json.Unmarshal([]byte(ae.Body), &body) != nil {
		return nil
	}
	st, ok := body["transcript_status"].(string)
	if !ok {
		return nil
	}
	msg, _ := body["message"].(string)
	reason := failedPrefix.ReplaceAllString(msg, "")
	if reason == "" {
		reason = msg
	}
	if reason == "" {
		reason = "no reason given"
	}
	return &TranscriptFailure{Reason: reason, TranscriptStatus: st, TranscriptID: body["transcript_id"]}
}
