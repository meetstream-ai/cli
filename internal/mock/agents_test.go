package mock

import "testing"

func TestLiveModeDefaultsToRaw(t *testing.T) {
	p := func(prov map[string]any) map[string]any {
		return map[string]any{"recording_config": map[string]any{"transcript": map[string]any{"provider": prov}}}
	}
	if got := liveModeOf(p(map[string]any{"meetstream_streaming": map[string]any{}})); got != "raw" {
		t.Fatalf("streaming default = %q, want raw (production default)", got)
	}
	if got := liveModeOf(p(map[string]any{"deepgram_streaming": map[string]any{"transcription_mode": "sentence"}})); got != "sentence" {
		t.Fatalf("explicit mode = %q, want sentence", got)
	}
}

func TestRawModeSendsInterimThenFinal(t *testing.T) {
	posts := livePosts("b1", "Jane", "1", "Can everyone hear me today", 1, "raw")
	if len(posts) != 2 || posts[0]["is_final"] != false || posts[1]["is_final"] != true {
		t.Fatalf("raw posts = %v", posts)
	}
	if words := livePosts("b1", "Jane", "1", "one two three", 1, "word_level"); len(words) != 3 {
		t.Fatalf("word_level posts = %d, want 3", len(words))
	}
}

func TestBotIDInsertedAfterHost(t *testing.T) {
	if got := withBotID("wss://host.example/bridge/audio", "abc"); got != "wss://host.example/abc/bridge/audio" {
		t.Fatalf("got %s", got)
	}
}

func TestValidateMIAMessagesMatchProduction(t *testing.T) {
	cases := []struct {
		body map[string]any
		want string
	}{
		{map[string]any{}, "Missing agent_name."},
		{map[string]any{"agent_name": "a", "mode": "pipeline", "model": map[string]any{"provider": "openai", "model": "gpt-4o-mini"}}, "Missing model.system_prompt."},
		{map[string]any{"agent_name": "a", "mode": "pipeline", "model": map[string]any{"provider": "openai", "model": "gpt-4o-mini", "system_prompt": "x"},
			"voice": map[string]any{"provider": "openai"}, "transcriber": map[string]any{"provider": "deepgram"}}, "Missing voice.voice_id."},
		{map[string]any{"agent_name": "a", "mode": "realtime", "model": map[string]any{"provider": "openai"}}, "Missing model.system_prompt."},
		{map[string]any{"agent_name": "a", "mode": "pipeline", "model": map[string]any{"provider": "anthropic", "model": "x", "system_prompt": "x", "key_source": "meetstream"},
			"voice": map[string]any{"provider": "openai", "voice_id": "nova"}, "transcriber": map[string]any{"provider": "deepgram"}}, "model.provider 'anthropic' has no MeetStream key. Use one of: openai."},
	}
	for _, c := range cases {
		if got := validateMIA(c.body); got != c.want {
			t.Errorf("validateMIA(%v) = %q, want %q", c.body, got, c.want)
		}
	}
}

func TestLoopbackHostIsExact(t *testing.T) {
	for h, want := range map[string]bool{"127.0.0.1:3333": true, "localhost:3333": true, "[::1]:3333": true, "localhost": true,
		"localhost.evil.example:3333": false, "127.0.0.1.nip.io:80": false, "example.com": false} {
		if got := loopbackHost(h); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", h, got, want)
		}
	}
}
