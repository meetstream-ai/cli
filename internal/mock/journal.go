package mock

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// Entry is one thing the mock saw: an API request, or a message an app sent on a bot socket.
type Entry struct {
	At     string `json:"at"`
	Kind   string `json:"kind"` // "http" or "ws"
	Method string `json:"method,omitempty"`
	Path   string `json:"path"`
	Body   any    `json:"body,omitempty"`
}

// record keeps the request body for GET /__mock/journal and gives the body back to the handler.
func (s *Server) record(r *http.Request, path string) {
	var body any
	if r.Body != nil && r.Method != http.MethodGet {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if len(raw) > 0 && json.Unmarshal(raw, &body) != nil {
			body = string(raw)
		}
	}
	s.journalAdd(Entry{Kind: "http", Method: r.Method, Path: path, Body: redact(body, false)})
}

// redact hides api_key values and the header values of custom functions in journal entries.
func redact(v any, inHeaders bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			switch {
			case inHeaders:
				out[k] = "[redacted]"
			case k == "api_key" || k == "apiKey":
				out[k] = "[redacted]"
			case k == "headers":
				out[k] = redact(x, true)
			default:
				out[k] = redact(x, false)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = redact(x, inHeaders)
		}
		return out
	}
	return v
}

func (s *Server) journalAdd(e Entry) {
	e.At = time.Now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	s.journal = append(s.journal, e)
	s.mu.Unlock()
}

// Journal returns a copy of everything recorded so far.
func (s *Server) Journal() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry{}, s.journal...) // [] (not null) when empty
}
