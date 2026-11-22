package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/api"
	"github.com/spf13/cobra"
)

func TestM3BPasswordInput(t *testing.T) {
	for _, platform := range []string{"teams"} {
		t.Run(platform, func(t *testing.T) {
			// Synthetic input only; failure messages must not include request bodies.
			input, err := os.CreateTemp(t.TempDir(), "stdin")
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if _, err = input.WriteString("synthetic-input\r\n"); err != nil {
				t.Fatal(err)
			}
			if _, err = input.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			oldIn := os.Stdin
			os.Stdin = input
			defer func() { os.Stdin = oldIn }()
			calls := 0
			oldTransport := http.DefaultTransport
			http.DefaultTransport = m3Transport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.RequestURI() != "/teams-logins" || r.Header.Get("Authorization") != "Token test-key" {
					t.Error("incorrect request method, path or auth")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["domain"] != "example.invalid" || body["email"] != "bot@example.invalid" || body["password"] != "synthetic-input" || len(body) != 3 {
					t.Error("incorrect login request body")
				}
				io.WriteString(w, `{"login_id":"l1","password":"synthetic-input","message":"echo synthetic-input"}`)
			})}
			defer func() { http.DefaultTransport = oldTransport }()
			args := []string{"logins", "teams"}
			args = append(args, "add", "--domain", "example.invalid", "--email", "bot@example.invalid", "--password-stdin", "--json")
			out, code := run(t, map[string]string{"MEETSTREAM_API_KEY": "test-key", "MEETSTREAM_API_URL": "http://meetstream.test"}, args...)
			if code != 0 || calls != 1 || strings.Contains(out, "synthetic-input") || !strings.Contains(out, "l1") {
				t.Fatal("login result or secret redaction incorrect")
			}
		})
	}
}

func TestM3BUsageAndPassword(t *testing.T) {
	t.Setenv("MEETSTREAM_LOGIN_PASSWORD", "")
	env, calls := m3Server(t, nil)
	for _, args := range [][]string{
		{"logins", "teams", "add", "--domain", "example.invalid", "--email", "bot@example.invalid", "--no-input"},
		{"logins", "teams", "remove-domain", "example.invalid", "--no-input"},
		{"logins", "google", "remove", "l1", "--domain", "example.invalid", "--no-input"},
		{"bots", "tail", "--no-input"},
	} {
		_, code := run(t, env, args...)
		if code != 2 {
			t.Errorf("%v returned %d", args, code)
		}
	}
	if *calls != 0 {
		t.Fatal("usage error sent a request")
	}
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("\n"))
	g = globals{NoInput: true}
	if _, err := loginPassword(cmd, true); err == nil {
		t.Fatal("empty password accepted")
	}
	t.Setenv("MEETSTREAM_LOGIN_PASSWORD", "synthetic-env")
	if value, err := loginPassword(cmd, false); value != "synthetic-env" || err != nil {
		t.Fatal("environment password unavailable in no-input mode")
	}
	cmd.SetIn(strings.NewReader("synthetic-stdin\n"))
	if value, err := loginPassword(cmd, true); value != "synthetic-stdin" || err != nil {
		t.Fatal("stdin password must win")
	}
}

func TestM3BCalendarAndLogins(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		route m3Route
	}{
		{[]string{"calendar", "schedule", "e1"}, m3Route{"POST", "/calendar/schedule/e1", nil, 200, `{"bot_id":"b1"}`}},
		{[]string{"calendar", "reschedule", "b1", "2027-01-01T01:00:00Z"}, m3Route{"PATCH", "/calendar/scheduled_bots/b1", map[string]any{"scheduled_join_time": "2027-01-01T01:00:00Z"}, 200, `{}`}},
		{[]string{"calendar", "auto-join", "enable", "--video", "-n", "Test"}, m3Route{"POST", "/calendar/auto-schedule/enable", map[string]any{"default_bot_config": map[string]any{"bot_name": "Test", "audio_required": true, "video_required": true, "automatic_leave": map[string]any{"everyone_left_timeout": 60}}}, 200, `{}`}},
		{[]string{"logins", "teams", "add-domain", "example.invalid"}, m3Route{"POST", "/teams-login-domains", map[string]any{"domain": "example.invalid", "login_mode": "always"}, 200, `{}`}},
		{[]string{"logins", "google", "add-domain", "example.invalid", "--login-mode", "if_required"}, m3Route{"POST", "/google-login-domains", map[string]any{"sso_workspace_domain": "example.invalid", "login_mode": "if_required"}, 200, `{}`}},
		{[]string{"logins", "google", "disable", "g1", "--domain", "example.invalid"}, m3Route{"PATCH", "/google-logins/g1", map[string]any{"domain": "example.invalid", "is_active": false}, 200, `{}`}},
		{[]string{"logins", "google", "remove", "g1", "--domain", "example.invalid", "--yes"}, m3Route{"DELETE", "/google-logins/g1?domain=example.invalid", nil, 200, `{}`}},
	} {
		t.Run(strings.Join(tc.args[:2], "/"), func(t *testing.T) {
			env, calls := m3Server(t, []m3Route{tc.route})
			_, code := run(t, env, append(tc.args, "--json")...)
			if code != 0 || *calls != 1 {
				t.Fatalf("code=%d calls=%d", code, *calls)
			}
		})
	}
}

