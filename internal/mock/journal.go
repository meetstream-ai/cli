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
	s.journalAdd(Entry{Kind: "http", Method: r.Method, Path: path, Body: body})
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
	return append([]Entry(nil), s.journal...)
}
