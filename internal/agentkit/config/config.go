// Package config holds bridge settings, independent of HTTP and sign-in transports.
package config

import (
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"time"
)

const (
	DefaultPort    = 8787
	MaxBodyBytes   = 4 * 1024 * 1024
	MaxAnswerChars = 20000
	Version        = "0.3.0"
	MCPSecretEnv   = "MEETSTREAM_AGENT_KIT_MCP_SECRET"
	TimeoutReply   = "That took too long, so I stopped it."
)

type Config struct {
	Agent                                                                      agents.Spec
	Binary, Workdir, MaxAccess, Owner, Model, Effort, MeetingNotes, RuntimeDir string
	Timeout                                                                    time.Duration
	Prime, Quiet, Debug, IgnoreRules                                           bool
	Notify                                                                     func(title, text string)
	Confirm                                                                    func(description string) bool
	// Event carries operational metadata only, never turn text, secrets, or participant identity.
	Event func(kind string, fields map[string]any)
}

func (c Config) Defaults() Config {
	if c.Binary == "" {
		c.Binary = c.Agent.Binary
	}
	if c.MaxAccess == "" {
		c.MaxAccess = "readonly"
	}
	if c.Owner == "" {
		c.Owner = "the user"
	}
	if c.Timeout <= 0 {
		c.Timeout = 60 * time.Second
	}
	if c.Notify == nil {
		c.Notify = func(string, string) {}
	}
	if c.Confirm == nil {
		c.Confirm = func(string) bool { return false }
	}
	if c.Event == nil {
		c.Event = func(string, map[string]any) {}
	}
	return c
}
