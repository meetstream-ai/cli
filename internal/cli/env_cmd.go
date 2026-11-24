package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/config"
	"github.com/meetstream-ai/cli/internal/ui"
)

// envLine is one KEY=value line of a .env file, in file order.
type envLine struct{ Key, Value string }

var (
	envExport = regexp.MustCompile(`^\s*export\s+`)
	envKeyOK  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// readEnvFile reads KEY=value lines the way common dotenv loaders do:
// "export KEY=..." and "KEY: value" name KEY. Lines whose key is not a plain
// identifier are dropped (bad counts them), so no loader can read a key the
// CLI did not see.
func readEnvFile(path string) (lines []envLine, bad int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = envExport.ReplaceAllString(line, "")
		sep := strings.IndexAny(line, "=:")
		if sep <= 0 {
			bad++
			continue
		}
		key, val := strings.TrimSpace(line[:sep]), strings.TrimSpace(line[sep+1:])
		if !envKeyOK.MatchString(key) {
			bad++
			continue
		}
		lines = append(lines, envLine{key, val})
	}
	return lines, bad, sc.Err()
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
	Path string `json:"path"`
	// Mode is "cli-sign-in" (the app calls MeetStream through the CLI, no key in
	// the file) or "key" (the key and API URL are in .env, for servers).
	Mode    string   `json:"meetstream_auth"`
	Set     []string `json:"set"`
	Filled  []string `json:"filled"`
	Missing []string `json:"missing"`
}

// syncEnv merges .env.example into .env: existing .env values stay, MeetStream
// values are filled from the profile (replaced only with overwrite), example
// defaults are copied, and empty keys are reported. It never prints a value.
func syncEnv(dir string, write, overwrite, withKey bool) (envReport, error) {
	example, _, err := readEnvFile(filepath.Join(dir, ".env.example"))
	if err != nil {
		return envReport{}, usageErr("no .env.example in "+dir, "run it in a project made by meetstream init, or pass --dir")
	}
	envPath := filepath.Join(dir, ".env")
	if fi, err := os.Lstat(envPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return envReport{}, usageErr(envPath+" is a symbolic link", "replace it with a plain file; the CLI does not write a key through a link")
	}
	current := map[string]string{}
	existing, bad, _ := readEnvFile(envPath)
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "⚠ dropped %d line(s) from .env that are not KEY=value with a plain key.\n", bad)
	}
	for _, l := range existing {
		current[l.Key] = l.Value
	}
	fill := map[string]string{}
	if withKey {
		fill = meetstreamValues()
		if fill["MEETSTREAM_API_KEY"] == "" {
			return envReport{}, usageErr("not signed in, so there is no key to write", "run: meetstream login")
		}
	}
	// The key and the URL it is sent to travel together: when the CLI writes the
	// key, it also writes the profile's API URL, so a project file cannot point
	// the key at another host.
	keyWritten := fill["MEETSTREAM_API_KEY"] != "" && (current["MEETSTREAM_API_KEY"] == "" || overwrite)
	if keyWritten && current["MEETSTREAM_API_URL"] != "" && current["MEETSTREAM_API_URL"] != fill["MEETSTREAM_API_URL"] {
		fmt.Fprintln(os.Stderr, "⚠ .env had a different MEETSTREAM_API_URL; replaced with your profile's URL because the key is written with it.")
		current["MEETSTREAM_API_URL"] = ""
	}
	rep := envReport{Path: envPath, Mode: "cli-sign-in", Set: []string{}, Filled: []string{}, Missing: []string{}}
	if withKey || current["MEETSTREAM_API_KEY"] != "" {
		rep.Mode = "key"
	}
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
		if val == "" && !withKey && (l.Key == "MEETSTREAM_API_KEY" || l.Key == "MEETSTREAM_API_URL") {
			continue // the app uses your CLI sign-in; nothing to fill
		}
		if val == "" {
			rep.Missing = append(rep.Missing, l.Key)
		} else {
			rep.Set = append(rep.Set, l.Key)
		}
		out = append(out, envLine{l.Key, val})
	}
	urlListed := false
	for _, l := range out {
		urlListed = urlListed || strings.EqualFold(l.Key, "MEETSTREAM_API_URL")
	}
	keyListed := false
	for _, l := range out {
		keyListed = keyListed || l.Key == "MEETSTREAM_API_KEY"
	}
	if keyWritten && !keyListed {
		out = append(out, envLine{"MEETSTREAM_API_KEY", fill["MEETSTREAM_API_KEY"]})
		rep.Filled = append(rep.Filled, "MEETSTREAM_API_KEY")
		rep.Set = append(rep.Set, "MEETSTREAM_API_KEY")
	}
	if keyWritten && !urlListed {
		rep.Filled = append(rep.Filled, "MEETSTREAM_API_URL")
		rep.Set = append(rep.Set, "MEETSTREAM_API_URL")
	}
	for _, l := range existing { // keep keys the user added
		if keyWritten && strings.EqualFold(l.Key, "MEETSTREAM_API_URL") {
			continue
		}
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
	if keyWritten { // the key never goes into .env without its URL, and the URL line is last (last-wins loaders)
		kept := out[:0]
		for _, l := range out {
			if !strings.EqualFold(l.Key, "MEETSTREAM_API_URL") {
				kept = append(kept, l)
			}
		}
		out = append(kept, envLine{"MEETSTREAM_API_URL", fill["MEETSTREAM_API_URL"]})
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
	var check, overwrite, withKey bool
	cmd := &cobra.Command{
		Use: "env", Short: "Fill .env from .env.example and list the keys still empty", GroupID: "start", Args: cobra.NoArgs,
		Long: `Fill .env in a project from its .env.example.

On your laptop no MeetStream key goes into the file: apps made by init call
MeetStream through the CLI ("meetstream api"), with your sign-in. For a
server, --with-key writes your key and the API URL it belongs to.

Keys for other services (model, email) are listed so you can add them. Values
are never printed. --check only reports and exits 3 when a key is empty.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := syncEnv(dir, !check, overwrite, withKey)
			if err != nil {
				return err
			}
			if err := emit(rep, func() {
				renderEnvReport(rep)
				if rep.Mode == "cli-sign-in" {
					ui.Note("MeetStream: no key in .env; the app uses your CLI sign-in. For a server: meetstream env --with-key")
				}
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
	f.BoolVar(&withKey, "with-key", false, "write your MeetStream key and API URL into .env (for servers; on a laptop the app uses your CLI sign-in)")
	return cmd
}

var errQuit = errors.New("cancelled")