func TestM3BMiaApply(t *testing.T) {
	for _, tc := range []struct{ body, method, expected string }{
		{`{"agent_name":"Test"}`, "POST", `{"agent_name":"Test"}`},
		{`{"id":"a1","agent_name":"Test"}`, "PUT", `{"agent_config_id":"a1","agent_name":"Test"}`},
		{`{"agent_config_id":"a1","agent_name":"Test"}`, "PUT", `{"agent_config_id":"a1","agent_name":"Test"}`},
	} {
		t.Run(tc.method+tc.body[:min(7, len(tc.body))], func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "mia.json")
			if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			var body any
			_ = json.Unmarshal([]byte(tc.expected), &body)
			env, calls := m3Server(t, []m3Route{{tc.method, "/mia", body, 200, `{"agent_config_id":"a1"}`}})
			out, code := run(t, env, "agents", "apply", "-f", file, "--json")
			if code != 0 || *calls != 1 || !strings.Contains(out, "a1") {
				t.Fatalf("code=%d calls=%d", code, *calls)
			}
			env, calls = m3Server(t, nil)
			out, code = run(t, env, "mia", "apply", "-f", file, "--dry-run")
			var got any
			if code != 0 || *calls != 0 || json.Unmarshal([]byte(out), &got) != nil || !reflect.DeepEqual(got, body) {
				t.Fatal("dry-run must print JSON without a request")
			}
		})
	}
}

