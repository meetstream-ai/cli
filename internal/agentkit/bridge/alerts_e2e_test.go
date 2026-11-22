package bridge

import (
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/alerts"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
)

// Ported from tests/test_rules_alerts.py::test_one_alert_per_kind_per_meeting:
// MIA gets a clean error and stays quiet in the meeting; the owner gets one
// desktop alert per kind of failure per meeting, not one per turn.
func TestOneAlertPerKindPerMeeting(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed for the stub agent")
	}
	const outOfCredits = "ERROR: Your workspace is out of credits. Ask your workspace owner to refill in order to continue."
	t.Setenv("STUB_FAIL_TEXT", outOfCredits)
	stub, _ := filepath.Abs("testdata/stub_agent.py")
	dir := t.TempDir()
	var mu sync.Mutex
	var sent [][2]string
	notify := func(title, text string) { mu.Lock(); sent = append(sent, [2]string{title, text}); mu.Unlock() }
	srv := New(config.Config{Agent: agents.All["claude"], Binary: stub, Workdir: dir, RuntimeDir: dir, Owner: "Nav", Timeout: 20 * time.Second, Notify: notify})
	store := &pairing.Store{Dir: dir}
	_, token, err := store.Create("test")
	if err != nil {
		t.Fatal(err)
	}
	srv.Pairings = store
	hs := httptest.NewServer(srv)
	t.Cleanup(func() { hs.Close(); srv.Close() })

	res := post(t, hs.URL+"/v1/session/start", token, map[string]any{"session_id": "bot1"})
	res.Body.Close()
	turn := map[string]any{"session_id": "bot1", "messages": []map[string]string{{"speaker": "Nav", "text": "hi"}}}
	for i := 0; i < 2; i++ {
		_, last := readTurn(t, post(t, hs.URL+"/v1/turn", token, turn))
		if last["type"] != "error" || last["reason"] != "out_of_credits" {
			t.Fatalf("turn %d: last event %v", i, last)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	want := [][2]string{{"MeetStream Agent Kit", alerts.Message("out_of_credits", agents.All["claude"])}}
	if !reflect.DeepEqual(sent, want) { // once, not per turn
		t.Fatalf("alerts %v, want %v", sent, want)
	}
}
