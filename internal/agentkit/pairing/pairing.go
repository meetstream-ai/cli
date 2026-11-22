package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

func Home() string {
	if v := os.Getenv("MEETSTREAM_AGENT_KIT_HOME"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "meetstream-agent-kit")
}
func Secret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("secure random source unavailable")
	}
	return hex.EncodeToString(b)
}

type Pairing struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	CreatedAt string `json:"created_at"`
}
type entry struct {
	Label     string `json:"label"`
	Hash      string `json:"sha256"`
	CreatedAt string `json:"created_at"`
}
type Store struct {
	Dir string
	mu  sync.Mutex
}

func (s *Store) dir() string {
	if s.Dir != "" {
		return s.Dir
	}
	return Home()
}
func (s *Store) Path() string { return filepath.Join(s.dir(), "pairings.json") }
func (s *Store) read() (map[string]entry, error) {
	p := s.Path()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) && s.dir() == Home() {
		legacy := os.Getenv("MIA_BRIDGE_HOME")
		if legacy == "" {
			h, _ := os.UserHomeDir()
			legacy = filepath.Join(h, ".config", "meetstream-agent-bridge")
		}
		b, err = os.ReadFile(filepath.Join(legacy, "pairings.json"))
	}
	if os.IsNotExist(err) {
		return map[string]entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read pairings file")
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil || raw == nil {
		return nil, fmt.Errorf("pairings file is not a JSON object")
	}
	out := map[string]entry{}
	for id, v := range raw {
		var e entry
		if len(v) > 0 && v[0] == '{' && json.Unmarshal(v, &e) == nil {
			out[id] = e
		}
	}
	return out, nil
}

// WritePrivate atomically replaces a kit state file, keeping the Python permissions.
func WritePrivate(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func hash(token string) string { v := sha256.Sum256([]byte(token)); return hex.EncodeToString(v[:]) }
func (s *Store) Create(label string) (Pairing, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read()
	if err != nil {
		return Pairing{}, "", err
	}
	id := Secret(4)
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return Pairing{}, "", err
	}
	token := "msak_" + id + "_" + base64.RawURLEncoding.EncodeToString(b)
	r := []rune(label)
	if len(r) > 80 {
		label = string(r[:80])
	}
	p := Pairing{id, label, time.Now().UTC().Format("2006-01-02T15:04:05Z")}
	entries[id] = entry{label, hash(token), p.CreatedAt}
	err = WritePrivate(s.Path(), entries)
	return p, token, err
}
func (s *Store) Verify(auth string) bool {
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read()
	if err != nil {
		return false
	}
	digest := hash(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")))
	matched := 0
	for _, e := range entries {
		matched |= subtle.ConstantTimeCompare([]byte(digest), []byte(e.Hash))
	}
	return matched == 1
}
func (s *Store) Revoke(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read()
	if err != nil {
		return false, err
	}
	if _, ok := entries[id]; !ok {
		return false, nil
	}
	delete(entries, id)
	return true, WritePrivate(s.Path(), entries)
}
func (s *Store) List() ([]Pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read()
	if err != nil {
		return nil, err
	}
	out := []Pairing{}
	for id, e := range entries {
		out = append(out, Pairing{id, e.Label, e.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
