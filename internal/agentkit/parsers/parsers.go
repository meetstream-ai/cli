package parsers

import (
	"encoding/json"
	"regexp"
	"strings"
)

type Parsed struct {
	Delta, Final, Error string
	HasFinal, HasError  bool
}

var auth = regexp.MustCompile(`(?i)(failed to authenticate|oauth|not logged in|login expired|authentication required|log ?in required|please (log|sign) ?in|authentication (failed|required|expired)|session expired|unauthori[sz]ed|invalid api key|credentials? (expired|missing|not found))`)

func AuthError(s string) bool { return auth.MatchString(s) }
func Classify(s string) string {
	if AuthError(s) {
		return "auth_required"
	}
	return "agent_error"
}
func Obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func Str(v any) string { s, _ := v.(string); return s }
func Parse(parser, line string) Parsed {
	if parser == "text" {
		return Parsed{Delta: line}
	}
	var e map[string]any
	if json.Unmarshal([]byte(line), &e) != nil {
		return Parsed{}
	}
	kind := Str(e["type"])
	switch parser {
	case "claude_stream_json":
		if kind == "stream_event" {
			inner := Obj(e["event"])
			d := Obj(inner["delta"])
			if inner["type"] == "content_block_delta" && d["type"] == "text_delta" {
				return Parsed{Delta: Str(d["text"])}
			}
		}
		if kind == "result" {
			if e["is_error"] == true {
				msg := Str(e["result"])
				if msg == "" {
					msg = Str(e["subtype"])
				}
				if msg == "" {
					msg = "agent error"
				}
				return Parsed{Error: msg, HasError: true}
			}
			return Parsed{Final: Str(e["result"]), HasFinal: true}
		}
	case "codex_jsonl":
		item := Obj(e["item"])
		if kind == "item.completed" && (item["type"] == "agent_message" || item["type"] == "assistant_message") {
			return Parsed{Delta: Str(item["text"])}
		}
		if kind == "turn.failed" || kind == "error" {
			msg := Str(Obj(e["error"])["message"])
			if msg == "" {
				msg = Str(e["message"])
			}
			if msg == "" {
				msg = "agent error"
			}
			return Parsed{Error: msg, HasError: true}
		}
	case "agy_stream_json":
		if s := Str(e["event"]); s != "" {
			kind = s
		}
		if kind == "step_update" {
			step := Obj(e["step_update"])
			if len(step) == 0 {
				step = e
			}
			if step["step_type"] == "agent_response" {
				return Parsed{Delta: Str(step["text_delta"])}
			}
		}
		if kind == "result" {
			result := Obj(e["result"])
			if len(result) == 0 {
				result = e
			}
			status := strings.ToLower(Str(result["status"]))
			switch status {
			case "error", "failed", "failure", "canceled", "cancelled", "interrupted", "invalid":
				msg := Str(result["error"])
				if msg == "" {
					msg = Str(result["response"])
				}
				if msg == "" {
					msg = "agent error"
				}
				return Parsed{Error: msg, HasError: true}
			}
			return Parsed{Final: Str(result["response"]), HasFinal: true}
		}
	}
	return Parsed{}
}
