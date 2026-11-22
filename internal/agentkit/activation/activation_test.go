package activation

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

type shown struct{ title, text string }

func makeGate() (*Gate, *[]shown) {
	var got []shown
	g := New("Claude Code", func(title, text string) { got = append(got, shown{title, text}) })
	return g, &got
}

func wrongCode(code string) string {
	n, _ := strconv.Atoi(code)
	return fmt.Sprintf("%06d", (n+1)%1000000)
}

// Ported from tests/test_bridge.py::test_activation_binds_one_participant.
func TestActivationBindsOneParticipant(t *testing.T) {
	g, got := makeGate()
	code := g.code
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		t.Fatalf("code %q is not 6 digits", code)
	}
	if len(*got) != 1 || !strings.Contains((*got)[0].text, code) {
		t.Fatalf("the code was not shown to the owner: %v", *got)
	}
	if g.State() != "pending" {
		t.Fatalf("state %q", g.State())
	}
	if ok, r := g.Claim(code, "", "Nav"); ok || r != "no_participant_id" {
		t.Fatalf("no id: %v %q", ok, r)
	}
	if ok, r := g.Claim(code, "dev-1", "Nav"); !ok || r != "ok" {
		t.Fatalf("claim: %v %q", ok, r)
	}
	if g.State() != "claimed" || g.owner != "dev-1" {
		t.Fatalf("state %q owner %q", g.State(), g.owner)
	}
	if ok, r := g.Claim(code, "dev-2", "Guest"); ok || r != "already_claimed" { // single use
		t.Fatalf("second claim: %v %q", ok, r)
	}
}

// Ported from tests/test_bridge.py::test_wrong_codes_rotate_then_lock.
func TestWrongCodesRotateThenLock(t *testing.T) {
	g, got := makeGate()
	first := g.code
	wrong := wrongCode(first)
	for i := 0; i < 3; i++ {
		if _, r := g.Claim(wrong, "dev-9", "Guest"); r != "wrong_code" {
			t.Fatalf("try %d: %q", i, r)
		}
	}
	if g.code == first {
		t.Fatal("the code did not rotate after 3 wrong tries")
	}
	if len(*got) != 2 { // a new code after 3 wrong tries
		t.Fatalf("shown %d codes, want 2", len(*got))
	}
	for g.State() == "pending" {
		g.Claim("x", "dev-9", "Guest")
	}
	if g.State() != "locked" || g.fails != 12 {
		t.Fatalf("state %q fails %d", g.State(), g.fails)
	}
	if ok, r := g.Claim(g.code, "dev-1", "Nav"); ok || r != "locked" {
		t.Fatalf("after lock: %v %q", ok, r)
	}
}

// Ported from tests/test_bridge.py::test_codes_expire_and_lock_again_when_the_owner_leaves.
func TestCodesExpireAndLockAgainWhenTheOwnerLeaves(t *testing.T) {
	base := time.Now()
	g, got := makeGate()
	g.now = func() time.Time { return base.Add(301 * time.Second) }
	if ok, r := g.Claim(g.code, "dev-1", "Nav"); ok || r != "expired" {
		t.Fatalf("expired code: %v %q", ok, r)
	}
	if len(*got) != 2 {
		t.Fatalf("shown %d codes, want 2", len(*got))
	}
	g.now = func() time.Time { return base.Add(302 * time.Second) }
	if ok, r := g.Claim(g.code, "dev-1", "Nav"); !ok || r != "ok" {
		t.Fatalf("new code: %v %q", ok, r)
	}
	if g.Release([]string{"dev-7"}) {
		t.Fatal("someone else leaving released the owner")
	}
	if !g.Release([]string{"dev-1"}) {
		t.Fatal("the owner leaving did not release")
	}
	if g.State() != "pending" || len(*got) != 3 {
		t.Fatalf("state %q shown %d", g.State(), len(*got))
	}
}

// The code is valid until exactly 5 minutes (the kit compares now > expires).
func TestCodeValidUntilFiveMinutes(t *testing.T) {
	base := time.Now()
	g, _ := makeGate()
	g.now = func() time.Time { return g.expires }
	if ok, r := g.Claim(g.code, "dev-1", "Nav"); !ok {
		t.Fatalf("at the deadline: %q", r)
	}
	if d := g.expires.Sub(base); d < 5*time.Minute || d > 5*time.Minute+time.Second {
		t.Fatalf("ttl %v", d)
	}
}

// The terminal + desktop hook (console.notify in the kit) gets the kit's title
// and text; a nil hook is allowed.
func TestShowHook(t *testing.T) {
	g, got := makeGate()
	want := shown{"MIA activation code", g.code + ": paste it in the meeting chat to let Claude Code answer you (valid 5 min)"}
	if (*got)[0] != want {
		t.Fatalf("shown %+v, want %+v", (*got)[0], want)
	}
	quiet := New("Claude Code", nil)
	if quiet.State() != "pending" {
		t.Fatal("a gate without a hook should still work")
	}
}

// Whitespace around a pasted code is ignored (the kit strips it).
func TestPastedCodeIsTrimmed(t *testing.T) {
	g, _ := makeGate()
	if ok, r := g.Claim("  "+g.code+"\n", "dev-1", "Nav"); !ok {
		t.Fatalf("trimmed code: %q", r)
	}
}
