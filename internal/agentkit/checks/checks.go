// Package checks holds the agent kit's doctor and pre-flight checks as data
// (the kit's checks.py and its doctor): each has a name, a status, what was
// found and the fix to show. The environment (PATH lookup, file access,
// listening) is injected, so the checks run in tests without touching the
// computer. The caller prints them and decides the exit code.
package checks

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/meetingctx"
	"github.com/meetstream-ai/cli/internal/agentkit/muse"
	"github.com/meetstream-ai/cli/internal/agentkit/parsers"
	"github.com/meetstream-ai/cli/internal/agentkit/warmcheck"
)

// Status of a check. Warn is worth showing but is not a problem to fix.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Check is one line of doctor's report.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Passed is false only for Fail.
func (c Check) Passed() bool { return c.Status != Fail }

// Env is what the checks look at. DefaultEnv is the real computer.
type Env struct {
	LookPath    func(file string) (string, error)
	Stat        func(path string) (fs.FileInfo, error)
	Home        string   // the user's home folder, for ~
	InstallDirs []string // where installers put a CLI that may not be on PATH yet
	Readable    func(dir string) bool
	Writable    func(dir string) bool
	Listen      func(network, address string) (net.Listener, error)
	GOOS        string                  // "" is this computer's (tests set "windows")
	Getenv      func(key string) string // PATHEXT on Windows; nil is os.Getenv
}

// DefaultInstallDirs are where installers commonly put a CLI (the kit's _INSTALL_DIRS).
func DefaultInstallDirs(home string) []string {
	var dirs []string
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
}

// DefaultEnv looks at this computer.
func DefaultEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{
		LookPath:    exec.LookPath,
		Stat:        os.Stat,
		Home:        home,
		InstallDirs: DefaultInstallDirs(home),
		Readable: func(dir string) bool {
			f, err := os.Open(dir)
			if err != nil {
				return false
			}
			defer f.Close()
			_, err = f.Readdirnames(1)
			return err == nil || errors.Is(err, io.EOF)
		},
		Writable: func(dir string) bool {
			f, err := os.CreateTemp(dir, ".doctor-*")
			if err != nil {
				return false
			}
			name := f.Name()
			_ = f.Close()
			_ = os.Remove(name)
			return true
		},
		Listen: net.Listen,
	}
}

func (e Env) expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(e.Home, p[1:])
	}
	return p
}

// executable: a regular file with an execute bit, or on Windows (no mode
// bits) one whose extension is in PATHEXT.
func (e Env) executable(p string) bool {
	fi, err := e.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	if e.windows() {
		return hasPathExt(p, e.pathExt())
	}
	return fi.Mode().Perm()&0o111 != 0
}

func (e Env) windows() bool {
	if e.GOOS != "" {
		return e.GOOS == "windows"
	}
	return runtime.GOOS == "windows"
}

// pathExt is PATHEXT, else Windows' default list.
func (e Env) pathExt() string {
	get := e.Getenv
	if get == nil {
		get = os.Getenv
	}
	if v := get("PATHEXT"); v != "" {
		return v
	}
	return ".COM;.EXE;.BAT;.CMD"
}

func hasPathExt(p, pathExt string) bool {
	ext := filepath.Ext(p)
	if ext == "" {
		return false
	}
	for _, x := range strings.Split(pathExt, ";") {
		if x = strings.TrimSpace(x); x != "" && strings.EqualFold(x, ext) {
			return true
		}
	}
	return false
}

// ResolveBinary finds the agent CLI: an override path as given, else PATH,
// else the folders installers use when PATH does not have them yet (offPath).
// "" when it is not installed. Every later check (sign-in, warm, Muse app,
// model list, test turn) must use this path, never spec.Binary again.
func ResolveBinary(e Env, spec agents.Spec, override string) (path string, offPath bool) {
	candidate := override
	if candidate == "" {
		candidate = spec.Binary
	}
	if strings.ContainsRune(candidate, '/') || strings.ContainsRune(candidate, os.PathSeparator) {
		p := e.expand(candidate)
		if e.executable(p) {
			return p, false
		}
		return "", false
	}
	if p, err := e.LookPath(candidate); err == nil {
		return p, false
	}
	for _, dir := range e.InstallDirs {
		if p := filepath.Join(dir, candidate); e.executable(p) {
			return p, true
		}
		if e.windows() && filepath.Ext(candidate) == "" { // claude → claude.exe, claude.cmd …
			for _, x := range strings.Split(e.pathExt(), ";") {
				if x = strings.TrimSpace(x); x != "" {
					if p := filepath.Join(dir, candidate+strings.ToLower(x)); e.executable(p) {
						return p, true
					}
				}
			}
		}
	}
	return "", false
}

