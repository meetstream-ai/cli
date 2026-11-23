package bench

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func strip(s string) string { return ansi.ReplaceAllString(s, "") }

func usage() *Usage { return &Usage{Input: 9000, Output: 20, TokensPerHour: 180000} }

func sleeper(d map[string]time.Duration, started map[string]time.Duration, mu *sync.Mutex, t0 time.Time) Runner {
	return func(ctx context.Context, r Request) Measured {
		if started != nil {
			mu.Lock()
			started[r.Model] = time.Since(t0)
			mu.Unlock()
		}
		wait, ok := d[r.Model]
		if !ok {
			wait = 300 * time.Millisecond
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return Measured{Model: r.Model, Error: "cancelled"}
		}
		return Measured{Model: r.Model, Usage: usage(), Seconds: wait.Seconds()}
	}
}

func states(jobs []Job) []State {
	out := []State{}
	for _, j := range jobs {
		out = append(out, j.State)
	}
	return out
}

func TestModelsRunAtTheSameTime(t *testing.T) {
	models := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	t0 := time.Now()
	jobs := NewQueue(models, sleeper(nil, nil, nil, t0), Options{Parallel: 4}).Run(context.Background())
	took := time.Since(t0)
	for _, s := range states(jobs) {
		if s != Done {
			t.Fatalf("states %v", states(jobs))
		}
	}
	if took > 1200*time.Millisecond { // 8 x 0.3 s one after another is 2.4 s; 4 at a time ~0.6 s
		t.Fatalf("took %s", took)
	}
}

func TestDefaultIsFourWorkers(t *testing.T) {
	if q := NewQueue([]string{"a", "b", "c", "d", "e"}, nil, Options{}); q.Workers() != 4 {
		t.Fatal(q.Workers())
	}
	if q := NewQueue([]string{"a", "b"}, nil, Options{}); q.Workers() != 2 {
		t.Fatal(q.Workers())
	}
}

func TestAFreeWorkerTakesTheNextModelAtOnce(t *testing.T) {
	started := map[string]time.Duration{}
	var mu sync.Mutex
	t0 := time.Now()
	run := sleeper(map[string]time.Duration{"slow": 3 * time.Second, "a": 100 * time.Millisecond, "b": 100 * time.Millisecond, "c": 100 * time.Millisecond}, started, &mu, t0)
	NewQueue([]string{"slow", "a", "b", "c"}, run, Options{Parallel: 2}).Run(context.Background())
	// "b" and "c" start as soon as "a"'s worker is free, long before "slow" ends (3 s).
	if started["b"] > 2*time.Second || started["c"] > 2*time.Second {
		t.Fatalf("started %v", started)
	}
}

func TestOnlyTransientFailuresAreRetried(t *testing.T) {
	cases := map[string]bool{
		"agent timed out after 120 s":          true,
		"agent exited with -9":                 true,
		"429 Too Many Requests":                true,
		"rate limit exceeded, try again":       true,
		"connection reset by peer":             true,
		"503 Service Unavailable":              true,
		"crashed: boom":                        true,
		"Please log in: run claude auth login": false,
		"401 Unauthorized":                     false,
		"model not found: gpt-9":               false,
		"Unknown model 'x'":                    false,
		"agent exited with 1: not signed in":   false,
		"agent CLI not found: agy":             false,
		"the CLI returned no answer":           false,
		"":                                     false,
	}
	for err, want := range cases {
		if Retryable(err) != want {
			t.Errorf("Retryable(%q) = %v, want %v", err, !want, want)
		}
	}
}

func flaky(errors map[string][]string, calls map[string]int, mu *sync.Mutex) Runner {
	return func(_ context.Context, r Request) Measured {
		mu.Lock()
		defer mu.Unlock()
		calls[r.Model]++
		if q := errors[r.Model]; len(q) > 0 {
			errors[r.Model] = q[1:]
			return Measured{Model: r.Model, Seconds: 0.1, Error: q[0]}
		}
		return Measured{Model: r.Model, Usage: usage(), Seconds: 0.1}
	}
}

