package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOptOut(t *testing.T) {
	t.Setenv("MEETSTREAM_TELEMETRY_TEST", "1")
	for _, tc := range []struct{ k, v string }{{"MEETSTREAM_TELEMETRY", "0"}, {"MEETSTREAM_TELEMETRY", "off"}, {"DO_NOT_TRACK", "1"}, {"DO_NOT_TRACK", "true"}} {
		t.Run(tc.k+"="+tc.v, func(t *testing.T) {
			t.Setenv(tc.k, tc.v)
			if Enabled() {
				t.Fatal("still enabled")
			}
		})
	}
}

func TestOffInTestsByDefault(t *testing.T) {
	t.Setenv("MEETSTREAM_TELEMETRY_TEST", "")
	if Enabled() {
		t.Fatal("tests must never send events")
	}
}

func TestTrackSendsOnlyNameAndVersion(t *testing.T) {
	t.Setenv("MEETSTREAM_TELEMETRY_TEST", "1")
	got := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		got <- m
	}))
	defer srv.Close()
	old := Endpoint
	Endpoint = srv.URL
	defer func() { Endpoint = old }()

	Track("cli_command_run", map[string]any{"command": "join", "version": "1.0.0"}, 2*time.Second)()
	m := <-got
	if m["event"] != "cli_command_run" || m["api_key"] != Key || !strings.HasPrefix(m["distinct_id"].(string), "anon_") {
		t.Fatalf("payload %v", m)
	}
	p := m["properties"].(map[string]any)
	if p["command"] != "join" || p["version"] != "1.0.0" || p["$lib"] != "meetstream-cli" {
		t.Fatalf("properties %v", p)
	}
	for k := range p {
		switch k {
		case "command", "command_path", "version", "$lib", "os", "arch":
		default:
			t.Fatalf("unexpected property %q", k)
		}
	}
}

func TestTrackNeverBlocksPastTimeout(t *testing.T) {
	t.Setenv("MEETSTREAM_TELEMETRY_TEST", "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) }))
	defer srv.Close()
	old := Endpoint
	Endpoint = srv.URL
	defer func() { Endpoint = old }()
	start := time.Now()
	Track("cli_command_run", nil, 200*time.Millisecond)()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("waited %v", d)
	}
}
