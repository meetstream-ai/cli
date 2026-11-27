package mock

// Agent stack in the mock: /mia agent configs, in-meeting bot commands, the bridge sockets
// (the bot is a WebSocket client of the customer's server) and custom-function calls.
// Shapes follow production (meetstream-codebase origin/main 13c8897bd, 26 Nov 2026).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type agentConfig struct {
	ID     string
	Record map[string]any // PascalCase, as production stores it
	Raw    map[string]any // the body as sent (snake_case), for custom_functions
}

// liveModeOf returns transcription_mode of the streaming provider (production default: raw).
func liveModeOf(payload map[string]any) string {
	rc, _ := payload["recording_config"].(map[string]any)
	tr, _ := rc["transcript"].(map[string]any)
	p, _ := tr["provider"].(map[string]any)
	for k, v := range p {
		if strings.HasSuffix(k, "_streaming") {
			if m, ok := v.(map[string]any); ok {
				if s, _ := m["transcription_mode"].(string); s != "" {
					return s
				}
			}
			return "raw"
		}
	}
	return "sentence"
}

// livePosts gives the live-transcript POSTs for one spoken line in the bot's mode.
func livePosts(botID, speaker, speakerID, text string, turn int, mode string) []map[string]any {
	base := func() map[string]any {
		return map[string]any{"bot_id": botID, "speakerName": speaker, "speakerId": speakerID,
			"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "transcription_mode": mode}
	}
	switch mode {
	case "word_level":
		var out []map[string]any
		for _, w := range strings.Fields(text) {
			m := base()
			m["transcript"], m["utterance"] = w, ""
			out = append(out, m)
		}
		return out
	case "raw":
		words := strings.Fields(text)
		half := strings.Join(words[:(len(words)+1)/2], " ")
		interim := base()
		interim["transcript"], interim["utterance"], interim["is_final"], interim["end_of_turn"] = half, "", false, false
		interim["turn_order"], interim["turn_is_formatted"], interim["end_of_turn_confidence"] = nil, false, nil
		final := base()
		final["transcript"], final["utterance"], final["is_final"], final["end_of_turn"] = text, "", true, true
		final["turn_order"], final["turn_is_formatted"], final["end_of_turn_confidence"] = nil, false, nil
		return []map[string]any{interim, final}
	default:
		return []map[string]any{LiveSegment(botID, speaker, speakerID, text, turn)}
	}
}

// ---------------------------------------------------------------------------------------------
// /mia

var managedCatalog = map[string]map[string][]string{ // section -> provider -> models with a MeetStream key
	"transcriber": {"deepgram": {"nova-3"}},
	"model":       {"openai": {"gpt-4o-mini", "gpt-4.1-mini"}},
	"voice":       {"openai": {"tts-1"}},
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func obj(m map[string]any, k string) map[string]any {
	o, _ := m[k].(map[string]any)
	return o
}

// validateMIA returns the first production 400 message, or "".
func validateMIA(b map[string]any) string {
	if str(b, "agent_name") == "" {
		return "Missing agent_name."
	}
	mode := str(b, "mode")
	if mode == "" {
		return "Missing mode. Must be 'pipeline' or 'realtime'."
	}
	if mode != "pipeline" && mode != "realtime" {
		return "Invalid mode: " + mode + ". Must be 'pipeline' or 'realtime'."
	}
	model := obj(b, "model")
	if mode == "realtime" {
		if model == nil {
			return "Missing model configuration for realtime mode."
		}
		p := strings.ToLower(str(model, "provider"))
		if p != "google" && p != "openai" && p != "xai" && p != "gemini" {
			return "Realtime mode requires model.provider to be one of: ['google', 'openai', 'xai']."
		}
		if str(model, "system_prompt") == "" {
			return "Missing model.system_prompt."
		}
		if obj(b, "voice") != nil && str(obj(b, "voice"), "key_source") == "meetstream" {
			return "MeetStream-managed keys in realtime mode cover only: model. Remove key_source from: voice."
		}
		return keySources(b, []string{"model"})
	}
	if model == nil {
		return "Missing model configuration for pipeline mode."
	}
	if str(model, "provider") == "" {
		return "Missing model.provider."
	}
	if str(model, "model") == "" {
		return "Missing model.model (model name)."
	}
	if str(model, "system_prompt") == "" {
		return "Missing model.system_prompt."
	}
	voice := obj(b, "voice")
	if voice == nil {
		return "Missing voice configuration for pipeline mode."
	}
	if str(voice, "provider") == "" {
		return "Missing voice.provider."
	}
	if str(voice, "voice_id") == "" && str(voice, "provider") != "none" {
		return "Missing voice.voice_id."
	}
	tr := obj(b, "transcriber")
	if tr == nil {
		return "Missing transcriber configuration for pipeline mode."
	}
	if str(tr, "provider") == "" {
		return "Missing transcriber.provider."
	}
	if msg := validateFunctions(b); msg != "" {
		return msg
	}
	return keySources(b, []string{"transcriber", "model", "voice"})
}

func keySources(b map[string]any, sections []string) string {
	for _, sec := range sections {
		o := obj(b, sec)
		if o == nil {
			continue
		}
		ks := str(o, "key_source")
		if ks == "" || ks == "user" {
			continue
		}
		if ks != "meetstream" {
			return sec + ".key_source must be one of: user, meetstream."
		}
		if str(o, "api_key") != "" {
			return sec + `.api_key must be empty when ` + sec + `.key_source is "meetstream".`
		}
		provs := managedCatalog[sec]
		models, ok := provs[str(o, "provider")]
		if !ok {
			var names []string
			for p := range provs {
				names = append(names, p)
			}
			return fmt.Sprintf("%s.provider '%s' has no MeetStream key. Use one of: %s.", sec, str(o, "provider"), strings.Join(names, ", "))
		}
		if m := str(o, "model"); m != "" {
			found := false
			for _, x := range models {
				found = found || x == m
			}
			if !found {
				return fmt.Sprintf("%s.model '%s' has no MeetStream price. Use one of: %s.", sec, m, strings.Join(models, ", "))
			}
		}
	}
	return ""
}

func functionsOf(b map[string]any) []any {
	if a := obj(b, "agent"); a != nil {
		if f, ok := a["custom_functions"].([]any); ok {
			return f
		}
	}
	f, _ := b["custom_functions"].([]any)
	return f
}

func validateFunctions(b map[string]any) string {
	fns := functionsOf(b)
	if len(fns) > 20 {
		return "agent.custom_functions has more than 20 entries"
	}
	for i, x := range fns {
		f, _ := x.(map[string]any)
		name := str(f, "name")
		ok := name != "" && len(name) <= 64 && name[0] >= 'a' && name[0] <= 'z'
		for _, c := range name {
			ok = ok && (c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_')
		}
		if !ok {
			return fmt.Sprintf("custom_functions[%d].name must match ^[a-z][a-z0-9_]*$ and be 1–64 chars", i)
		}
		if str(f, "description") == "" {
			return fmt.Sprintf("custom_functions[%d].description is required", i)
		}
		u, err := url.Parse(str(f, "url"))
		// Production needs https and a public IP. The mock also allows http://127.0.0.1 and localhost so tests can run.
		local := err == nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")
		if err != nil || (u.Scheme != "https" && !local) {
			return fmt.Sprintf("custom_functions[%d].url rejected: must be an https URL", i)
		}
	}
	return ""
}

func pascal(b map[string]any, id string) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	rec := map[string]any{"AgentConfigID": id, "UserID": "mock-user", "AgentName": b["agent_name"], "Mode": b["mode"],
		"Model": b["model"], "Agent": b["agent"], "Audio": map[string]any{"sample_rate": 24000, "num_channels": 1},
		"Voice": b["voice"], "Transcriber": b["transcriber"], "CreatedAt": now, "UpdatedAt": now}
	if b["mode"] == "realtime" {
		rec["Voice"], rec["Transcriber"] = nil, nil
	}
	if b["agent"] == nil {
		rec["Agent"] = map[string]any{}
	}
	if w, ok := b["wake_word"]; ok {
		rec["WakeWord"] = w
	}
	return rec
}

func (s *Server) mia(w http.ResponseWriter, r *http.Request) {
	msg := func(code int, m string) { writeJSON(w, code, map[string]any{"message": m}) }
	readBody := func() (map[string]any, bool) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if len(bytes.TrimSpace(raw)) == 0 {
			msg(400, "Request body is missing")
			return nil, false
		}
		var b map[string]any
		if err := json.Unmarshal(raw, &b); err != nil {
			msg(400, "Invalid JSON in request body: "+err.Error())
			return nil, false
		}
		return b, true
	}
	id := r.URL.Query().Get("agent_config_id")
	switch r.Method {
	case http.MethodPost:
		b, ok := readBody()
		if !ok {
			return
		}
		if m := validateMIA(b); m != "" {
			msg(400, m)
			return
		}
		s.mu.Lock()
		s.seq++
		cid := fmt.Sprintf("mock-agent-%04d", s.seq)
		ac := &agentConfig{ID: cid, Record: pascal(b, cid), Raw: b}
		s.agents[cid] = ac
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"message": "Agent configuration created successfully.", "agent_config_id": cid, "agent_config": ac.Record})
	case http.MethodGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		if id != "" {
			ac := s.agents[id]
			if ac == nil {
				msg(404, "Agent configuration not found.")
				return
			}
			writeJSON(w, 200, map[string]any{"agent_config": ac.Record})
			return
		}
		rows := []any{}
		for _, ac := range s.agents {
			rows = append(rows, ac.Record)
		}
		// Same shape as production's catalog (prices per hour come from SSM there; these are the code's example values).
		writeJSON(w, 200, map[string]any{"agent_configs": rows, "count": len(rows), "mia_managed": map[string]any{
			"enabled": true,
			"rates":   map[string]any{"base_price": 0.35, "agent": 0.6, "managed_agent": 0.6},
			"sections": map[string]any{
				"transcriber": map[string]any{"default_provider": "deepgram", "providers": map[string]any{"deepgram": map[string]any{"label": "Deepgram via MeetStream", "default_model": "nova-3", "models": map[string]any{"nova-3": 0.50}}}},
				"model":       map[string]any{"default_provider": "openai", "providers": map[string]any{"openai": map[string]any{"label": "OpenAI via MeetStream", "default_model": "gpt-4o-mini", "models": map[string]any{"gpt-4o-mini": 0.05, "gpt-4.1-mini": 0.10}}}},
				"voice":       map[string]any{"default_provider": "openai", "providers": map[string]any{"openai": map[string]any{"label": "OpenAI via MeetStream", "default_model": "tts-1", "models": map[string]any{"tts-1": 0.10}}}},
			}}})
	case http.MethodPut:
		b, ok := readBody()
		if !ok {
			return
		}
		cid := str(b, "agent_config_id")
		if cid == "" {
			msg(400, "Missing agent_config_id.")
			return
		}
		s.mu.Lock()
		ac := s.agents[cid]
		if ac == nil {
			s.mu.Unlock()
			msg(404, "Agent configuration not found.")
			return
		}
		merged := map[string]any{}
		for k, v := range ac.Raw {
			merged[k] = v
		}
		for _, k := range []string{"agent_name", "mode", "model", "voice", "transcriber", "agent", "wake_word", "audio"} {
			if v, ok := b[k]; ok {
				merged[k] = v // each block is replaced whole, as production does
			}
		}
		if m := validateFunctions(merged); m != "" { // production validates custom_functions whenever present
			s.mu.Unlock()
			msg(400, m)
			return
		}
		ac.Raw = merged
		ac.Record = pascal(ac.Raw, cid)
		rec := ac.Record
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"message": "Agent configuration updated successfully.", "agent_config": rec})
	case http.MethodDelete:
		if id == "" {
			msg(400, "Missing agent_config_id parameter.")
			return
		}
		s.mu.Lock()
		_, ok := s.agents[id]
		delete(s.agents, id)
		s.mu.Unlock()
		if !ok {
			msg(404, "Agent configuration not found.")
			return
		}
		msg(200, "Agent configuration deleted successfully.")
	default:
		msg(405, "Method "+r.Method+" not allowed")
	}
}

