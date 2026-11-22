package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/pairing"
)

// The same store as the Python kit, so an existing kit sign-in keeps working:
// macOS Keychain (service ai.meetstream.agent-kit, account default) through
// `security -i` with the commands on stdin; Linux Secret Service through
// secret-tool with the secret on stdin; else credentials.json (0600 in 0700).
const (
	service = "ai.meetstream.agent-kit"
	account = "default"
)

// Credentials is what stays on disk. The access token never does.
type Credentials struct {
	DeviceID     string `json:"device_id"`
	RefreshToken string `json:"refresh_token"`
	AuthURL      string `json:"auth_url"`
	RelayURL     string `json:"relay_url"`
	SignedInAt   string `json:"signed_in_at,omitempty"`
}

// Store picks a backend once. MEETSTREAM_AGENT_KIT_CREDENTIALS=file|keychain|secret-tool forces one.
type Store struct {
	Home    string // directory for the file backend
	Backend string
}

// KitHome is the kit's config directory.
func KitHome() string {
	return pairing.Home()
}

func NewStore() *Store {
	s := &Store{Home: KitHome()}
	switch f := os.Getenv("MEETSTREAM_AGENT_KIT_CREDENTIALS"); f {
	case "file", "keychain", "secret-tool":
		s.Backend = f
		return s
	}
	if _, err := exec.LookPath("security"); err == nil && runtime.GOOS == "darwin" {
		s.Backend = "keychain"
	} else if _, err := exec.LookPath("secret-tool"); err == nil && runtime.GOOS == "linux" {
		s.Backend = "secret-tool"
	} else {
		s.Backend = "file"
	}
	return s
}

// Where names the store for people.
func (s *Store) Where() string {
	switch s.Backend {
	case "keychain":
		return "macOS Keychain"
	case "secret-tool":
		return "Secret Service"
	}
	return filepath.Join(s.Home, "credentials.json")
}

// Load returns nil, nil when this computer is not signed in.
func (s *Store) Load() (*Credentials, error) {
	raw, err := s.read()
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil || c.RefreshToken == "" {
		return nil, errors.New("the stored MeetStream sign-in is unreadable; run: meetstream agent login")
	}
	return &c, nil
}

func (s *Store) Save(c *Credentials) error {
	if c.SignedInAt == "" {
		c.SignedInAt = time.Now().UTC().Format(time.RFC3339)
	}
	b, _ := json.Marshal(c)
	switch s.Backend {
	case "keychain":
		return runStdin("security", []string{"-i"}, fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", service, account, quote(string(b))))
	case "secret-tool":
		return runStdin("secret-tool", []string{"store", "--label=MeetStream Agent Kit", "service", service, "account", account}, string(b))
	}
	if err := os.MkdirAll(s.Home, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(s.Home, ".credentials.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.Home, "credentials.json"))
}

func (s *Store) Delete() {
	switch s.Backend {
	case "keychain":
		_ = runStdin("security", []string{"-i"}, fmt.Sprintf("delete-generic-password -s %s -a %s\n", service, account))
	case "secret-tool":
		_ = exec.Command("secret-tool", "clear", "service", service, "account", account).Run()
	}
	_ = os.Remove(filepath.Join(s.Home, "credentials.json"))
}

func (s *Store) read() (string, error) {
	switch s.Backend {
	case "keychain":
		out, err := exec.Command("security", "find-generic-password", "-s", service, "-a", account, "-w").Output()
		if err != nil {
			return "", nil // not found
		}
		return string(out), nil
	case "secret-tool":
		out, err := exec.Command("secret-tool", "lookup", "service", service, "account", account).Output()
		if err != nil {
			return "", nil
		}
		return string(out), nil
	}
	b, err := os.ReadFile(filepath.Join(s.Home, "credentials.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// quote is for `security -i`, which splits on spaces and honours double quotes.
func quote(v string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

func runStdin(name string, args []string, stdin string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s failed: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}
