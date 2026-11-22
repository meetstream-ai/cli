package alerts

import (
	"context"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var credits = regexp.MustCompile(`(?i)out of credits|insufficient (?:credits|quota|balance|funds)|exceeded your current quota|quota exceeded|credit balance is too low|payment required|usage limit (?:reached|exceeded)|refill`)
var rate = regexp.MustCompile(`(?i)rate.?limit|too many requests|\b429\b|overloaded`)

func Kind(e map[string]any) string {
	if e["type"] == "done" {
		if e["timed_out"] == true {
			return "timeout"
		}
		return ""
	}
	if e["type"] != "error" {
		return ""
	}
	switch e["code"] {
	case "auth_required", "cancelled", "busy":
		return ""
	}
	m, _ := e["message"].(string)
	if credits.MatchString(m) {
		return "out_of_credits"
	}
	if rate.MatchString(m) {
		return "rate_limited"
	}
	if e["code"] == "timeout" {
		return "timeout"
	}
	if strings.Contains(m, "exited") {
		return "crashed"
	}
	return "failed"
}

// Message is one or two short sentences: what happened, that MIA is quiet, what
// to do. detail (the agent's error text) only shows for a plain failure.
func Message(kind string, a agents.Spec, detail ...string) string {
	fix := "check your agent or switch agents"
	if a.Relogin != "" {
		fix = "run `" + a.Relogin + "` or switch agents"
	}
	switch kind {
	case "out_of_credits":
		return a.Label + " ran out of credits. MIA is quiet in your meeting; " + fix + "."
	case "rate_limited":
		return a.Label + " is rate limited. MIA is quiet until it recovers; wait or switch models."
	case "timeout":
		return a.Label + " took too long, so the turn was stopped. MIA told the meeting."
	case "crashed":
		return a.Label + " stopped unexpectedly. The bridge starts it again on the next turn."
	}
	d := []rune(strings.Join(detail, ""))
	if len(d) > 120 {
		d = d[:120]
	}
	return a.Label + " failed: " + string(d) + ". MIA is quiet in your meeting."
}

// Runner runs one notifier command; tests swap it so no real notification fires.
type Runner func(ctx context.Context, name string, args ...string) error

var (
	goos            = runtime.GOOS
	lookPath        = exec.LookPath
	run      Runner = func(ctx context.Context, name string, args ...string) error {
		return exec.CommandContext(ctx, name, args...).Run()
	}
)

// command is the notifier this machine would run, or nil when there is none:
// osascript on macOS, otherwise notify-send when it is on PATH.
func command(title, text string) []string {
	if goos == "darwin" {
		if _, err := lookPath("osascript"); err != nil {
			return nil
		}
		return []string{"osascript", "-e", "display notification " + strconv.Quote(text) + " with title " + strconv.Quote(title)}
	}
	if _, err := lookPath("notify-send"); err != nil {
		return nil
	}
	return []string{"notify-send", title, text}
}

// Desktop never includes model output. Activation codes are shown only through
// this explicit owner notification path, never through the operational event log.
// A missing or failing notifier never stops the bridge.
func Desktop(title, text string) {
	argv := command(title, text)
	if argv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = run(ctx, argv[0], argv[1:]...)
}