// ---------------------------------------------------------------------------------------------
// In-meeting commands (errors use {"error": ...} like the production Lambda)

var commandStatuses = map[string]bool{"InMeeting": true, "Joining": true, "Recording": true}
var recordingStatuses = map[string]bool{"InMeeting": true, "Recording": true}

func (s *Server) command(w http.ResponseWriter, r *http.Request, b *bot, action string) {
	var body map[string]any
	if action != "pause_recording" && action != "resume_recording" {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &body) != nil {
			writeJSON(w, 400, map[string]any{"error": "Invalid JSON body", "detail": "body must be a JSON object"})
			return
		}
	}
	if body == nil {
		body = map[string]any{}
	}
	cmd := action
	switch action {
	case "send_message":
		cmd = "sendmsg"
		m, ok := body["message"].(string)
		if !ok {
			m, ok = body["msg"].(string)
		}
		if !ok {
			writeJSON(w, 400, map[string]any{"error": "Missing 'message' field."})
			return
		}
		m = strings.ReplaceAll(strings.ReplaceAll(m, "<script>", ""), "</script>", "")
		if strings.TrimSpace(m) == "" {
			writeJSON(w, 400, map[string]any{"error": "message is empty or contains only whitespace/control characters", "command": cmd})
			return
		}
		if len([]rune(m)) > 2000 {
			writeJSON(w, 400, map[string]any{"error": fmt.Sprintf("message exceeds 2000 character limit (%d chars)", len([]rune(m))), "command": cmd})
			return
		}
		body["message"] = m
	case "send_image":
		u, hasURL := body["img_url"].(string)
		_, hasImg := body["img"].(string)
		switch {
		case hasURL:
			cmd = "sendimg_url"
			if !strings.HasPrefix(u, "https://") {
				writeJSON(w, 400, map[string]any{"error": "img_url must start with https://", "command": cmd})
				return
			}
		case hasImg:
			cmd = "sendimg"
		default:
			writeJSON(w, 400, map[string]any{"error": "Missing 'img_url' or 'img' field."})
			return
		}
		if d, ok := body["display_duration"]; ok {
			f, isNum := d.(float64)
			if !isNum {
				writeJSON(w, 400, map[string]any{"error": "display_duration must be a number (seconds)", "command": cmd})
				return
			}
			if f <= 0 || f > 300 {
				writeJSON(w, 400, map[string]any{"error": "display_duration must be positive and at most 300s", "command": cmd})
				return
			}
		}
	case "send_command":
		cmd, _ = body["command"].(string)
		allowed := map[string]bool{"interrupt": true, "pause_recording": true, "resume_recording": true, "screenshot": true, "sendchat": true, "sendimg": true, "sendimg_url": true, "sendmsg": true, "update_mia": true, "raise_hand": true, "lower_hand": true}
		if !allowed[cmd] {
			writeJSON(w, 400, map[string]any{"error": "Invalid command. Allowed: ['interrupt', 'pause_recording', 'resume_recording', 'screenshot', 'sendchat', 'sendimg', 'sendimg_url', 'sendmsg', 'update_mia']"})
			return
		}
	case "update_mia":
		_, a := body["agent_config_params"]
		_, p := body["system_prompt"]
		_, we := body["wake_word_enabled"]
		_, ww := body["wake_words"]
		if !a && !p && !we && !ww {
			writeJSON(w, 400, map[string]any{"error": "At least one of 'agent_config_params', 'system_prompt', 'wake_word_enabled', or 'wake_words' is required", "command": "update_mia"})
			return
		}
		if p {
			if sp, ok := body["system_prompt"].(string); !ok || strings.TrimSpace(sp) == "" {
				writeJSON(w, 400, map[string]any{"error": "system_prompt cannot be empty", "command": "update_mia"})
				return
			}
		}
	}
	s.mu.Lock()
	status := b.Status
	s.mu.Unlock()
	needRec := cmd == "pause_recording" || cmd == "resume_recording" || cmd == "raise_hand" || cmd == "lower_hand"
	if (needRec && !recordingStatuses[status]) || !commandStatuses[status] {
		writeJSON(w, 409, map[string]any{"error": "Bot is not currently in a meeting.", "bot_status": status,
			"detail": "Commands can only be sent when the bot status is one of ['InMeeting', 'Joining', 'Recording']. Current status: " + status + "."})
		return
	}
	resp := map[string]any{"status": "accepted", "bot_id": b.ID, "command": cmd}
	s.mu.Lock()
	switch cmd {
	case "sendmsg":
		b.Chats = append(b.Chats, body["message"].(string))
		resp["request_id"] = fmt.Sprintf("mock-req-%d", time.Now().UnixNano())
	case "pause_recording", "resume_recording":
		want := cmd == "pause_recording"
		if b.Paused == want {
			s.mu.Unlock()
			writeJSON(w, 409, map[string]any{"status": "no_op", "command": cmd, "error": map[bool]string{true: "Recording already paused", false: "Recording is not paused"}[want], "recording_paused": want})
			return
		}
		b.Paused = want
		resp["recording_paused"] = want
	case "screenshot":
		sid := time.Now().UTC().Format("20060102T150405000Z") + fmt.Sprintf("_mock%04d", len(b.Shots)+1)
		shot := map[string]any{"id": sid, "recorded_at": time.Now().UTC().Format(time.RFC3339Nano), "filename": sid + ".png",
			"url": "https://example.invalid/mock/" + b.ID + "/" + sid + ".png", "size_bytes": 12345, "meta": body["metadata"]}
		b.Shots = append(b.Shots, shot)
		for k, v := range shot {
			resp[k] = v
		}
	case "raise_hand", "lower_hand":
		resp["hand_raised"] = cmd == "raise_hand"
	case "update_mia":
		if b.AgentID == "" && b.Control == nil {
			s.mu.Unlock()
			writeJSON(w, 503, map[string]any{"error": "No control WS to agentic container"})
			return
		}
		if p, ok := body["system_prompt"].(string); ok {
			b.Prompt = p
		}
		if body["wait_for_ack"] == true && b.AgentID != "" {
			var applied []string
			for _, k := range []string{"agent_config_params", "system_prompt", "wake_word_enabled", "wake_words"} {
				if _, ok := body[k]; ok {
					applied = append(applied, k)
				}
			}
			resp["status"], resp["applied"], resp["errors"] = "applied", applied, []string{}
		}
	}
	ctrl := b.Control
	s.mu.Unlock()
	if cmd == "update_mia" && ctrl != nil {
		frame := map[string]any{"command": "update_mia", "bot_id": b.ID, "request_id": fmt.Sprintf("mock-%d", time.Now().UnixNano())}
		for k, v := range body {
			frame[k] = v
		}
		ctrl.send(frame)
	}
	writeJSON(w, 200, resp)
}