// Binary: is the agent CLI installed, and where.
func Binary(e Env, spec agents.Spec, override string) Check {
	path, offPath := ResolveBinary(e, spec, override)
	switch {
	case path == "":
		shown := override
		if shown == "" {
			shown = spec.Binary
		}
		return Check{"agent", Fail, fmt.Sprintf("%s not found (%s)", spec.Label, shown), spec.InstallHint}
	case offPath:
		dir := filepath.Dir(path)
		return Check{"agent", Warn, fmt.Sprintf("%s: %s (its folder is not on your PATH)", spec.Label, path),
			fmt.Sprintf(`Add it to your shell profile: export PATH="%s:$PATH"`, dir)}
	}
	return Check{"agent", OK, spec.Label + ": " + path, ""}
}

// MuseApp: Muse's launcher without the app it downloads. ok is false for every
// other agent (no check to show).
func MuseApp(spec agents.Spec, binary string) (Check, bool) {
	if spec.Name != muse.AgentName || binary == "" {
		return Check{}, false
	}
	if muse.LauncherWithoutApp(spec.Name, binary) {
		return Check{"app", Fail, spec.Label + ": " + muse.MenuHint + " (about 456 MB)",
			"Download it with: " + muse.InstallCommand(binary).String()}, true
	}
	return Check{"app", OK, spec.Label + " app downloaded", ""}, true
}

// Workdir: the folder the agent works in exists and can be read.
func Workdir(e Env, path string) Check {
	path = e.expand(path)
	fi, err := e.Stat(path)
	switch {
	case err != nil:
		return Check{"workdir", Fail, path + " does not exist", "Pass an existing folder with --workdir."}
	case !fi.IsDir():
		return Check{"workdir", Fail, path + " is not a folder", "Pass a folder with --workdir."}
	case !e.Readable(path):
		return Check{"workdir", Fail, path + " cannot be read", "Pass a folder you can read with --workdir."}
	}
	return Check{"workdir", OK, path, ""}
}

// Home: the kit's config folder exists, or can be created, and is writable.
func Home(e Env, home string) Check {
	probe := home
	for {
		if _, err := e.Stat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	if e.Writable(probe) {
		return Check{"config", OK, home, ""}
	}
	return Check{"config", Fail, home + " cannot be written",
		"Fix the folder's permissions, or set MEETSTREAM_AGENT_KIT_HOME to a folder you own."}
}

// Port: the local bridge port is free (only for `agent serve`; the relay needs none).
func Port(e Env, port int) Check {
	ln, err := e.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return Check{"port", Fail, fmt.Sprintf("port %d is used by another program (maybe a bridge already running)", port),
				fmt.Sprintf("Start the bridge with another --port, or free port %d.", port)}
		}
		return Check{"port", Fail, fmt.Sprintf("cannot listen on 127.0.0.1:%d: %v", port, err), ""}
	}
	_ = ln.Close()
	return Check{"port", OK, fmt.Sprintf("127.0.0.1:%d is free", port), ""}
}

// Login turns the login package's status (State, Detail) into a check.
// "unknown" (a CLI with no sign-in check) is a warning: the first turn will tell.
func Login(spec agents.Spec, state, detail string) Check {
	text := spec.Label + " " + state
	if detail != "" {
		text += " (" + detail + ")"
	}
	switch state {
	case "ok":
		return Check{"login", OK, spec.Label + " signed in", ""}
	case "unknown":
		return Check{"login", Warn, text, ""}
	case "not_installed":
		return Check{"login", Fail, text, spec.InstallHint}
	}
	hint := "Sign in to the agent CLI."
	if spec.Relogin != "" {
		hint = "Run: " + spec.Relogin
	}
	return Check{"login", Fail, text, hint}
}

// Warm turns a warm check into a doctor line.
func Warm(r warmcheck.Result) Check {
	if r.OK {
		return Check{"warm", OK, r.Message, ""}
	}
	return Check{"warm", Fail, r.Message, r.Fix}
}

// Relay: this computer is connected to MeetStream (the relay replaced pairing tokens).
func Relay(connected bool, device, relayURL string) Check {
	if !connected {
		return Check{"relay", Fail, "this computer isn't connected to MeetStream", "Run: meetstream agent login"}
	}
	return Check{"relay", OK, strings.TrimSpace(device + " · " + relayURL), ""}
}

// ContextWarnTokens: more than this read on every meeting turn is a slower, costlier meeting.
const ContextWarnTokens = 4000

// Context: what the agent reads on every turn, project rules (CLAUDE.md,
// AGENTS.md …) included. mode is meetingctx.Mode; workspace is meetingctx.Workspace.
func Context(items []meetingctx.Item, mode, workspace string) Check {
	loaded := 0
	for _, i := range items {
		if i.Loaded {
			loaded++
		}
	}
	tokens := meetingctx.Tokens(items)
	detail := fmt.Sprintf("%s · ~%d tokens a turn (%d file(s))", mode, tokens, loaded)
	if tokens > ContextWarnTokens {
		return Check{"context", Warn, detail,
			fmt.Sprintf("Use %s as the folder, or --ignore-rules. See: meetstream agent context show", workspace)}
	}
	return Check{"context", OK, detail, ""}
}

