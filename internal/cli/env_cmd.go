package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/ui"
)

// envLine is one KEY=value line of a .env file, in file order.
type envLine struct{ Key, Value string }

func readEnvFile(path string) ([]envLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []envLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out = append(out, envLine{strings.TrimSpace(k), strings.TrimSpace(v)})
	}
	return out, sc.Err()
}

// meetstreamValues are the values the CLI can fill from the signed-in profile.
func meetstreamValues() map[string]string {
	v := map[string]string{"MEETSTREAM_API_URL": config.APIURL()}
	if key, _ := config.ResolveKey(); key != "" {
		v["MEETSTREAM_API_KEY"] = key
	}
	return v
}

type envReport struct {
	Path    string   `json:"path"`
	Set     []string `json:"set"`
	Filled  []string `json:"filled"`
	Missing []string `json:"missing"`
}

// syncEnv merges .env.example into .env: existing .env values stay, MeetStream
// values are filled from the profile (replaced only with overwrite), example
// defaults are copied, and empty keys are reported. It never prints a value.
func syncEnv(dir string, write, overwrite bool) (envReport, error) {
	example, err := readEnvFile(filepath.Join(dir, ".env.example"))
	if err != nil {
		return envReport{}, usageErr("no .env.example in "+dir, "run it in a project made by meetstream init, or pass --dir")
	}
	envPath := filepath.Join(dir, ".env")
	if fi, err := os.Lstat(envPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return envReport{}, usageErr(envPath+" is a symbolic link", "replace it with a plain file; the CLI does not write a key through a link")
	}
	current := map[string]string{}
	existing, _ := readEnvFile(envPath)
	for _, l := range existing {
		current[l.Key] = l.Value
	}
	fill := meetstreamValues()
	// The key and the URL it is sent to travel together: when the CLI writes the
	// key, it also writes the profile's API URL, so a project file cannot point
	// the key at another host.
	keyWritten := fill["MEETSTREAM_API_KEY"] != "" && (current["MEETSTREAM_API_KEY"] == "" || overwrite)
	if keyWritten && current["MEETSTREAM_API_URL"] != "" && current["MEETSTREAM_API_URL"] != fill["MEETSTREAM_API_URL"] {
		fmt.Fprintln(os.Stderr, "⚠ .env had MEETSTREAM_API_URL="+current["MEETSTREAM_API_URL"]+"; replaced with your profile's URL because the key is written with it.")
		current["MEETSTREAM_API_URL"] = ""
	}
	rep := envReport{Path: envPath, Set: []string{}, Filled: []string{}, Missing: []string{}}
	var out []envLine
	for _, l := range example {
		val, had := current[l.Key]
		switch {
		case fill[l.Key] != "" && (val == "" || overwrite):
			if val != fill[l.Key] {
				rep.Filled = append(rep.Filled, l.Key)
			}
			val = fill[l.Key]
		case !had || val == "":
			val = l.Value
		}
		if val == "" {
			rep.Missing = append(rep.Missing, l.Key)
		} else {
			rep.Set = append(rep.Set, l.Key)
		}
		out = append(out, envLine{l.Key, val})
	}
	for _, l := range existing { // keep keys the user added
		found := false
		for _, e := range example {
			if e.Key == l.Key {
				found = true
			}
		}
		if !found {
			out = append(out, l)
		}
	}
	if write {
		var b strings.Builder
		for _, l := range out {
			b.WriteString(l.Key + "=" + l.Value + "\n")
		}
		if err := os.WriteFile(envPath, []byte(b.String()), 0600); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func renderEnvReport(rep envReport) {
	for _, k := range rep.Set {
		note := ""
		for _, f := range rep.Filled {
			if f == k {
				note = ui.DimStyle.Render("  filled from your MeetStream profile")
			}
		}
		fmt.Fprintln(ui.Out, ui.OKLine(fmt.Sprintf("%-26s set", k))+note)
	}
	for _, k := range rep.Missing {
		fmt.Fprintln(ui.Out, ui.WarnLine(fmt.Sprintf("%-26s empty", k))+ui.DimStyle.Render("  add it to .env"))
	}
}

func newEnvCmd() *cobra.Command {
	var dir string
	var check, overwrite bool
	cmd := &cobra.Command{
		Use: "env", Short: "Fill .env from .env.example and list the keys still empty", GroupID: "start", Args: cobra.NoArgs,
		Long: `Fill .env in a project from its .env.example.

MeetStream values (API key, API URL) come from your signed-in profile. Keys
for other services (model, email) are listed so you can add them. Values are
never printed. --check only reports and exits 3 when a key is empty.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := syncEnv(dir, !check, overwrite)
			if err != nil {
				return err
			}
			if err := emit(rep, func() {
				renderEnvReport(rep)
				if !check {
					ui.Note("Wrote " + rep.Path + " (mode 0600; .gitignore keeps it out of git).")
				}
			}); err != nil {
				return err
			}
			if check && len(rep.Missing) > 0 {
				return &ExitError{Code: 3, Message: fmt.Sprintf("%d key(s) empty", len(rep.Missing)), Hint: "add them to .env", Silent: g.JSON}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dir, "dir", ".", "project folder")
	f.BoolVar(&check, "check", false, "only report; exit 3 if a key is empty")
	f.BoolVar(&overwrite, "overwrite", false, "replace MeetStream values that are already set")
	return cmd
}

var errQuit = errors.New("cancelled")
