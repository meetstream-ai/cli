package login

import (
	"context"
	"errors"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/parsers"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Status struct{ State, Detail string }

func (s Status) Usable() bool { return s.State == "ok" || s.State == "unknown" }

type Checker struct {
	Agent    agents.Spec
	Binary   string
	Notify   func(string, string)
	mu       sync.Mutex
	cached   Status
	at       time.Time
	notified bool
}

func (c *Checker) Check(force bool) Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && !c.at.IsZero() && time.Since(c.at) < 30*time.Second {
		return c.cached
	}
	s := c.run()
	c.cached = s
	c.at = time.Now()
	c.tell(s)
	return s
}
func (c *Checker) Expired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cached = Status{State: "expired"}
	c.at = time.Now()
	c.tell(c.cached)
}
func (c *Checker) tell(s Status) {
	if s.State == "ok" {
		c.notified = false
	}
	if s.Usable() || c.notified {
		return
	}
	c.notified = true
	if c.Notify == nil {
		return
	}
	if s.State == "not_installed" {
		c.Notify(c.Agent.Label+" is not installed", c.Agent.InstallHint)
	} else {
		c.Notify(c.Agent.Label+" needs sign-in", "MIA cannot use your agent. Run: "+c.Agent.Relogin)
	}
}
func (c *Checker) run() Status {
	if len(c.Agent.LoginCheck) == 0 {
		return Status{"unknown", "this CLI has no login check; the first turn will tell"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Binary, c.Agent.LoginCheck...)
	b, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return Status{"not_installed", "agent CLI not found or not executable"}
	}
	if ctx.Err() != nil {
		return Status{"unknown", "login check timed out"}
	}
	code := 0
	if err != nil {
		var e *exec.ExitError
		if !errors.As(err, &e) {
			return Status{"not_installed", "agent CLI could not be started"}
		}
		code = e.ExitCode()
	}
	ok := false
	for _, v := range c.Agent.LoginOKCodes {
		if code == v {
			ok = true
		}
	}
	text := string(b)
	auth := parsers.AuthError(text)
	if !ok || (code != 0 && auth) || (code == 0 && auth && !strings.Contains(text, "loggedIn")) {
		return Status{"expired", "agent login check failed"}
	}
	return Status{State: "ok"}
}
