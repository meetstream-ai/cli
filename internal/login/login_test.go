package login

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeAS mimics the MCP's OAuth server for the meetstream-cli client.
func fakeAS(t *testing.T, returnKey bool, knowsCLI bool) *httptest.Server {
	t.Helper()
	var challenge, redirect string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !knowsCLI || q.Get("client_id") != ClientID {
			http.Error(w, "unknown client", 400)
			return
		}
		if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad request", 400)
			return
		}
		challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
		u, _ := url.Parse(redirect)
		v := u.Query()
		v.Set("code", "mso_c_test")
		v.Set("state", q.Get("state"))
		v.Set("iss", srv.URL)
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if r.PostForm.Get("code") != "mso_c_test" || r.PostForm.Get("redirect_uri") != redirect ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.PostForm.Get("client_id") != ClientID {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		out := map[string]any{"access_token": "mso_at_x", "token_type": "Bearer"}
		if returnKey {
			out["api_key"], out["api_key_name"] = "ms_test_key_1234", "MeetStream CLI"
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	t.Cleanup(srv.Close)
	return srv
}

// browser follows the sign-in link like a browser that's already signed in.
func browser(u string) error {
	go func() {
		res, err := http.Get(u)
		if err == nil {
			res.Body.Close()
		}
	}()
	return nil
}

func TestRunGetsKey(t *testing.T) {
	as := fakeAS(t, true, true)
	var shown string
	res, err := Run(context.Background(), Options{AuthURL: as.URL, OpenBrowser: browser, ShowURL: func(u string) { shown = u }, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.APIKey != "ms_test_key_1234" || res.APIKeyName != "MeetStream CLI" {
		t.Fatalf("got %+v", res)
	}
	if !strings.Contains(shown, "client_id=meetstream-cli") || strings.Contains(shown, "ms_test_key") {
		t.Fatalf("sign-in URL %q", shown)
	}
}

func TestRunServerWithoutKeySupport(t *testing.T) {
	as := fakeAS(t, false, true)
	_, err := Run(context.Background(), Options{AuthURL: as.URL, OpenBrowser: browser, Timeout: 10 * time.Second})
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
}

func TestRunOldServerFailsFast(t *testing.T) {
	as := fakeAS(t, true, false)
	start := time.Now()
	_, err := Run(context.Background(), Options{AuthURL: as.URL, OpenBrowser: func(string) error { t.Fatal("browser opened"); return nil }, Timeout: 10 * time.Second})
	if !errors.Is(err, ErrNoKey) || time.Since(start) > 5*time.Second {
		t.Fatalf("want fast ErrNoKey, got %v after %s", err, time.Since(start))
	}
}

func TestRunRejectsWrongState(t *testing.T) {
	as := fakeAS(t, true, true)
	evil := func(u string) error {
		go func() {
			pu, _ := url.Parse(u)
			cb, _ := url.Parse(pu.Query().Get("redirect_uri"))
			q := cb.Query()
			q.Set("code", "mso_c_test")
			q.Set("state", "forged")
			cb.RawQuery = q.Encode()
			if res, err := http.Get(cb.String()); err == nil {
				res.Body.Close()
			}
		}()
		return nil
	}
	if _, err := Run(context.Background(), Options{AuthURL: as.URL, OpenBrowser: evil, Timeout: 10 * time.Second}); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("want state error, got %v", err)
	}
}
