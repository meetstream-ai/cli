package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// G7: no transcript_id and a terminal bot: one GET /bots/{id}/get_transcript; 409 returns at once.
func TestGetTranscriptTerminalBot409StopsPolling(t *testing.T) {
	var botCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/detail"):
			_, _ = w.Write([]byte(`{"bot_details":{}}`))
		case strings.HasSuffix(r.URL.Path, "/transcriptions"):
			_, _ = w.Write([]byte(`{"transcriptions":[]}`))
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"status":"NotAllowed"}`))
		case strings.HasSuffix(r.URL.Path, "/bots/b1/get_transcript"):
			atomic.AddInt32(&botCalls, 1)
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"message":"The bot was not admitted to the meeting, so there is no transcript."}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	start := time.Now()
	_, err := New(srv.URL, "k", "test").GetTranscript(context.Background(), "b1", TranscriptOpt{Wait: true, Timeout: 5 * time.Second, Interval: 10 * time.Millisecond})
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 409 || time.Since(start) > 2*time.Second || atomic.LoadInt32(&botCalls) != 1 {
		t.Fatalf("err=%v calls=%d after %s", err, botCalls, time.Since(start))
	}
}

// A bot still running is not asked; a terminal bot is asked once, and a 404 there keeps polling.
func TestGetTranscriptAsksBotOnceOnlyWhenTerminal(t *testing.T) {
	var botCalls int32
	status := "InMeeting"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/detail"):
			_, _ = w.Write([]byte(`{"bot_details":{}}`))
		case strings.HasSuffix(r.URL.Path, "/transcriptions"):
			_, _ = w.Write([]byte(`{"transcriptions":[]}`))
		case strings.HasSuffix(r.URL.Path, "/status"):
			_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
		case strings.HasSuffix(r.URL.Path, "/bots/b1/get_transcript"):
			atomic.AddInt32(&botCalls, 1)
			w.WriteHeader(404)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k", "test")
	opt := TranscriptOpt{Wait: true, Timeout: 100 * time.Millisecond, Interval: 10 * time.Millisecond}
	if _, err := c.GetTranscript(context.Background(), "b1", opt); err == nil || atomic.LoadInt32(&botCalls) != 0 {
		t.Fatalf("running bot: err=%v calls=%d", err, botCalls)
	}
	status = "RecordingPermissionDenied" // not terminal
	if _, err := c.GetTranscript(context.Background(), "b1", opt); err == nil || atomic.LoadInt32(&botCalls) != 0 {
		t.Fatalf("RecordingPermissionDenied: err=%v calls=%d", err, botCalls)
	}
	status = "Stopped"
	_, err := c.GetTranscript(context.Background(), "b1", opt)
	if err == nil || !strings.Contains(err.Error(), "timed out") || atomic.LoadInt32(&botCalls) != 1 {
		t.Fatalf("stopped bot: err=%v calls=%d", err, botCalls)
	}
}

// G8: 202 on a read is a NotReadyError; 507 stays a success.
func TestReadNotReady202(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/get_video") {
			w.WriteHeader(507)
			_, _ = w.Write([]byte(`{"url":"u"}`))
			return
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"message":"still processing"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", "test")
	ctx := context.Background()
	for name, fn := range map[string]func(context.Context, string) (any, error){
		"chats": c.Chats, "audio": c.BotAudio, "screenshots": c.Screenshots, "streams": c.RecordingStreams, "audio-streams": c.AudioStreams,
	} {
		v, err := fn(ctx, "b1")
		var nr *NotReadyError
		if !errors.As(err, &nr) || v != nil || nr.Detail() != "still processing" {
			t.Errorf("%s: v=%v err=%v", name, v, err)
		}
	}
	if v, err := c.BotVideo(ctx, "b1"); err != nil || v == nil {
		t.Fatalf("507: v=%v err=%v", v, err)
	}
}
