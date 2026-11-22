// Package login signs in through the browser with the hosted MCP's OAuth
// server (mcp.meetstream.ai): authorization code + PKCE over a 127.0.0.1
// loopback redirect, as the fixed public client "meetstream-cli".
//
// The dashboard mints a named API key during consent, and the code exchange
// returns it to this client only (the first-party meetstream-cli client).
// The key is stored, never printed.
package login

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
	ClientID   = "meetstream-cli"
	DefaultURL = "https://mcp.meetstream.ai"
)

// AuthURL is $MEETSTREAM_AUTH_URL or the hosted MCP.
func AuthURL() string {
	if u := os.Getenv("MEETSTREAM_AUTH_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return DefaultURL
}

// Result is what the sign-in produced.
type Result struct {
	APIKey     string
	APIKeyName string
}

// ErrNoKey means the server signed the user in but does not hand keys to the
// CLI yet (it predates the CLI client).
var ErrNoKey = errors.New("the sign-in server did not return an API key")

// Options tune the flow; zero values are fine.
type Options struct {
	AuthURL     string
	OpenBrowser func(string) error // default: the OS opener
	ShowURL     func(string)       // always called with the sign-in URL
	Timeout     time.Duration      // default 5 minutes
	HTTP        *http.Client
}

func randURL(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Run does the whole flow and returns the key.
func Run(ctx context.Context, o Options) (*Result, error) {
	if o.AuthURL == "" {
		o.AuthURL = AuthURL()
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if o.OpenBrowser == nil {
		o.OpenBrowser = Open
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("could not open a local port for the sign-in: %w", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)

	verifier := randURL(48) // 64 chars, within PKCE's 43-128
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randURL(24)

	q := url.Values{
		"response_type": {"code"}, "client_id": {ClientID}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
		"state": {state}, "resource": {o.AuthURL + "/mcp"},
	}
	signInURL := o.AuthURL + "/oauth/authorize?" + q.Encode()

	// Preflight: a sign-in server without the CLI client answers 4xx here, so don't send the user to a dead end.
	pre := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if res, err := pre.Get(signInURL); err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // drop the URL: it carries the state and challenge
		}
		return nil, fmt.Errorf("could not reach %s: %w", o.AuthURL, err)
	} else {
		res.Body.Close()
		if res.StatusCode >= 400 {
			return nil, ErrNoKey
		}
	}

	type answer struct {
		code string
		err  error
	}
	got := make(chan answer, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query()
		a := answer{}
		switch {
		case v.Get("state") != state:
			a.err = errors.New("the sign-in answer did not match this request (state); run meetstream login again")
		case v.Get("error") != "":
			a.err = fmt.Errorf("sign-in was not completed: %s", firstNonEmpty(v.Get("error_description"), v.Get("error")))
		case v.Get("iss") != "" && strings.TrimRight(v.Get("iss"), "/") != o.AuthURL:
			a.err = errors.New("the sign-in answer came from an unexpected server (iss)")
		case v.Get("code") == "":
			a.err = errors.New("the sign-in answer had no code")
		default:
			a.code = v.Get("code")
		}
		page(w, a.err)
		select {
		case got <- a:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	if o.ShowURL != nil {
		o.ShowURL(signInURL)
	}
	_ = o.OpenBrowser(signInURL) // the URL is shown too, so a failed open is not fatal

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
	return exchange(ctx, o, a.code, redirect, verifier)
}

func exchange(ctx context.Context, o Options, code, redirect, verifier string) (*Result, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {ClientID},
		"redirect_uri": {redirect}, "code_verifier": {verifier},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, o.AuthURL+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := o.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not finish the sign-in: %w", err)
	}
	defer res.Body.Close()
	var tok struct {
		APIKey     string `json:"api_key"`
		APIKeyName string `json:"api_key_name"`
		Error      string `json:"error"`
		Desc       string `json:"error_description"`
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	_ = json.Unmarshal(raw, &tok)
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sign-in token exchange failed (%d): %s", res.StatusCode, firstNonEmpty(tok.Desc, tok.Error, "no details"))
	}
	if tok.APIKey == "" {
		return nil, ErrNoKey
	}
	return &Result{APIKey: tok.APIKey, APIKeyName: tok.APIKeyName}, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func page(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	title, body, color := "You're signed in", "You can close this tab and go back to your terminal.", "#f97316"
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		title, body, color = "Sign-in didn't finish", html.EscapeString(err.Error()), "#ef4444"
	}
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>MeetStream CLI</title>
<body style="font:16px system-ui,sans-serif;background:#0f1115;color:#e5e7eb;display:grid;place-items:center;min-height:100vh;margin:0">
<div style="text-align:center;max-width:28rem;padding:2rem"><div style="font-size:2rem;color:%s">◆</div><h1 style="font-size:1.4rem">%s</h1><p style="color:#9ca3af">%s</p></div>`, color, title, body)
}

// Open opens url in the default browser.
func Open(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}
