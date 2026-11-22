// Package bench compares models by token use: one real, short meeting turn per
// model, run a few at a time (the kit's compare.py). The caller runs each model
// in its own process and temp folder with a per-model timeout (a Runner) and
// fills the usage and cost it reports (Usage), so this package needs neither a
// price table nor a CLI.
package bench

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sync"
	"time"
)

const (
	// DefaultParallel is how many models are measured at once.
	DefaultParallel = 4
	// RetryBackoff is the pause before the one retry of a transient failure.
	RetryBackoff = 3 * time.Second
	// CompareTimeout caps one model's run (the caller's Runner enforces it).
	CompareTimeout = 120 * time.Second
	// DefaultTicked is how many models setup ticks by default for "Measure live".
	DefaultTicked = 4
	// AskAbove: more models than this picked, warn and ask first (each is a real turn).
	AskAbove = 6
)

var noRetry = regexp.MustCompile(`(?i)log ?in|sign(ed)? ?in|auth|unauthori[sz]ed|forbidden|\b40[13]\b|api key|credential|` +
	`model\b.*\b(not found|unknown|invalid|not supported|not available|does not exist)|` +
	`(unknown|invalid|unsupported) model|cli not found`)
var transient = regexp.MustCompile(`(?i)timed? ?out|timeout|exited with|crash|killed|signal|rate.?limit|\b429\b|too many requests|` +
	`overloaded|\b50[234]\b|unavailable|network|connection|econnreset|socket|temporar|try again`)

// Retryable reports a transient failure worth one more try (timeout, crash,
// rate limit, network); never a sign-in or unknown-model error.
func Retryable(err string) bool {
	return err != "" && !noRetry.MatchString(err) && transient.MatchString(err)
}

// reportsUsage are the CLIs that report their own token counts, so measuring means something.
var reportsUsage = map[string]bool{"claude": true, "codex": true, "agy": true, "hermes": true}

// ReportsUsage reports whether "Measure live" should be offered for this agent.
func ReportsUsage(agent string) bool { return reportsUsage[agent] }

// NeedsConfirm reports whether measuring n models needs a yes first.
func NeedsConfirm(n int) bool { return n > AskAbove }

// ConfirmText is the warning and the question (default no) for NeedsConfirm.
func ConfirmText(n int) (warning, question string) {
	return fmt.Sprintf("%d models: each one is a real turn on your plan or API credits.", n),
		fmt.Sprintf("Measure all %d?", n)
}

// Usage is what the caller derived from the CLI's own usage report for one turn.
type Usage struct {
	Input, Output int      // tokens in the measured turn
	TokensPerHour int      // projected to an hour of meeting (the usage package's project_hour)
	CostPerHour   *float64 // that hour at API rates; nil when unknown
	Free          bool     // a free model (shown as "free" instead of a price)
}

// Measured is one model's result. Usage is nil when the CLI reported none
// (Copilot never does), which is not a failure by itself.
type Measured struct {
	Model         string
	Usage         *Usage
	Seconds       float64
	Error         string
	FirstOutputMS *float64
}

// Request is one run of one model.
type Request struct {
	Model, Effort string
	// Stage takes the run's progress events: "output" (thinking),
	// "first_delta" (first words) and "done" (answered). Others are ignored.
	Stage func(event string)
}

// Runner runs one model once. It must stop promptly when ctx is cancelled
// (kill the CLI) and return what it has, with an error such as "cancelled".
type Runner func(ctx context.Context, req Request) Measured

// State of a Job.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Retrying  State = "retrying"
	Done      State = "done"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Job is one model in the queue. Snapshot and OnChange hand out copies.
type Job struct {
	Model    string
	State    State
	Stage    string // booting, thinking, first words, answered; waiting while a retry pauses
	Retried  bool
	Started  time.Time
	Ended    time.Time
	Reason   string
	Measured *Measured
	Attempts []time.Duration
}

// Elapsed is the time since the job started (until it ended).
func (j Job) Elapsed(now time.Time) time.Duration {
	if j.Started.IsZero() {
		return 0
	}
	end := j.Ended
	if end.IsZero() {
		end = now
	}
	return end.Sub(j.Started)
}

// Finished reports a job that will not change again.
func (j Job) Finished() bool { return j.State == Done || j.State == Failed || j.State == Cancelled }

// Options for a Queue.
type Options struct {
	Effort   string
	Parallel int           // 0: DefaultParallel
	Backoff  time.Duration // 0: RetryBackoff
	// ReportsUsage: a run stopped before it reported usage counts as cancelled, not done.
	ReportsUsage bool
	// OnChange gets a copy of a job each time it changes (for logs, or to save
	// a finished measurement). It is called from worker goroutines.
	OnChange func(Job)
}

// Queue measures models with a few workers; a free worker takes the next model at once.
type Queue struct {
	run    Runner
	opts   Options
	mu     sync.Mutex
	jobs   []*Job
	ctx    context.Context
	cancel context.CancelFunc
}

// NewQueue makes a queue with one job per model, all queued.
func NewQueue(models []string, run Runner, opts Options) *Queue {
	if opts.Parallel <= 0 {
		opts.Parallel = DefaultParallel
	}
	if opts.Backoff <= 0 {
		opts.Backoff = RetryBackoff
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{run: run, opts: opts, ctx: ctx, cancel: cancel}
	for _, m := range models {
		q.jobs = append(q.jobs, &Job{Model: m, State: Queued})
	}
	return q
}

// Workers is how many models run at once.
func (q *Queue) Workers() int { return max(1, min(q.opts.Parallel, len(q.jobs))) }

// Stop cancels running work and starts nothing new; finished results are kept.
func (q *Queue) Stop() { q.cancel() }

// Stopped reports whether Stop was called (or the Run context ended).
func (q *Queue) Stopped() bool { return q.ctx.Err() != nil }

// Snapshot copies every job, for a live view.
func (q *Queue) Snapshot() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Job, len(q.jobs))
	for i, j := range q.jobs {
		out[i] = copyJob(j)
	}
	return out
}