func TestM3BMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".cursor", "mcp.json")
	out, code := run(t, nil, "mcp", "add", "cursor", "--json")
	if code != 0 || !strings.Contains(out, hostedMCPURL) {
		t.Fatal("print failed")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("print must not write config")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte(`{"other":true,"mcpServers":{"existing":{"url":"http://example.invalid"}}}`)
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	_, code = run(t, nil, "mcp", "add", "cursor", "--write", "--json")
	backup, err := os.ReadFile(path + ".bak")
	if code != 0 || err != nil || !bytes.Equal(backup, old) {
		t.Fatal("backup missing or changed")
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if json.Unmarshal(updated, &body) != nil || body["other"] != true || object(body["mcpServers"])["existing"] == nil || object(object(body["mcpServers"])["meetstream"])["url"] != hostedMCPURL {
		t.Fatal("config merge lost data")
	}
	if strings.Contains(string(updated), "Authorization") || strings.Contains(string(updated), "api_key") {
		t.Fatal("MCP config must use OAuth")
	}
	before := string(updated)
	_, code = run(t, nil, "mcp", "add", "cursor", "--write", "--json")
	after, _ := os.ReadFile(path)
	if code == 0 || string(after) != before {
		t.Fatal("existing backup must not be overwritten")
	}
}

func TestM3BCodexMerge(t *testing.T) {
	old := []byte("model = \"test\"\n[mcp_servers.other]\nurl = \"http://example.invalid\"\n[mcp_servers.meetstream]\nurl = \"old\"\n[features]\nflag = true\n")
	updated, err := mergeCodexMCP(old)
	if err != nil || !strings.Contains(string(updated), "[features]\nflag = true") || !strings.Contains(string(updated), "[mcp_servers.other]") || strings.Count(string(updated), codexMCPSection) != 1 || strings.Contains(string(updated), "\"old\"") {
		t.Fatal("TOML merge lost data or duplicated target")
	}
}

func TestM3BWebhookHandler(t *testing.T) {
	var out, diagnostics bytes.Buffer
	handler := webhookHandler("/custom", "", true, &out, &diagnostics)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/custom", strings.NewReader(`{"event":"bot.done"}`)))
	if recorder.Code != 200 || recorder.Body.String() != `{"status":"ok"}` || out.String() != "{\"event\":\"bot.done\"}\n" {
		t.Fatal("webhook acknowledgment or JSON line incorrect")
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/webhook", strings.NewReader(`{}`)))
	if recorder.Code != 404 {
		t.Fatal("custom path ignored")
	}
}

func TestM3BListenSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, diagnostics bytes.Buffer
	handled := make(chan struct{})
	handler := webhookHandler("/webhook", "", true, &out, &diagnostics)
	done := make(chan error, 1)
	go func() {
		done <- serveWebhooks(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r); close(handled) }))
	}()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Post("http://"+ln.Addr().String()+"/webhook", "application/json", strings.NewReader(`{"event":"bot.done"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("event not handled")
	}
	cancel()
	select {
	case err := <-done:
		var ee *ExitError
		if !errors.As(err, &ee) || ee.Code != 130 {
			t.Fatal("shutdown must exit 130")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("shutdown did not complete")
	}
	if out.String() != "{\"event\":\"bot.done\"}\n" {
		t.Fatal("event missing")
	}
}

func TestM3BCredentialError(t *testing.T) {
	for _, input := range []string{"synthetic-input", strings.Repeat("synthetic", 60), "synthetic\ninput"} {
		encoded, _ := json.Marshal(map[string]any{"message": input})
		_, err := safeCredentialResult(nil, &api.Error{Status: 403, Body: string(encoded)}, map[string]any{"password": input})
		var ee *ExitError
		if !errors.As(err, &ee) || ee.Code != 2 || strings.Contains(err.Error(), "synthetic") {
			t.Fatal("credential redaction lost exit code or exposed input")
		}
	}
}

func TestM3BGoogleCertificate(t *testing.T) {
	dir := t.TempDir()
	key, cert := filepath.Join(dir, "key.pem"), filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(key, []byte("synthetic-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cert, []byte("synthetic-cert"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args  []string
		route m3Route
	}{
		{[]string{"add", "--email", "bot@example.invalid", "--inactive"}, m3Route{"POST", "/google-logins", map[string]any{"domain": "example.invalid", "email": "bot@example.invalid", "sso_private_key_pem": "synthetic-key", "sso_cert_pem": "synthetic-cert", "is_active": false}, 200, `{"login_id":"g1"}`}},
		{[]string{"set-cert", "g1"}, m3Route{"PATCH", "/google-logins/g1", map[string]any{"domain": "example.invalid", "sso_private_key_pem": "synthetic-key", "sso_cert_pem": "synthetic-cert"}, 200, `{"login_id":"g1"}`}},
	} {
		env, calls := m3Server(t, []m3Route{tc.route})
		args := append([]string{"logins", "google"}, tc.args...)
		args = append(args, "--domain", "example.invalid", "--key", key, "--cert", cert, "--json")
		out, code := run(t, env, args...)
		if code != 0 || *calls != 1 || strings.Contains(out, "synthetic-") {
			t.Fatal("SSO request or output incorrect")
		}
	}
}

func TestM3BCalendarConnect(t *testing.T) {
	env, calls := m3Server(t, []m3Route{{"POST", "/calendar/create_calendar", map[string]any{"google_client_id": "synthetic-id", "google_client_secret": "synthetic-secret", "google_refresh_token": "synthetic-token"}, 200, `{"connected":true,"google_refresh_token":"synthetic-token"}`}})
	out, code := run(t, env, "calendar", "connect", "--own-app", "--client-id", "synthetic-id", "--client-secret", "synthetic-secret", "--refresh-token", "synthetic-token", "--json")
	if code != 0 || *calls != 1 || strings.Contains(out, "synthetic-token") || strings.Contains(out, "synthetic-secret") {
		t.Fatal("calendar connect request or redaction incorrect")
	}
}

func TestM3BCodexWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte("model = \"synthetic-model\"\n[features]\nflag = true\n")
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	_, code := run(t, nil, "mcp", "add", "codex", "--write", "--json")
	updated, err := os.ReadFile(path)
	if code != 0 || err != nil || !strings.Contains(string(updated), string(old)) || !strings.Contains(string(updated), hostedMCPURL) {
		t.Fatal("Codex write failed to preserve config")
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || !bytes.Equal(backup, old) {
		t.Fatal("Codex backup missing")
	}
	for _, raw := range []string{"model = \"\"\"multiline\nvalue\"\"\"", "[mcp_servers.\"meetstream\"]\nurl=\"old\"", "mcp_servers = {}"} {
		if _, err := mergeCodexMCP([]byte(raw)); err == nil {
			t.Fatal("ambiguous TOML must require manual merging")
		}
	}
}

func TestMiaApplyReadsYAML(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/mia.yaml"
	if err := os.WriteFile(p, []byte("id: cfg_7\nagent_name: Standup helper\nmodel:\n  provider: openai\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := run(t, map[string]string{"MEETSTREAM_API_KEY": "test-key"}, "mia", "apply", "-f", p, "--dry-run")
	if code != 0 || !strings.Contains(out, `"agent_config_id":"cfg_7"`) || !strings.Contains(out, `"provider":"openai"`) || strings.Contains(out, `"id":`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
}
