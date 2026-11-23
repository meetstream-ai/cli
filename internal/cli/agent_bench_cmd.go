package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/bench"
	"github.com/meetstream-ai/cli/internal/agentkit/checks"
	kitconfig "github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/drivers"
	"github.com/meetstream-ai/cli/internal/agentkit/estimates"
	"github.com/meetstream-ai/cli/internal/agentkit/parsers"
	"github.com/meetstream-ai/cli/internal/agentkit/pricing"
	"github.com/meetstream-ai/cli/internal/agentkit/proctree"
	"github.com/meetstream-ai/cli/internal/agentkit/prompts"
	"github.com/meetstream-ai/cli/internal/agentkit/usage"
	"github.com/meetstream-ai/cli/internal/ui"
)

// `meetstream agent bench`: token use per model, one short real meeting turn
// each, projected to an hour of meeting (the kit's `bench --compare`). Setup's
// "Measure live" uses the same measurer and queue.

// sampleTurn is the meeting turn every model answers ({owner} is the person).
var sampleTurn = []map[string]any{
	{"speaker": "Priya", "text": "Okay, next item is the release."},
	{"speaker": "Sam", "text": "We're mostly done, two bugs left."},
	{"speaker": "{owner}", "text": "Agent, in one sentence: are we on track to ship this week?"},
}

// benchExec runs one CLI process in dir and hands each stdout line to onLine.
// It returns the last lines of stderr. The CLI runs in its own process group:
// a timeout or Stop kills everything it started, not only the CLI itself.
// Tests swap it so no CLI is started.
var benchExec = func(ctx context.Context, argv []string, dir, input string, env []string, onLine func(string)) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, 3*time.Second
	proctree.Prepare(cmd)
	cmd.Cancel = func() error { return proctree.Kill(cmd) }
	defer proctree.Reap(cmd)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var tail tailBuffer
	cmd.Stderr = &tail
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("agent CLI not found: %s", argv[0])
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		onLine(sc.Text())
	}
	_, _ = io.Copy(io.Discard, out)
	err = cmd.Wait()
	return tail.String(), err
}

// tailBuffer keeps the last 4 KB written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// measurer runs one model once: read-only, through the CLI's one-shot mode, in
// its own process and its own empty temp folder, with a per-model timeout. It
// keeps each model's raw usage so finished runs can be saved and exported.
type measurer struct {
	spec    agents.Spec
	binary  string
	owner   string
	timeout time.Duration

	mu    sync.Mutex
	turns map[string]usage.Usage
}

func newMeasurer(spec agents.Spec, binary string) *measurer {
	return &measurer{spec: spec, binary: binary, owner: setupOwner(), timeout: bench.CompareTimeout, turns: map[string]usage.Usage{}}
}

// usageOf is the last usage model reported, nil when none.
func (m *measurer) usageOf(model string) *usage.Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.turns[model]; ok {
		return &u
	}
	return nil
}

