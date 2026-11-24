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
	Path    string   `json:"path"`
	Set     []string `json:"set"`
	Filled  []string `json:"filled"`
	Missing []string `json:"missing"`
}

// syncEnv merges .env.example into .env: existing .env values stay, MeetStream
// values are filled from the profile (replaced only with overwrite), example
// defaults are copied, and empty keys are reported. It never prints a value.
func syncEnv(dir string, write, overwrite bool) (envReport, error) {
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
	fill := meetstreamValues()
	// The key and the URL it is sent to travel together: when the CLI writes the
	// key, it also writes the profile's API URL, so a project file cannot point
	// the key at another host.
	keyWritten := fill["MEETSTREAM_API_KEY"] != "" && (current["MEETSTREAM_API_KEY"] == "" || overwrite)
	if keyWritten && current["MEETSTREAM_API_URL"] != "" && current["MEETSTREAM_API_URL"] != fill["MEETSTREAM_API_URL"] {
		fmt.Fprintln(os.Stderr, "⚠ .env had a different MEETSTREAM_API_URL; replaced with your profile's URL because the key is written with it.")
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
	urlListed := false
	for _, l := range out {
		urlListed = urlListed || strings.EqualFold(l.Key, "MEETSTREAM_API_URL")
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
