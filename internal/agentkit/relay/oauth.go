// Package relay connects this computer out to MeetStream's agent relay
// , so MIA reaches the local bridge with no tunnel and no pairing key.
//
// Sign-in is the hosted MCP's OAuth with the static public client
// "meetstream-agent-kit" and scope "local_agent" (wire contract:
// agent_relay/PROTOCOL.md). The refresh token rotates on every use, so the
// new one is saved before the old one could be needed again. The access
// token lives in memory only. Tokens are never logged or put in argv.
package relay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	ClientID        = "meetstream-agent-kit"
	Scope           = "local_agent"
	DefaultAuthURL  = "https://mcp.meetstream.ai"
	DefaultRelayURL = "wss://relay.meetstream.ai/v1/connect"
	KitVersion      = "0.4.0-go"
)

// AuthURL and RelayURL honour the same overrides as the kit.
func AuthURL() string { return envOr("MEETSTREAM_AUTH_URL", DefaultAuthURL) }
func RelayURL() string {
	return envOr("MEETSTREAM_RELAY_URL", DefaultRelayURL)
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return def
}

// Tokens is one token response.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	DeviceID     string
}

// ErrSignInRequired means the refresh token is dead: sign in again.
var ErrSignInRequired = errors.New("MeetStream sign-in is no longer valid")

var httpClient = &http.Client{Timeout: 15 * time.Second}

// DeviceName is the name shown on the consent screen and in the dashboard.
func DeviceName() string {
	name := ""
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			name = strings.TrimSpace(string(out))
		}
	}
	if name == "" {
		name, _ = os.Hostname()
	}
	name = strings.Join(strings.Fields(name), " ")
	if len(name) > 40 {
		name = name[:40]
	}
	if name == "" {
		name = "my computer"
	}
	return name
}

// Platform is "macOS", "Linux" or "Windows".
func Platform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	}
	return runtime.GOOS
}

func randURL(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Discover checks that the server offers the local_agent scope.
func Discover(ctx context.Context, authURL string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, authURL+"/.well-known/oauth-authorization-server", nil)
	res, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", authURL, unwrapURL(err))
	}
	defer res.Body.Close()
	var meta struct {
		Scopes []string `json:"scopes_supported"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&meta) != nil {
		return fmt.Errorf("%s did not answer OAuth discovery (%d)", authURL, res.StatusCode)
	}
	for _, s := range meta.Scopes {
		if s == Scope {
			return nil
		}
	}
	return fmt.Errorf("the relay sign-in isn't switched on at %s yet", authURL)
}

// LoginOptions tune the browser flow.
type LoginOptions struct {
	AuthURL     string
	OpenBrowser func(string) error
	ShowURL     func(string)
	Timeout     time.Duration
}

// Login runs the browser sign-in and returns the tokens.
func Login(ctx context.Context, o LoginOptions) (*Tokens, error) {
	if o.AuthURL == "" {
		o.AuthURL = AuthURL()
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Minute
	}
	if err := Discover(ctx, o.AuthURL); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	verifier := randURL(32)
	sum := sha256.Sum256([]byte(verifier))
	state := randURL(16)
	q := url.Values{
		"response_type": {"code"}, "client_id": {ClientID}, "redirect_uri": {redirect},
		"scope": {Scope}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"device_name": {DeviceName()}, "platform": {Platform()}, "kit_version": {KitVersion},
	}
	signIn := o.AuthURL + "/oauth/authorize?" + q.Encode()

	type answer struct {
		code string
		err  error
	}
	got := make(chan answer, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		v := r.URL.Query()
		a := answer{code: v.Get("code")}
		switch {
		case v.Get("state") != state:
			a = answer{err: errors.New("the sign-in answer did not match this request")}
		case v.Get("error") != "":
			a = answer{err: fmt.Errorf("sign-in was not completed: %s", v.Get("error"))}
		case a.code == "":
			a = answer{err: errors.New("the sign-in answer had no code")}
		}
		page(w, a.err)
		select {
		case got <- a:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	if o.ShowURL != nil {
		o.ShowURL(signIn)
	}
	if o.OpenBrowser != nil {
		_ = o.OpenBrowser(signIn)
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	var a answer
	select {
	case a = <-got:
	case <-ctx.Done():
		return nil, errors.New("timed out waiting for the browser sign-in")
	}
	if a.err != nil {
		return nil, a.err
	}
	return tokenRequest(ctx, o.AuthURL, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {ClientID}, "code": {a.code},
		"redirect_uri": {redirect}, "code_verifier": {verifier},
	}, "")
}

// Refresh swaps a refresh token for new tokens (the old refresh token dies).
func Refresh(ctx context.Context, authURL, refreshToken, deviceID string) (*Tokens, error) {
	return tokenRequest(ctx, authURL, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {ClientID}, "refresh_token": {refreshToken},
	}, deviceID)
}

// Revoke tells the server to drop this device's grant. Best effort.
func Revoke(ctx context.Context, authURL, token string) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, authURL+"/oauth/revoke",
		strings.NewReader(url.Values{"token": {token}, "client_id": {ClientID}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if res, err := httpClient.Do(req); err == nil {
		res.Body.Close()
	}
}

func tokenRequest(ctx context.Context, authURL string, form url.Values, fallbackDevice string) (*Tokens, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, authURL+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", authURL, unwrapURL(err))
	}
	defer res.Body.Close()
	var body struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		ExpiresIn    float64 `json:"expires_in"`
		DeviceID     string  `json:"meetstream_device_id"`
		Error        string  `json:"error"`
		Description  string  `json:"error_description"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body)
	if res.StatusCode != 200 {
		if body.Error == "invalid_grant" {
			return nil, fmt.Errorf("%w (%s)", ErrSignInRequired, firstOf(body.Description, body.Error))
		}
		return nil, fmt.Errorf("sign-in server answered %d: %s", res.StatusCode, firstOf(body.Description, body.Error, "no details"))
	}
	if body.AccessToken == "" || body.RefreshToken == "" {
		return nil, errors.New("the sign-in server returned no tokens")
	}
	if body.ExpiresIn <= 0 {
		body.ExpiresIn = 900
	}
	t := &Tokens{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken,
		ExpiresAt: time.Now().Add(time.Duration(body.ExpiresIn * float64(time.Second))), DeviceID: firstOf(body.DeviceID, fallbackDevice)}
	return t, nil
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func page(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	title, body, color := "This computer is connected", "MIA can now reach your agent. You can close this tab.", "#f97316"
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		title, body, color = "Sign-in didn't finish", html.EscapeString(err.Error()), "#ef4444"
	}
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>MeetStream</title>
<body style="font:16px system-ui,sans-serif;background:#0f1115;color:#e5e7eb;display:grid;place-items:center;min-height:100vh;margin:0">
<div style="text-align:center;max-width:28rem;padding:2rem"><div style="font-size:2rem;color:%s">◆</div><h1 style="font-size:1.4rem">%s</h1><p style="color:#9ca3af">%s</p></div>`, color, title, body)
}