// ---------------------------------------------------------------------------------------------
// Bridge sockets: the bot dials the customer's URLs. Production inserts the bot id after the host only
// when the bot has an agent_config_id (create_bot/lambda_function.py:1607); a plain bridge bot uses the URLs as given.

type wsConn struct {
	c  *websocket.Conn
	mu sync.Mutex
}

func (c *wsConn) send(v any) {
	b, _ := json.Marshal(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.c.Write(ctx, websocket.MessageText, b)
}

func (c *wsConn) sendBinary(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return c.c.Write(ctx, websocket.MessageBinary, b)
}

// withBotID inserts the bot id after the host: wss://host/path -> wss://host/<bot_id>/path.
func withBotID(raw, botID string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Path = "/" + botID + u.Path
	return u.String()
}

func (s *Server) dial(b *bot, raw, channel string) *wsConn {
	target := raw
	if b.AgentID != "" {
		target = withBotID(raw, b.ID)
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		s.logf("bridge %s socket → %s failed: %v", channel, target, err)
		s.journalAdd(Entry{Kind: "ws", Path: channel, Body: map[string]any{"dial_error": err.Error(), "url": target}})
		return nil
	}
	c.SetReadLimit(16 << 20)
	s.journalAdd(Entry{Kind: "ws", Path: channel, Body: map[string]any{"connected": target}})
	return &wsConn{c: c}
}

// readControl records every message the customer sends on the control socket and answers like Google Meet.
func (s *Server) readControl(b *bot, c *wsConn) {
	for {
		typ, data, err := c.c.Read(s.ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			kind := "binary"
			if len(data) > 0 && data[0] == 0x02 {
				kind = "sendaudio_binary"
			}
			s.journalAdd(Entry{Kind: "ws", Path: "control", Body: map[string]any{"binary": kind, "bytes": len(data)}})
			if kind == "sendaudio_binary" {
				c.send(map[string]any{"command": "sendaudio", "bot_id": b.ID, "status": "sent", "timestamp": time.Now().UTC().Format(time.RFC3339Nano)})
			}
			continue
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			s.journalAdd(Entry{Kind: "ws", Path: "control", Body: map[string]any{"invalid_json": string(data)}})
			continue
		}
		s.mu.Lock()
		ready, readyAt := b.MeetingReady, b.MeetingReadyAt
		s.mu.Unlock()
		if !ready { // Google Meet drops commands before meeting_ready, with no reply (seen live)
			s.journalAdd(Entry{Kind: "ws", Path: "control", Body: map[string]any{"dropped_before_meeting_ready": m["command"]}})
			continue
		}
		// Live, a chat sent 0.5 s after meeting_ready was dropped too; one at +5 s was posted.
		// The mock scales that settling time to 3 steps.
		if cmd, _ := m["command"].(string); cmd == "sendmsg" && time.Since(readyAt) < 3*s.Step {
			s.journalAdd(Entry{Kind: "ws", Path: "control", Body: map[string]any{"dropped_chat_too_soon_after_meeting_ready": true}})
			continue
		}
		rec := map[string]any{}
		for k, v := range m {
			if k == "audiochunk" {
				if str, ok := v.(string); ok {
					dec, derr := base64.StdEncoding.DecodeString(str)
					rec["audiochunk_bytes"], rec["audiochunk_valid"] = len(dec), derr == nil
					continue
				}
			}
			rec[k] = v
		}
		s.journalAdd(Entry{Kind: "ws", Path: "control", Body: rec})
		cmd, _ := m["command"].(string)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		switch cmd {
		case "sendaudio":
			c.send(map[string]any{"command": "sendaudio", "bot_id": b.ID, "status": "sent", "timestamp": now})
		case "sendmsg":
			text, _ := m["message"].(string)
			if text == "" {
				text, _ = m["msg"].(string)
			}
			s.mu.Lock()
			b.Chats = append(b.Chats, text)
			s.mu.Unlock()
			c.send(map[string]any{"command": "sendmsg", "bot_id": b.ID, "status": "sent", "timestamp": now})
		case "interrupt":
			c.send(map[string]any{"command": "interrupt_ack", "playback_id": m["playback_id"], "success": true, "bot_id": b.ID, "timestamp": now})
		case "resume_audio":
			c.send(map[string]any{"command": "resume_audio_ack", "bot_id": b.ID, "resume_id": m["resume_id"], "success": true, "error": ""})
		}
	}
}

// audioFrame builds a 0x04 frame: [0x04][rate u32][channels u8][sid u16][sid][sname u16][sname][pcm].
func audioFrame(rate uint32, speakerID, speaker string, pcm []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(0x04)
	_ = binary.Write(&buf, binary.LittleEndian, rate)
	buf.WriteByte(1)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(speakerID)))
	buf.WriteString(speakerID)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(speaker)))
	buf.WriteString(speaker)
	buf.Write(pcm)
	return buf.Bytes()
}