func TestOneRetryForTransientFailuresAndNoneForSignIn(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	errors := map[string][]string{
		"flaky":      {"agent timed out after 120 s"},
		"down":       {"503 Service Unavailable", "503 Service Unavailable"},
		"signed-out": {"Please log in"},
		"unknown":    {"model not found: unknown"},
	}
	type seen struct {
		model string
		state State
	}
	var events []seen
	jobs := NewQueue([]string{"flaky", "down", "signed-out", "unknown", "fine"}, flaky(errors, calls, &mu), Options{
		Backoff:  time.Nanosecond,
		OnChange: func(j Job) { mu.Lock(); events = append(events, seen{j.Model, j.State}); mu.Unlock() },
	}).Run(context.Background())
	type st struct {
		state   State
		retried bool
	}
	got := map[string]st{}
	for _, j := range jobs {
		got[j.Model] = st{j.State, j.Retried}
	}
	want := map[string]st{"flaky": {Done, true}, "down": {Failed, true}, "signed-out": {Failed, false}, "unknown": {Failed, false}, "fine": {Done, false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if !reflect.DeepEqual(calls, map[string]int{"flaky": 2, "down": 2, "signed-out": 1, "unknown": 1, "fine": 1}) {
		t.Fatalf("calls %v", calls)
	}
	found := false
	for _, e := range events {
		found = found || e == seen{"flaky", Retrying}
	}
	if !found {
		t.Fatal("the table never saw the retry")
	}
	for _, j := range jobs {
		if j.Model == "down" && j.Reason != "503 Service Unavailable" {
			t.Fatalf("reason %q", j.Reason)
		}
	}
}

func TestStopKeepsFinishedResultsAndCancelsTheRest(t *testing.T) {
	run := func(ctx context.Context, r Request) Measured {
		if r.Model == "m0" {
			return Measured{Model: r.Model, Usage: usage(), Seconds: 0.1}
		}
		select { // a real runner kills the CLI when ctx ends
		case <-ctx.Done():
			return Measured{Model: r.Model, Seconds: 0.1, Error: "cancelled"}
		case <-time.After(5 * time.Second):
			return Measured{Model: r.Model, Seconds: 0.1}
		}
	}
	q := NewQueue([]string{"m0", "m1", "m2", "m3"}, run, Options{Parallel: 2, ReportsUsage: true})
	time.AfterFunc(300*time.Millisecond, q.Stop)
	t0 := time.Now()
	jobs := q.Run(context.Background())
	if time.Since(t0) > 2*time.Second {
		t.Fatal("the running model was waited for, not stopped")
	}
	if !reflect.DeepEqual(states(jobs), []State{Done, Cancelled, Cancelled, Cancelled}) {
		t.Fatalf("states %v", states(jobs))
	}
	if jobs[0].Measured == nil || jobs[0].Measured.Usage == nil {
		t.Fatal("finished result lost")
	}
	if jobs[3].Reason != "not started" {
		t.Fatalf("reason %q", jobs[3].Reason)
	}
	var errs []string
	for _, m := range Results(jobs)[1:] {
		errs = append(errs, m.Error)
	}
	if !reflect.DeepEqual(errs, []string{"stopped", "stopped", "not started"}) {
		t.Fatalf("errors %v", errs)
	}
}

func TestStopDuringTheRetryPause(t *testing.T) {
	run := func(context.Context, Request) Measured { return Measured{Model: "x", Error: "503 Service Unavailable"} }
	q := NewQueue([]string{"x"}, run, Options{Backoff: time.Minute})
	time.AfterFunc(200*time.Millisecond, q.Stop)
	t0 := time.Now()
	jobs := q.Run(context.Background())
	if time.Since(t0) > 2*time.Second || jobs[0].State != Cancelled || jobs[0].Reason != "stopped" {
		t.Fatalf("%v %q after %s", jobs[0].State, jobs[0].Reason, time.Since(t0))
	}
}

func TestACrashInOneModelDoesNotStopTheOthers(t *testing.T) {
	run := func(_ context.Context, r Request) Measured {
		if r.Model == "bad" {
			panic("boom")
		}
		return Measured{Model: r.Model, Usage: usage(), Seconds: 0.1}
	}
	jobs := NewQueue([]string{"bad", "good"}, run, Options{Backoff: time.Nanosecond}).Run(context.Background())
	if jobs[0].State != Failed || jobs[1].State != Done {
		t.Fatalf("states %v", states(jobs))
	}
	if jobs[0].Reason != "crashed: boom" || !jobs[0].Retried {
		t.Fatalf("reason %q retried %v (a crash is transient: one retry)", jobs[0].Reason, jobs[0].Retried)
	}
}

func TestStagesReachTheJob(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	run := func(_ context.Context, r Request) Measured {
		r.Stage("output")
		r.Stage("first_delta")
		r.Stage("done")
		r.Stage("output") // after answered: ignored
		r.Stage("whatever")
		return Measured{Model: r.Model, Usage: usage()}
	}
	NewQueue([]string{"m"}, run, Options{OnChange: func(j Job) { mu.Lock(); seen = append(seen, j.Stage); mu.Unlock() }}).Run(context.Background())
	if !reflect.DeepEqual(seen, []string{"booting", "thinking", "first words", "answered", "answered"}) {
		t.Fatalf("stages %v", seen)
	}
}

func TestTimeLeftFromTheModelsDoneSoFar(t *testing.T) {
	base := time.Unix(0, 0)
	at := func(s float64) time.Time { return base.Add(time.Duration(s * float64(time.Second))) }
	now := at(100)
	done := Job{Model: "a", State: Done, Started: at(80), Ended: at(90)} // 10 s each
	running := Job{Model: "b", State: Running, Started: at(96)}
	jobs := []Job{done, running}
	for range 4 {
		jobs = append(jobs, Job{State: Queued})
	}
	if _, ok := TimeLeft([]Job{{State: Queued}}, 2, now); ok {
		t.Fatal("estimate before any model finished")
	}
	if left, _ := TimeLeft(jobs, 2, now); left != 26*time.Second {
		t.Fatalf("left %s, want 6 s + 2 x 10 s", left)
	}
}

func TestTheLiveTableShowsEveryState(t *testing.T) {
	cost := 0.42
	start := time.Unix(1, 0)
	now := time.Unix(6, 0)
	jobs := []Job{
		{Model: "done-model", State: Done, Started: start, Ended: time.Unix(5, 0), Measured: &Measured{Model: "sonnet", Usage: &Usage{Input: 9000, Output: 20, TokensPerHour: 180000, CostPerHour: &cost}, Seconds: 4}},
		{Model: "failing", State: Failed, Started: start, Ended: time.Unix(2, 0), Reason: "401 Unauthorized"},
		{Model: "flaky", State: Retrying, Stage: "booting", Started: start, Retried: true, Reason: "timed out"},
		{Model: "running", State: Running, Stage: "first words", Started: start},
		{Model: "waiting", State: Queued},
	}
	text := strip(Table(jobs, 4, "⠋", now, false))
	for _, want := range []string{"✓  done-model", "180k/h", "≈ $0.42/h", "✗  failing", "401 Unauthorized",
		"retrying (1/1) · booting · timed out", "first words", "queued", "2/5 done · 2 running", "Esc stop"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	finished := []Job{{Model: "a", State: Done}, {Model: "b", State: Failed}, {Model: "c", State: Cancelled}}
	if text := strip(Table(finished, 4, "", now, false)); !strings.Contains(text, "1 answered · 1 failed · 1 stopped") || strings.Contains(text, "Esc") {
		t.Fatalf("finished table:\n%s", text)
	}
}

func col(line, word string) int { return len([]rune(line[:strings.Index(line, word)])) }

func TestTableColumnsLineUpWithColour(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(saved)
	jobs := []Job{{Model: "a", State: Done, Measured: &Measured{Usage: &Usage{TokensPerHour: 5}}}, {Model: "much-longer-model", State: Queued}}
	coloured := Table(jobs, 2, "", time.Now(), false)
	if !ansi.MatchString(coloured) {
		t.Fatal("no colour in the table; the test would prove nothing")
	}
	lines := strings.Split(strip(coloured), "\n")
	// the status column starts at the same cell on both rows, whatever ANSI codes surround it
	if col(lines[0], "answered") != col(lines[1], "queued") {
		t.Fatalf("columns do not line up:\n%s\n%s", lines[0], lines[1])
	}
	if got := Clip("\x1b[31mabcdef\x1b[0m", 10); got != "\x1b[31mabcdef\x1b[0m" {
		t.Fatalf("Clip counted ANSI codes: %q", got)
	}
	if Clip("abcdef", 4) != "abc…" {
		t.Fatal(Clip("abcdef", 4))
	}
}

func TestHourTextAndPlainLines(t *testing.T) {
	cost := 0.004
	if _, c := HourText(&Measured{Usage: &Usage{TokensPerHour: 1, CostPerHour: &cost}}); c != "≈ <$0.01/h" {
		t.Fatal(c)
	}
	if _, c := HourText(&Measured{Usage: &Usage{Free: true}}); c != "free" {
		t.Fatal(c)
	}
	if tok, _ := HourText(&Measured{}); tok != "usage n/a" {
		t.Fatal(tok)
	}
	line, ok := PlainLine("Copilot CLI", Job{Model: "auto", State: Done, Measured: &Measured{Seconds: 2}})
	if !ok || line != "  ✓ auto answered in 2.0 s · Copilot CLI doesn't report token usage" {
		t.Fatal(line)
	}
	if line, ok := PlainLine("x", Job{Model: "m", State: Retrying, Stage: "waiting", Reason: "timed out"}); !ok || line != "  ↻ m: timed out; retrying once" {
		t.Fatal(line)
	}
	if _, ok := PlainLine("x", Job{State: Running}); ok {
		t.Fatal("running is not a line")
	}
}

func TestAskBeforeMeasuringMoreThanSix(t *testing.T) {
	if NeedsConfirm(6) || !NeedsConfirm(7) {
		t.Fatal("ask only above 6")
	}
	warn, q := ConfirmText(7)
	if q != "Measure all 7?" || !strings.Contains(warn, "7 models") {
		t.Fatal(warn, q)
	}
	for agent, want := range map[string]bool{"claude": true, "codex": true, "agy": true, "hermes": true, "copilot": false, "muse": false, "openclaw": false} {
		if ReportsUsage(agent) != want {
			t.Errorf("ReportsUsage(%s) != %v", agent, want)
		}
	}
}

func TestChartLines(t *testing.T) {
	cost := 0.17
	lines := ChartLines([]Measured{
		{Model: "sonnet", Usage: &Usage{TokensPerHour: 200000, CostPerHour: &cost}, Seconds: 4},
		{Model: "haiku", Usage: &Usage{TokensPerHour: 100000}, Seconds: 2},
		{Model: "broken", Error: "401"},
	}, ChartOptions{Selected: "sonnet", Width: 8, TermWidth: 80})
	text := strip(strings.Join(lines, "\n"))
	for _, want := range []string{"Estimated tokens for one hour of meeting  (an estimate)", " ★ sonnet  ████████    200k tok/h   4 s  ≈ $0.17/h",
		"   haiku   ████        100k tok/h   2 s  —", "Assumes 20 answered turns"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "broken") {
		t.Fatal("failed models are not charted")
	}
	if got := ChartLines([]Measured{{Model: "auto"}}, ChartOptions{}); !strings.Contains(got[0], "doesn't report token usage") {
		t.Fatal(got)
	}
	if got := ChartLines([]Measured{{Model: "x", Error: "e"}}, ChartOptions{}); !strings.Contains(got[0], "nothing to chart") {
		t.Fatal(got)
	}
	if Bar(1, 2, 4) != "██" || Bar(1, 16, 1) != "▏" || Bar(0, 1, 4) != "" {
		t.Fatal(Bar(1, 2, 4), Bar(1, 16, 1))
	}
}

func TestEscStopsTheLiveTableAndKeepsResults(t *testing.T) {
	run := func(ctx context.Context, r Request) Measured {
		if r.Model == "fast" {
			return Measured{Model: r.Model, Usage: usage()}
		}
		<-ctx.Done()
		return Measured{Model: r.Model, Error: "cancelled"}
	}
	q := NewQueue([]string{"fast", "slow"}, run, Options{Parallel: 2, ReportsUsage: true})
	var m tea.Model = NewModel(context.Background(), q)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init starts nothing")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- doneMsg{q.Run(context.Background())} }() // what Init's command does
	time.Sleep(100 * time.Millisecond)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.(Model).Stopped() || !strings.Contains(strip(m.View()), "Stopping") {
		t.Fatalf("Esc did not stop:\n%s", m.View())
	}
	var quit tea.Cmd
	select {
	case msg := <-done:
		m, quit = m.Update(msg)
	case <-time.After(3 * time.Second):
		t.Fatal("queue did not stop")
	}
	if quit == nil {
		t.Fatal("model does not quit when the queue is done")
	}
	jobs := m.(Model).Jobs()
	if jobs[0].State != Done || jobs[1].State != Cancelled {
		t.Fatalf("states %v", states(jobs))
	}
	if text := strip(m.View()); !strings.Contains(text, "1 answered · 1 stopped") {
		t.Fatalf("final view:\n%s", text)
	}
}