func (m *measurer) run(ctx context.Context, req bench.Request) bench.Measured {
	stage := func(e string) {
		if req.Stage != nil {
			req.Stage(e)
		}
	}
	t0 := time.Now()
	out := bench.Measured{Model: req.Model}
	dir, err := os.MkdirTemp("", "meetstream-measure-")
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer os.RemoveAll(dir)
	turn := make([]map[string]any, len(sampleTurn))
	for i, msg := range sampleTurn {
		turn[i] = map[string]any{"speaker": strings.ReplaceAll(msg["speaker"].(string), "{owner}", m.owner), "text": msg["text"]}
	}
	prompt := prompts.System(m.owner, "Be brief.", "readonly", "") + "\n\n" + prompts.Turn(turn)
	argv, input := drivers.OneShotArgs(m.spec, m.binary, prompt, "readonly", req.Model, req.Effort, false)
	report := ""
	if flag := usage.UsageFileFlag[m.spec.Name]; flag != "" {
		report = filepath.Join(dir, "usage.json")
		argv = append(argv, flag, report)
	}
	runCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	var lines []string
	var answer strings.Builder
	final, said := "", ""
	gotLine := false
	stderr, runErr := benchExec(runCtx, argv, dir, input, drivers.Env(kitconfig.Config{Agent: m.spec}, req.Model, ""), func(line string) {
		if !gotLine {
			gotLine = true
			stage("output")
		}
		lines = append(lines, line)
		p := parsers.Parse(m.spec.Parser, line)
		if p.Delta != "" {
			if answer.Len() == 0 {
				ms := float64(time.Since(t0).Microseconds()) / 1000
				out.FirstOutputMS = &ms
				stage("first_delta")
			}
			answer.WriteString(p.Delta)
		}
		if p.HasFinal {
			final = p.Final
		}
		if p.HasError {
			said = p.Error
		}
	})
	stage("done")
	out.Seconds = time.Since(t0).Seconds()
	text := strings.TrimSpace(answer.String())
	if text == "" {
		text = strings.TrimSpace(final)
	}
	switch {
	case ctx.Err() != nil:
		out.Error = "cancelled"
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		out.Error = fmt.Sprintf("timed out after %d s", int(m.timeout.Seconds()))
	case runErr != nil && strings.HasPrefix(runErr.Error(), "agent CLI not found"):
		out.Error = runErr.Error()
	case said != "":
		out.Error = said
	case runErr != nil && text == "":
		out.Error = strings.TrimSpace(runErr.Error() + ": " + lastLine(stderr))
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			out.Error = strings.TrimSuffix(fmt.Sprintf("exited with code %d: %s", ee.ExitCode(), lastLine(stderr)), ": ")
		}
	case text == "":
		out.Error = "the CLI returned no answer"
	}
	turnUsage := usage.FromLines(lines)
	if report != "" {
		if data, err := os.ReadFile(report); err == nil {
			if u := usage.FromJSON([]byte(strings.ReplaceAll(string(data), "\n", " "))); u != nil {
				turnUsage = u
			}
		}
	}
	// An answer without token counts is not a failure: some CLIs never report them.
	if turnUsage != nil && out.Error == "" {
		m.mu.Lock()
		m.turns[req.Model] = *turnUsage
		m.mu.Unlock()
		out.Usage = benchUsage(m.spec, req.Model, *turnUsage)
	}
	return out
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// benchUsage turns the CLI's own usage report into the queue's numbers.
func benchUsage(spec agents.Spec, model string, turn usage.Usage) *bench.Usage {
	hour := usage.ProjectHour(turn, nil)
	cost, _ := estimates.HourCost(spec, model, &turn)
	return &bench.Usage{Input: int(turn.Input), Output: int(turn.Output), TokensPerHour: int(hour.Total()), CostPerHour: cost,
		Free: pricing.PlanKind(spec.Name, model, pricing.PlanOptions{}) == pricing.Free}
}

// runBenchLive shows the live table while the queue runs (Esc stops and keeps
// what finished); interrupted is ctrl+c. Tests swap it.
var runBenchLive = func(ctx context.Context, q *bench.Queue) (jobs []bench.Job, interrupted bool) {
	final, err := tea.NewProgram(bench.NewModel(ctx, q)).Run()
	if err != nil {
		q.Stop()
		return q.Snapshot(), false
	}
	return final.(bench.Model).Jobs(), final.(bench.Model).Interrupted()
}

// benchLiveAllowed is whether the live table can show; tests swap it.
var benchLiveAllowed = func() bool { return !g.JSON && ui.Interactive() }

// runBenchQueue measures models a few at a time: the live table at a terminal,
// else one plain line per finished model. Each finished measurement is saved
// for the model picker (estimates.Save, serialized there). interrupted is
// ctrl+c (SIGINT, or the key in the live table): what finished is kept.
func runBenchQueue(ctx context.Context, spec agents.Spec, ids []string, run bench.Runner, m *measurer, opts bench.Options, allowLive bool) (jobs []bench.Job, interrupted bool) {
	live := allowLive && benchLiveAllowed()
	opts.ReportsUsage = usage.ReportsUsage(spec.Name)
	var printing sync.Mutex
	opts.OnChange = func(j bench.Job) {
		if j.State == bench.Done && j.Measured != nil && m != nil {
			_ = estimates.Save(spec.Name, j.Model, estimates.Measurement{Usage: m.usageOf(j.Model), Seconds: j.Measured.Seconds,
				FirstOutputMS: j.Measured.FirstOutputMS, Error: j.Measured.Error})
		}
		if live || g.JSON || j.State == bench.Running {
			return
		}
		if line, ok := bench.PlainLine(spec.Label, j); ok {
			printing.Lock()
			fmt.Fprintln(ui.Out, line)
			printing.Unlock()
		}
	}
	q := bench.NewQueue(ids, run, opts)
	if live {
		return runBenchLive(ctx, q)
	}
	if !g.JSON {
		ui.Note(fmt.Sprintf("Measuring %d model(s), %d at a time.", len(ids), q.Workers()))
	}
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	jobs = q.Run(sigCtx)
	interrupted = sigCtx.Err() != nil && ctx.Err() == nil
	stopped := 0
	for _, j := range jobs {
		if j.State == bench.Cancelled {
			stopped++
		}
	}
	if stopped > 0 && !g.JSON {
		ui.Note(fmt.Sprintf("Stopped: %d model(s) not measured.", stopped))
	}
	return jobs, interrupted
}