func copyJob(j *Job) Job {
	c := *j
	c.Attempts = append([]time.Duration(nil), j.Attempts...)
	if j.Measured != nil {
		m := *j.Measured
		c.Measured = &m
	}
	return c
}

// update changes a job under the lock, then tells OnChange.
func (q *Queue) update(j *Job, change func(*Job)) {
	q.mu.Lock()
	change(j)
	c := copyJob(j)
	q.mu.Unlock()
	if q.opts.OnChange != nil {
		q.opts.OnChange(c)
	}
}

var stages = map[string]string{"output": "thinking", "first_delta": "first words", "done": "answered"}

func (q *Queue) attempt(j *Job) (m Measured) {
	ctx, cancel := context.WithCancel(q.ctx) // each run its own: ending one never cancels another
	defer cancel()
	defer func() {
		if r := recover(); r != nil { // one model's crash must not stop the others
			m = Measured{Model: j.Model, Error: fmt.Sprintf("crashed: %v", r)}
		}
	}()
	stage := func(event string) {
		name, ok := stages[event]
		if !ok {
			return
		}
		q.mu.Lock()
		skip := j.Stage == "answered" || j.Stage == name
		q.mu.Unlock()
		if !skip {
			q.update(j, func(j *Job) { j.Stage = name })
		}
	}
	return q.run(ctx, Request{Model: j.Model, Effort: q.opts.Effort, Stage: stage})
}

func (q *Queue) work(pending <-chan *Job) {
	for q.ctx.Err() == nil {
		var j *Job
		select {
		case j = <-pending:
		default:
			return
		}
		q.update(j, func(j *Job) { j.State, j.Stage, j.Started = Running, "booting", time.Now() })
		for {
			t0 := time.Now()
			m := q.attempt(j)
			took := time.Since(t0)
			q.mu.Lock()
			j.Attempts = append(j.Attempts, took)
			retried := j.Retried
			q.mu.Unlock()
			cutShort := m.Usage == nil && q.opts.ReportsUsage // killed before it reported usage
			if q.ctx.Err() != nil && (m.Error != "" || cutShort) {
				q.mu.Lock()
				j.State, j.Reason = Cancelled, "stopped"
				q.mu.Unlock()
				break
			}
			if m.Error != "" && !retried && Retryable(m.Error) {
				q.update(j, func(j *Job) { j.State, j.Retried, j.Reason, j.Stage = Retrying, true, m.Error, "waiting" })
				timer := time.NewTimer(q.opts.Backoff)
				select {
				case <-q.ctx.Done():
					timer.Stop()
				case <-timer.C:
				}
				if q.ctx.Err() != nil {
					q.mu.Lock()
					j.State, j.Reason = Cancelled, "stopped"
					q.mu.Unlock()
					break
				}
				q.update(j, func(j *Job) { j.Stage = "booting" })
				continue
			}
			q.mu.Lock()
			measured := m
			j.Measured = &measured
			j.Reason = m.Error
			if m.Error != "" {
				j.State, j.Stage = Failed, ""
			} else {
				j.State, j.Stage = Done, "answered"
			}
			q.mu.Unlock()
			break
		}
		q.update(j, func(j *Job) { j.Ended = time.Now() })
	}
}

// Run measures every model and returns the jobs when all are finished or the
// queue is stopped (by Stop or by ctx). Jobs never started end as cancelled,
// "not started". Run is called once.
func (q *Queue) Run(ctx context.Context) []Job {
	defer context.AfterFunc(ctx, q.cancel)()
	pending := make(chan *Job, len(q.jobs))
	for _, j := range q.jobs {
		pending <- j
	}
	var wg sync.WaitGroup
	for range q.Workers() {
		wg.Add(1)
		go func() { defer wg.Done(); q.work(pending) }()
	}
	wg.Wait()
	q.mu.Lock()
	for _, j := range q.jobs {
		if j.State == Queued {
			j.State, j.Reason = Cancelled, "not started"
		}
	}
	q.mu.Unlock()
	return q.Snapshot()
}

// Results turns jobs into one Measured each; an unfinished job carries its reason as the error.
func Results(jobs []Job) []Measured {
	out := make([]Measured, 0, len(jobs))
	for _, j := range jobs {
		if j.Measured != nil {
			out = append(out, *j.Measured)
			continue
		}
		reason := j.Reason
		if reason == "" {
			reason = string(Cancelled)
		}
		out = append(out, Measured{Model: j.Model, Seconds: j.Elapsed(time.Now()).Seconds(), Error: reason})
	}
	return out
}

// TimeLeft estimates when the last model finishes, from the ones done so far;
// false before any has finished.
func TimeLeft(jobs []Job, workers int, now time.Time) (time.Duration, bool) {
	var sum time.Duration
	n := 0
	for _, j := range jobs {
		if j.State == Done || j.State == Failed {
			sum += j.Elapsed(now)
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	each := sum / time.Duration(n)
	queued := 0
	var running time.Duration
	for _, j := range jobs {
		switch j.State {
		case Queued:
			queued++
		case Running, Retrying:
			running = max(running, max(0, each-j.Elapsed(now)))
		}
	}
	rounds := math.Ceil(float64(queued) / float64(max(1, workers)))
	return running + time.Duration(rounds)*each, true
}
