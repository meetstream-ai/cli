package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func TestStartCalendarOAuth(t *testing.T) {
	var got map[string]any
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/calendar/oauth/start" || r.Header.Get("Authorization") != "Token test-key" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"auth_url":"https://accounts.example.test/auth?state=x","session_id":"s1","expires_at":1900000000}`))
	})
	res, err := c.StartCalendarOAuth(context.Background(), "google", "accepted", nil)
	if err != nil || res.SessionID != "s1" || res.ExpiresAt != 1900000000 || res.AuthURL != "https://accounts.example.test/auth?state=x" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if want := map[string]any{"provider": "google", "join_rule": "accepted"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}

	_, _ = c.StartCalendarOAuth(context.Background(), "microsoft", "all", map[string]any{"bot_name": "MIA"})
	if want := map[string]any{"provider": "microsoft", "join_rule": "all", "default_bot_config": map[string]any{"bot_name": "MIA"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("body %v", got)
	}
}

func TestStartCalendarOAuthErrors(t *testing.T) {
	notFound := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"Not found"}`))
	})
	_, err := notFound.StartCalendarOAuth(context.Background(), "google", "accepted", nil)
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 404 {
		t.Fatalf("want 404 *Error, got %v", err)
	}
	for _, body := range []string{`{"auth_url":"javascript:alert(1)","session_id":"s"}`, `{"auth_url":"https://x.test/a"}`, `{}`} {
		bad := fakeClient(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		if _, err := bad.StartCalendarOAuth(context.Background(), "google", "accepted", nil); err == nil {
			t.Fatalf("accepted bad answer %s", body)
		}
	}
}

func TestCalendarOAuthStatus(t *testing.T) {
	c := fakeClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/calendar/oauth/status?session_id=a%2Bb" {
			t.Errorf("got %s %s", r.Method, r.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`{"status":"connected","provider":"google","account_email":"a@example.test"}`))
	})
	st, err := c.CalendarOAuthStatus(context.Background(), "a+b")
	if err != nil || st.Status != "connected" || st.Provider != "google" || st.AccountEmail != "a@example.test" {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}
