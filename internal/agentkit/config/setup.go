package config

import (
	"encoding/json"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
	"os"
	"path/filepath"
)

// Setup preserves the Python wizard's schema; run 3 can use the same load/save seam.
type Setup struct {
	Agent       string `json:"agent"`
	Command     string `json:"command"`
	Workdir     string `json:"workdir"`
	MaxAccess   string `json:"max_access"`
	Tunnel      string `json:"tunnel"`
	TunnelURL   string `json:"tunnel_url"`
	Owner       string `json:"owner"`
	Model       string `json:"model"`
	MeetingMD   string `json:"meeting_md"`
	IgnoreRules string `json:"ignore_rules"`
	Effort      string `json:"effort"`
	// AutoStart "yes" starts your agent for meetings when the home app opens (default off).
	AutoStart string `json:"auto_start,omitempty"`
	// MIAAgentID is the MIA agent setup created for this computer; running setup
	// again updates it instead of creating another.
	MIAAgentID string `json:"mia_agent_id,omitempty"`
}

func LoadSetup(dir string) (*Setup, error) {
	if dir == "" {
		dir = pairing.Home()
	}
	b, err := os.ReadFile(filepath.Join(dir, "setup.json"))
	if err != nil {
		return nil, err
	}
	var s Setup
	if err = json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.Command == "" {
		if _, ok := agents.All[s.Agent]; !ok {
			return nil, os.ErrInvalid
		}
	}
	return &s, nil
}
func (s Setup) Save(dir string) error {
	if dir == "" {
		dir = pairing.Home()
	}
	return pairing.WritePrivate(filepath.Join(dir, "setup.json"), s)
}
