// Package mock is a local stand-in for the MeetStream API. It answers the calls
// the CLI and the templates make, runs a bot through a scripted scenario, and
// posts the same webhook envelopes production sends. No bot joins a meeting and
// nothing is charged.
package mock

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// Event builds one lifecycle webhook in the production envelope: "event" names
// the delivery, "bot_event" the specific reason (bot.stopped is the only
// terminal event; bot_event says why).
func Event(event, botEvent, botID, botStatus, message string, code int) map[string]any {
	m := map[string]any{
		"event":     event,
		"bot_id":    botID,
		"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if botEvent != "" {
		m["bot_event"] = botEvent
	}
	if botStatus != "" {
		m["bot_status"] = botStatus
	}
	if message != "" {
		m["message"] = message
	}
	if code != 0 {
		m["status_code"] = code
	}
	return m
}

// LiveSegment is one live-transcript POST in sentence mode (no "event" key, as in production).
func LiveSegment(botID, speaker, speakerID, text string, turn int) map[string]any {
	return map[string]any{
		"bot_id": botID, "speakerName": speaker, "speakerId": speakerID,
		"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"utterance": text, "transcript": text, "end_of_turn": true, "turn_order": turn,
		"transcription_mode": "sentence",
	}
}

// Samples are the events `meetstream trigger` can send, by name.
func Samples(botID string) map[string]map[string]any {
	return map[string]map[string]any{
		"bot.joining":             Event("bot.joining", "bot.joining", botID, "Joining", "Bot is joining the meeting", 200),
		"bot.in_waiting_room":     Event("bot.in_waiting_room", "bot.in_waiting_room", botID, "InWaitingRoom", "Waiting to be admitted", 200),
		"bot.inmeeting":           Event("bot.inmeeting", "bot.inmeeting", botID, "InMeeting", "Bot is in the meeting", 200),
		"bot.recording":           Event("bot.recording", "bot.recording", botID, "Recording", "Recording started", 200),
		"bot.stopped":             Event("bot.stopped", "bot.stopped", botID, "Stopped", "Everyone left", 200),
		"bot.kicked":              Event("bot.stopped", "bot.kicked", botID, "Stopped", "Removed by a participant", 200),
		"bot.notallowed":          Event("bot.stopped", "bot.notallowed", botID, "NotAllowed", "Failed: Not admitted to meeting", 500),
		"bot.denied":              Event("bot.stopped", "bot.denied", botID, "Denied", "Host refused entry", 500),
		"bot.failed":              Event("bot.stopped", "bot.failed", botID, "Failed", "Unexpected error", 500),
		"audio.processed":         Event("audio.processed", "", botID, "", "Audio ready", 200),
		"transcription.processed": Event("transcription.processed", "", botID, "", "Transcript ready", 200),
		"transcription.failed":    Event("transcription.failed", "", botID, "", "Transcript processing failed: audio too short", 500),
		"bot.done":                Event("bot.done", "", botID, "", "Post-call processing finished", 200),
		"live.transcript":         LiveSegment(botID, "Jane Smith", "16778240", "Can everyone hear me?", 1),
	}
}

// SampleNames is the sorted list of trigger names.
func SampleNames() []string {
	var out []string
	for k := range Samples("x") {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Post sends one JSON payload and returns the HTTP status (0 on a network error).
func Post(ctx context.Context, client *http.Client, url string, payload any) (int, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "meetstream-mock")
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	return res.StatusCode, nil
}
