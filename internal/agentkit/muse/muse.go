// Package muse handles Muse Code's launcher: ~/.local/bin/muse is a small
// script, and the app it downloads (about 456 MB) lives beside it as
// muse-bin-<contents of .muse-version>. A download that died leaves the
// launcher alone, which is not an installed agent (the kit's setup.py
// launcher_without_app and _download_app).
package muse

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// AgentName is the agents.All key this package is about.
const AgentName = "muse"

// InstallEnv makes the launcher run its own install mode (it also clears stale locks).
const InstallEnv = "MUSE_LAUNCHER_INSTALL=1"

// MenuHint is the agent menu's hint for a launcher whose app is missing.
const MenuHint = "launcher only; app not downloaded yet"

// versionPattern is the launcher's own version_pattern for .muse-version.
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+-R[0-9]+(\.[0-9]+)?$`)

// LauncherWithoutApp reports whether binary is Muse's launcher with no app beside
// it. It is false for every other agent. A symlinked launcher is followed.
func LauncherWithoutApp(agentName, binary string) bool {
	if agentName != AgentName {
		return false
	}
	path := binary
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		path = resolved
	} else if abs, err := filepath.Abs(binary); err == nil {
		path = abs
	}
	folder := filepath.Dir(path)
	raw, err := os.ReadFile(filepath.Join(folder, ".muse-version"))
	if err != nil {
		return true
	}
	version := strings.TrimSpace(string(raw))
	if !versionPattern.MatchString(version) {
		return true
	}
	return !appPresent(filepath.Join(folder, "muse-bin-"+version))
}

// appPresent: the downloaded app is a regular file with an execute bit. Windows
// has no execute bits (Go reports none), so there the file itself, with or
// without .exe, is the app.
func appPresent(app string) bool {
	if runtime.GOOS == "windows" {
		for _, p := range []string{app, app + ".exe"} {
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				return true
			}
		}
		return false
	}
	fi, err := os.Stat(app)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// Command is a program to run without a shell: Argv plus extra environment
// entries (KEY=value) added to the caller's own environment.
type Command struct {
	Argv []string
	Env  []string
}

// String is the command as a person would type it.
func (c Command) String() string {
	return strings.TrimSpace(strings.Join(c.Env, " ") + " " + strings.Join(c.Argv, " "))
}

// Cmd builds the process: no shell, the caller's environment plus Env.
func (c Command) Cmd() *exec.Cmd {
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Env = append(os.Environ(), c.Env...)
	return cmd
}

// InstallCommand is the launcher's own download: MUSE_LAUNCHER_INSTALL=1 <launcher>.
func InstallCommand(binary string) Command {
	return Command{Argv: []string{binary}, Env: []string{InstallEnv}}
}

// AbortMessage is setup's error when it cannot go on without the app.
func AbortMessage(label string) string { return label + " has not finished downloading" }

// Downloader offers the launcher's own download and one retry at a time, as
// setup does. Every field but Binary and Label is injected so tests run nothing.
type Downloader struct {
	Label  string // "Muse Code"
	Binary string // the launcher's path
	Offer  bool   // at a terminal: offer to run it; otherwise only show the command
	// Confirm asks a yes/no question. inputEnded is true when stdin has closed:
	// the answer is then a default and is never acted on, so it cannot loop.
	Confirm func(question string) (yes, inputEnded bool)
	// Run runs the command (its output goes to the person) and returns its exit code.
	Run func(Command) int
	// Say shows one line: kind is "ok", "fail", "note" or "command".
	Say func(kind, text string)
}

// Download returns true once the app is beside the launcher.
func (d Downloader) Download() bool {
	say := d.Say
	if say == nil {
		say = func(string, string) {}
	}
	say("fail", d.Label+": only the launcher is installed; the app (about 456 MB) has not downloaded yet.")
	command := InstallCommand(d.Binary)
	if !d.Offer || d.Confirm == nil || d.Run == nil {
		say("note", "Download it, then run setup again:")
		say("command", command.String())
		return false
	}
	question := fmt.Sprintf("Download the %s app now?", d.Label)
	for {
		yes, ended := d.Confirm(question)
		if !yes || ended { // input ended: never retry on a default answer
			break
		}
		say("note", "Downloading with Muse's own launcher; its output follows.")
		code := d.Run(command)
		if !LauncherWithoutApp(AgentName, d.Binary) {
			say("ok", d.Label+" app downloaded")
			return true
		}
		text := fmt.Sprintf("The download did not finish (exit code %d).", code)
		if code != 0 {
			text += " The connection may have dropped."
		}
		say("fail", text)
		question = "Try the download again?"
	}
	say("note", "Pick another agent, or run setup again later.")
	return false
}
