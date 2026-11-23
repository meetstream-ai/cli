package activation

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"
)

type Gate struct {
	mu           sync.Mutex
	owner, code  string
	ownerName    string
	fails, tries int
	expires      time.Time
	now          func() time.Time
	show         func(string, string)
	label        string
	// Log, when set, gets one line per change: "activated for <name>", the lock.
	Log func(string)
}

func (g *Gate) note(text string) {
	if g.Log != nil {
		g.Log(text)
	}
}

func (g *Gate) ownerLabel() string {
	if g.ownerName != "" {
		return g.ownerName
	}
	return "the owner"
}

func New(label string, show func(string, string)) *Gate {
	if show == nil {
		show = func(string, string) {}
	}
	g := &Gate{now: time.Now, show: show, label: label}
	g.rotate()
	return g
}
func (g *Gate) rotate() {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		panic("secure random source unavailable")
	}
	g.code = fmt.Sprintf("%06d", n.Int64())
	g.expires = g.now().Add(5 * time.Minute)
	g.tries = 0
	g.show("MIA activation code", g.code+": paste it in the meeting chat to let "+g.label+" answer you (valid 5 min)")
}
func (g *Gate) state() string {
	if g.owner != "" {
		return "claimed"
	}
	if g.fails >= 12 {
		return "locked"
	}
	return "pending"
}
func (g *Gate) State() string { g.mu.Lock(); defer g.mu.Unlock(); return g.state() }
func (g *Gate) Claim(code, id, name string) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.owner != "" {
		return false, "already_claimed"
	}
	if g.fails >= 12 {
		return false, "locked"
	}
	if id == "" {
		return false, "no_participant_id"
	}
	if g.now().After(g.expires) {
		g.rotate()
		return false, "expired"
	}
	if g.code != "" && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(code)), []byte(g.code)) == 1 {
		g.owner = id
		if r := []rune(strings.TrimSpace(name)); len(r) > 80 {
			g.ownerName = string(r[:80])
		} else {
			g.ownerName = string(r)
		}
		g.code = ""
		g.note("activated for " + g.ownerLabel())
		return true, "ok"
	}
	g.fails++
	g.tries++
	if g.fails >= 12 {
		g.code = ""
		g.note("too many wrong codes: locked for this meeting")
		return false, "locked"
	}
	if g.tries >= 3 {
		g.rotate()
	}
	return false, "wrong_code"
}
func (g *Gate) Release(ids []string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.owner == "" {
		return false
	}
	for _, id := range ids {
		if id == g.owner {
			g.note(g.ownerLabel() + " left: locked")
			g.owner, g.ownerName = "", ""
			if g.fails < 12 {
				g.rotate()
			}
			return true
		}
	}
	return false
}
