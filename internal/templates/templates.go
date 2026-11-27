// Package templates holds the starter apps `meetstream init` writes. They ship
// inside the binary (no download, works offline) and are pinned to the CLI
// release. Files are text/template with [[ ]] delimiters.
package templates

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

//go:embed all:files
var files embed.FS

// UseCase is one entry of the catalog.
type UseCase struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Langs     []string `json:"langs"`
	Options   []string `json:"options"`
	Available bool     `json:"available"`
	Entry     string   `json:"-"` // what to run, per language
}

// Catalog is the use cases in menu order. Unavailable ones show in the menu as
// "coming soon" so the full shape is visible.
var Catalog = []UseCase{
	{ID: "notetaker", Title: "Notetaker", Summary: "bot joins, transcript after the call, AI notes by email", Langs: []string{"python"}, Options: []string{"transcription", "llm", "email"}, Available: true},
	{ID: "send-a-bot", Title: "Send a bot", Summary: "smallest script: join, follow status, print transcript", Langs: []string{"python", "node"}, Options: []string{"transcription"}, Available: true},
	{ID: "live-captions", Title: "Live captions", Summary: "real-time transcript while the meeting runs", Langs: []string{"python"}, Options: []string{"live-transcription"}, Available: true},
	{ID: "webhook-receiver", Title: "Webhook receiver", Summary: "a handler for every bot event", Langs: []string{"python", "node"}, Options: []string{}, Available: true},
	{ID: "voice-bridge", Title: "Voice agent bridge", Summary: "your own STT, LLM and TTS over websockets (coming soon)", Langs: []string{"python"}, Options: []string{"stt", "llm", "tts"}},
	{ID: "mia-hosted", Title: "Hosted voice agent", Summary: "MIA: MeetStream runs the voice pipeline (use: meetstream setup --mode hosted)", Langs: []string{"none"}},
	{ID: "calendar", Title: "Calendar auto-join", Summary: "a bot in every meeting on a calendar (use: meetstream calendar connect)", Langs: []string{"python"}, Options: []string{"transcription"}},
}

// Find returns the use case by id.
func Find(id string) (UseCase, bool) {
	for _, u := range Catalog {
		if u.ID == id {
			return u, true
		}
	}
	return UseCase{}, false
}

// Data fills the templates.
type Data struct {
	UseCase, Lang, Title, BotName   string
	Transcription, Live, LLM, Email string
}

// Plan is the files a render would write, by relative path.
type Plan struct {
	Files map[string][]byte
}

// Paths is the sorted file list.
func (p Plan) Paths() []string {
	var out []string
	for k := range p.Files {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func render(name string, raw []byte, d Data) ([]byte, error) {
	t, err := template.New(name).Delims("[[", "]]").Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Build renders one template set in memory.
func Build(d Data) (Plan, error) {
	dir := path.Join("files", d.UseCase, d.Lang)
	if _, err := fs.Stat(files, dir); err != nil {
		return Plan{}, fmt.Errorf("no %s template for %s", d.Lang, d.UseCase)
	}
	p := Plan{Files: map[string][]byte{}}
	add := func(root string, rename map[string]string) error {
		return fs.WalkDir(files, root, func(f string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			raw, err := files.ReadFile(f)
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(f, root+"/")
			if r, ok := rename[rel]; ok {
				rel = r
			}
			out, err := render(rel, raw, d)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			p.Files[rel] = out
			return nil
		})
	}
	if err := add(dir, nil); err != nil {
		return Plan{}, err
	}
	if err := add("files/_common", map[string]string{"gitignore": ".gitignore"}); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// Write puts the plan in dir. It refuses a non-empty dir unless force is set;
// with force it overwrites only the template's own files. .env is never in a plan.
func Write(dir string, p Plan, force bool) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 && !force {
		return &NotEmptyError{Dir: dir}
	}
	for rel, b := range p.Files {
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, b, 0644); err != nil {
			return err
		}
	}
	return nil
}

// NotEmptyError means the target folder has files and --force was not given.
type NotEmptyError struct{ Dir string }

func (e *NotEmptyError) Error() string { return e.Dir + " is not empty" }