// DoctorInput is what Doctor checks. A nil func skips its check.
type DoctorInput struct {
	Spec     *agents.Spec // nil: the agent could not be resolved (AgentError says why)
	AgentErr *Check       // shown instead of the agent checks when Spec is nil
	Override string       // --binary
	Workdir  string
	Home     string // the kit's config folder (pairing.Home())
	Port     int    // 0: no port check (relay mode)
	// Login runs the sign-in check for the resolved binary (login.Checker).
	Login func(binary string) (state, detail string)
	// Warm runs warmcheck.Check without a turn; nil, or a non-ACP agent, skips it.
	Warm func(binary string) warmcheck.Result
	// Relay reports the relay credentials.
	Relay func() (connected bool, device, relayURL string)
	// Context lists what the agent reads each turn.
	Context func(spec agents.Spec, workdir string) (items []meetingctx.Item, mode, workspace string)
}

// Doctor runs the checks in the kit's order: config, agent, workdir, binary,
// Muse app, sign-in, warm, relay, context, port.
func Doctor(e Env, in DoctorInput) []Check {
	out := []Check{Home(e, in.Home)}
	if in.Spec == nil && in.AgentErr != nil {
		out = append(out, *in.AgentErr)
	}
	out = append(out, Workdir(e, in.Workdir))
	if in.Spec != nil {
		spec := *in.Spec
		out = append(out, Binary(e, spec, in.Override))
		binary, _ := ResolveBinary(e, spec, in.Override)
		appMissing := false
		if c, ok := MuseApp(spec, binary); ok {
			out = append(out, c)
			appMissing = !c.Passed()
		}
		if binary != "" && !appMissing && in.Login != nil {
			state, detail := in.Login(binary)
			login := Login(spec, state, detail)
			out = append(out, login)
			if login.Passed() && in.Warm != nil && warmcheck.AppliesTo(spec) {
				out = append(out, Warm(in.Warm(binary)))
			}
		}
	}
	if in.Relay != nil {
		out = append(out, Relay(in.Relay()))
	}
	if in.Spec != nil && in.Context != nil {
		out = append(out, Context(in.Context(*in.Spec, e.expand(in.Workdir))))
	}
	if in.Port > 0 {
		out = append(out, Port(e, in.Port))
	}
	return out
}

// Verdict is doctor's summary line and the failed count.
func Verdict(cs []Check) (string, int) {
	failed := 0
	for _, c := range cs {
		if !c.Passed() {
			failed++
		}
	}
	if failed == 0 {
		return "All good.", 0
	}
	return fmt.Sprintf("%d problem(s) to fix, listed below.", failed), failed
}

// ── Why a test turn failed ─────────────────────────────────────────────────────

// TurnDiagnosis is the likely reason a test turn failed, and its fix.
type TurnDiagnosis struct {
	Kind    string // not_installed, signed_out, timeout, failed, empty
	Message string
	Fix     string
	Detail  string // what the CLI itself said (its error or last stderr lines)
}

// DiagnoseTurn returns nil when the agent answered.
func DiagnoseTurn(spec agents.Spec, answer, errText, stderrTail string, timeoutS float64) *TurnDiagnosis {
	if strings.TrimSpace(answer) != "" && errText == "" {
		return nil
	}
	said, tail := strings.TrimSpace(errText), strings.TrimSpace(stderrTail)
	var parts []string
	if said != "" {
		parts = append(parts, said)
	}
	if tail != "" && !strings.Contains(said, tail) {
		parts = append(parts, tail)
	}
	detail := strings.Join(parts, "\n")
	signIn := "Sign in to the CLI, then try again."
	if spec.Relogin != "" {
		signIn = "Sign in with: " + spec.Relogin
	}
	switch {
	case strings.HasPrefix(said, "agent CLI not found"):
		return &TurnDiagnosis{"not_installed", spec.Label + " is not installed", spec.InstallHint, ""}
	case parsers.AuthError(said + "\n" + tail):
		return &TurnDiagnosis{"signed_out", spec.Label + " is signed out", signIn, detail}
	case strings.Contains(said, "timed out"):
		limit := ""
		if timeoutS > 0 {
			limit = fmt.Sprintf(" within %d s", int(timeoutS))
		}
		return &TurnDiagnosis{"timeout", spec.Label + " did not answer" + limit,
			fmt.Sprintf("It may be waiting for a sign-in or a permission prompt. Run `%s` on its own once to see what it asks for, then try again.", spec.Binary), tail}
	case said != "":
		return &TurnDiagnosis{"failed", spec.Label + " failed", signIn, detail}
	}
	fix := "Often a sign-in it is waiting for. " + signIn
	if tail == "" {
		fix += fmt.Sprintf(" If it still says nothing, run `%s` on its own once.", spec.Binary)
	}
	return &TurnDiagnosis{"empty", spec.Label + " ran but sent back no answer", fix, tail}
}
