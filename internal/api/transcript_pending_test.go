package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The API answers 202 with a JSON message while a transcript is processing; that is not a transcript.
func TestGetTranscriptTreats202AsPending(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/detail"):
			_, _ = w.Write([]byte(`{"bot_details":{"transcript_id":"t1"}}`))
		case strings.Contains(r.URL.Path, "/transcript/t1/get_transcript"):
			if atomic.AddInt32(&calls, 1) < 3 {
				w.WriteHeader(202)
				_, _ = w.Write([]byte(`{"message":"Transcript is still being processed. Current status: processing"}`))
				return
			}
			_, _ = w.Write([]byte(`[{"speaker":"A","transcript":"hello"}]`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k", "test")
	res, err := c.GetTranscript(context.Background(), "b1", TranscriptOpt{Wait: true, Timeout: 5 * time.Second, Interval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	arr, ok := res.Transcript.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("202 message was taken as the transcript: %#v", res.Transcript)
	}
	res, err = c.GetTranscript(context.Background(), "b1", TranscriptOpt{Wait: false})
	if err != nil || res.Transcript == nil {
		t.Fatalf("ready transcript: %v %#v", err, res)
	}
}

func TestGetTranscriptNoWait202IsNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/detail") {
			_, _ = w.Write([]byte(`{"bot_details":{"transcript_id":"t1"}}`))
			return
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"message":"processing"}`))
	}))
	defer srv.Close()
	res, err := New(srv.URL, "k", "test").GetTranscript(context.Background(), "b1", TranscriptOpt{Wait: false})
	if err != nil || res.Transcript != nil {
		t.Fatalf("expected not ready, got %v %#v", err, res)
	}
}