// benchRunner is how the bench command runs a model; tests may swap it.
var benchRunner = func(m *measurer) bench.Runner { return m.run }

type benchResult struct {
	Model         string   `json:"model"`
	State         string   `json:"state"`
	InputTokens   *int     `json:"input_tokens"`
	OutputTokens  *int     `json:"output_tokens"`
	TokensPerHour *int     `json:"tokens_per_hour"`
	CostPerHour   *float64 `json:"cost_per_hour_usd"`
	Free          bool     `json:"free"`
	Seconds       float64  `json:"seconds"`
	FirstOutputMS *float64 `json:"first_output_ms"`
	Error         string   `json:"error,omitempty"`
}

func newAgentBenchCmd(info BuildInfo) *cobra.Command {
	var agentName, binary, modelList, export, effort string
	var compare bool
	var parallel, maxModels int
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Measure your agent's models: tokens and cost per hour of meeting, one short real turn each",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if effort != "" && !effortPattern.MatchString(effort) {
				return usageErr("invalid --effort", "for example: low, medium, high")
			}
			if parallel < 1 {
				return usageErr("--parallel must be at least 1", "")
			}
			saved, _ := kitconfig.LoadSetup("")
			if agentName == "" && saved != nil && saved.Agent != "" && saved.Command == "" {
				agentName = saved.Agent
			}
			var choice *agentChoice
			for _, c := range detectAgents() {
				if (agentName == "" && c.installed) || c.name == agentName {
					c := c
					choice = &c
					break
				}
			}
			if choice == nil {
				if agentName == "" {
					return usageErr("no supported agent CLI is installed", "run: meetstream setup")
				}
				return usageErr("unknown agent "+agentName, "choose: "+strings.Join(agentOrder, ", "))
			}
			spec := agents.All[choice.name]
			path := choice.binary
			if binary != "" {
				path, _ = resolveAgentBinary(spec, binary)
			}
			if path == "" {
				return &ExitError{Code: 2, Message: spec.Label + " is not installed", Hint: spec.InstallHint}
			}
			if !usage.ReportsUsage(spec.Name) {
				return &ExitError{Code: 2, Message: spec.Label + " doesn't report token usage, so its models can't be measured",
					Hint: "measuring works with Claude Code, Codex, Antigravity and Hermes"}
			}
			savedModel := ""
			if saved != nil && saved.Agent == spec.Name {
				savedModel = saved.Model
				if effort == "" {
					effort = saved.Effort
				}
			}
			var ids []string
			for _, m := range strings.Split(modelList, ",") {
				if m = strings.TrimSpace(m); m != "" {
					ids = append(ids, m)
				}
			}
			if len(ids) == 0 && compare {
				stop := func() {}
				if !g.JSON {
					stop = spin("Asking " + spec.Label + " which models it offers")
				}
				listed := setupCatalog(spec, path)
				stop()
				def := orDefault(savedModel, listed.DefaultModel)
				if def != "" {
					ids = append(ids, def)
				}
				for _, m := range listed.Models {
					if m.ID != def {
						ids = append(ids, m.ID)
					}
				}
				ids = ids[:min(len(ids), max(1, maxModels))]
			}
			if len(ids) == 0 {
				ids = []string{savedModel} // what meetings use; "" is the CLI's default
			}
			names := make([]string, len(ids))
			for i, m := range ids {
				names[i] = orDefault(m, "default")
			}
			if !g.JSON {
				ui.Header(info.Version, "Measure your agent's models")
				ui.Note(fmt.Sprintf("Comparing %d model(s) of %s: %s", len(ids), spec.Label, strings.Join(names, ", ")))
			}
			if bench.NeedsConfirm(len(ids)) && !g.JSON && !g.NoInput && ui.Interactive() {
				warning, question := bench.ConfirmText(len(ids))
				ui.Warn(warning)
				res, i, err := askStep(question, fmt.Sprintf("Yes, measure all %d", len(ids)), "No")
				if err != nil {
					return err
				}
				if res != menuChosen || i != 0 {
					return &ExitError{Code: 130, Message: "bench cancelled"}
				}
			}
			m := newMeasurer(spec, path)
			jobs, interrupted := runBenchQueue(cmd.Context(), spec, ids, benchRunner(m), m, bench.Options{Effort: effort, Parallel: parallel}, true)
			results := bench.Results(jobs)

			exported, total := 0, 0
			if export != "" {
				stamp := time.Now().UTC().Format(time.RFC3339)
				var entries []estimates.Entry
				for _, r := range results {
					if u := m.usageOf(r.Model); u != nil && r.Error == "" {
						entries = append(entries, estimates.NewEntry(spec.Name, r.Model, *u, r.FirstOutputMS, stamp))
					}
				}
				var err error
				if total, err = estimates.Export(export, entries); err != nil {
					return &ExitError{Code: 1, Message: "could not write " + export + ": " + err.Error()}
				}
				exported = len(entries)
			}
			measured := false
			stopped := 0
			rows := make([]benchResult, len(jobs))
			for i, j := range jobs {
				r := benchResult{Model: j.Model, State: string(j.State), Error: j.Reason}
				if j.Measured != nil {
					r.Seconds, r.FirstOutputMS = j.Measured.Seconds, j.Measured.FirstOutputMS
					if r.Error == "" {
						r.Error = j.Measured.Error
					}
					if u := j.Measured.Usage; u != nil {
						in, outT, hour := u.Input, u.Output, u.TokensPerHour
						r.InputTokens, r.OutputTokens, r.TokensPerHour, r.CostPerHour, r.Free = &in, &outT, &hour, u.CostPerHour, u.Free
						measured = true
					}
				}
				if j.State == bench.Cancelled {
					stopped++
				}
				rows[i] = r
			}
			doc := map[string]any{"agent": spec.Name, "effort": effort, "results": rows, "stopped": stopped}
			if export != "" {
				doc["export"] = map[string]any{"path": export, "written": exported, "models_in_file": total}
			}
			// The results are the one JSON document: a failure goes into it, and
			// the error after it is silent there.
			var failed *ExitError
			switch {
			case interrupted:
				failed = &ExitError{Code: 130, Message: "bench interrupted; finished results are kept"}
			case !measured:
				failed = &ExitError{Code: 1, Message: "no model reported its token usage", Hint: "check the errors above; sign in to the CLI if it asks"}
			}
			if failed != nil {
				e := map[string]any{"message": failed.Message, "exit_code": failed.Code}
				if failed.Hint != "" {
					e["hint"] = failed.Hint
				}
				doc["error"] = e
				failed.Silent = g.JSON
			}
			if err := emit(doc, func() {
				fmt.Fprintln(ui.Out)
				for _, line := range bench.ChartLines(results, bench.ChartOptions{Selected: savedModel}) {
					fmt.Fprintln(ui.Out, line)
				}
				if export != "" {
					fmt.Fprintln(ui.Out)
					ui.OK(fmt.Sprintf("Wrote %d measurement(s) to %s (%d model(s) in the file).", exported, export, total))
				}
			}); err != nil {
				return err
			}
			if failed != nil {
				return failed
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&agentName, "agent", "", "agent CLI: claude, codex, hermes, agy (default: your setup, or the first installed)")
	f.StringVar(&binary, "binary", "", "path to the agent CLI")
	f.BoolVar(&compare, "compare", false, "measure the models the CLI offers (default: only the model your setup uses)")
	f.StringVar(&modelList, "models", "", "comma-separated model ids to measure")
	f.IntVar(&parallel, "parallel", bench.DefaultParallel, "models measured at once")
	f.IntVar(&maxModels, "max-models", bench.DefaultTicked, "with --compare: at most this many models")
	f.StringVar(&export, "export", "", "write or merge the measurements into a model-estimates.json file (nothing is uploaded)")
	f.StringVar(&effort, "effort", "", "reasoning effort for every run, where the CLI supports it")
	return cmd
}

// resolveAgentBinary finds an agent CLI: the override as given, else PATH, else
// the folders installers use when PATH does not have them yet.
func resolveAgentBinary(spec agents.Spec, override string) (string, bool) {
	return checks.ResolveBinary(agentEnv(), spec, override)
}