// streamAudio sends `frames` 20 ms frames of a 440 Hz tone at 24 kHz (the low-latency agent format).
func (s *Server) streamAudio(b *bot, c *wsConn, frames int) {
	const rate = 24000
	n := rate / 50 // 480 samples = 960 bytes per 20 ms
	t := 0
	for i := 0; i < frames; i++ {
		pcm := make([]byte, n*2)
		for j := 0; j < n; j++ {
			v := int16(8000 * math.Sin(2*math.Pi*440*float64(t)/rate))
			binary.LittleEndian.PutUint16(pcm[j*2:], uint16(v))
			t++
		}
		speaker, sid := "Jane Smith", "16778240"
		if i >= frames/2 {
			speaker, sid = "Ravi Kumar", "16778241"
		}
		if c.sendBinary(audioFrame(rate, sid, speaker, pcm)) != nil {
			return
		}
		if !s.sleep(20 * time.Millisecond) {
			return
		}
	}
	s.journalAdd(Entry{Kind: "ws", Path: "audio", Body: map[string]any{"frames_sent": frames, "sample_rate": rate, "bytes_per_frame": n * 2}})
}

// callFunctions makes one production-shaped call to each custom function of the bot's agent.
func (s *Server) callFunctions(b *bot) {
	s.mu.Lock()
	ac := s.agents[b.AgentID]
	s.mu.Unlock()
	if ac == nil {
		return
	}
	for _, x := range functionsOf(ac.Raw) {
		f, _ := x.(map[string]any)
		args := map[string]any{}
		if p := obj(f, "parameters"); p != nil {
			for name, sch := range obj(p, "properties") {
				t := str(sch.(map[string]any), "type")
				switch t {
				case "number", "integer":
					args[name] = 2
				case "boolean":
					args[name] = true
				default:
					args[name] = "A1"
				}
			}
		}
		method := strings.ToUpper(str(f, "method"))
		if method == "" {
			method = "POST"
		}
		var body io.Reader
		payload := map[string]any{"name": f["name"], "bot": map[string]any{"bot_id": b.ID, "dynamic_vars": map[string]any{}, "agent_id": b.AgentID}, "args": args}
		if f["payload_args_only"] == true {
			payload = args
		}
		if method == "POST" || method == "PUT" || method == "PATCH" {
			raw, _ := json.Marshal(payload)
			body = bytes.NewReader(raw)
		}
		if validateFunctions(map[string]any{"custom_functions": []any{f}}) != "" {
			continue // only https or loopback URLs, checked again right before the call
		}
		target := str(f, "url")
		// MEETSTREAM_MOCK_TOOL_REWRITE="https://public.example=http://127.0.0.1:8080" stands in for the
		// tunnel: an app registers its public https URL and the mock calls the local server behind it.
		if rw := os.Getenv("MEETSTREAM_MOCK_TOOL_REWRITE"); rw != "" {
			if from, to, ok := strings.Cut(rw, "="); ok && strings.HasPrefix(target, from) {
				target = to + strings.TrimPrefix(target, from)
			}
		}
		req, err := http.NewRequestWithContext(s.ctx, method, target, body)
		if err != nil {
			continue
		}
		for k, v := range obj(f, "headers") {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
		req.Header.Set("X-Bot-ID", b.ID)
		req.Header.Set("X-Agent-ID", b.AgentID)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := s.client.Do(req)
		e := map[string]any{"function": f["name"], "url": str(f, "url"), "method": method, "args": args}
		if err != nil {
			e["error"] = err.Error()
		} else {
			out, _ := io.ReadAll(io.LimitReader(res.Body, 15000))
			res.Body.Close()
			e["status"], e["response"] = res.StatusCode, string(out)
		}
		s.journalAdd(Entry{Kind: "fn", Path: "custom_function", Body: e})
	}
}

func (s *Server) chats(w http.ResponseWriter, b *bot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.Status != "Done" && b.Status != "MediaProcessing" && b.Status != "Stopped" {
		writeJSON(w, 202, "Manifest is still being processed. Current status: Processing")
		return
	}
	msgs := []any{}
	for i, t := range b.Chats {
		msgs = append(msgs, map[string]any{"messageId": fmt.Sprintf("bot-%d", i+1), "deviceId": "bot", "text": t, "speakerName": "Bot", "fromBot": true})
	}
	writeJSON(w, 200, map[string]any{"metadata": map[string]any{"totalMessages": len(msgs), "botId": b.ID}, "chatMessages": msgs})
}

func (s *Server) screenshots(w http.ResponseWriter, b *bot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := []any{}
	for i := len(b.Shots) - 1; i >= 0; i-- {
		rows = append(rows, b.Shots[i])
	}
	writeJSON(w, 200, map[string]any{"bot_id": b.ID, "screenshots": rows, "next_continuation_token": nil, "prev_continuation_token": nil})
}
