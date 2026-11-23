package drivers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/meetstream-ai/cli/internal/agentkit/agents"
	"github.com/meetstream-ai/cli/internal/agentkit/config"
	"github.com/meetstream-ai/cli/internal/agentkit/parsers"
	"github.com/meetstream-ai/cli/internal/agentkit/prompts"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Event struct{ Kind, Text string }
type process struct {
	cmd                         *exec.Cmd
	in                          io.WriteCloser
	events                      chan Event
	done                        chan struct{}
	stop                        chan struct{}
	once                        sync.Once
	writeMu                     sync.Mutex
	mu                          sync.Mutex
	next                        int
	waiters                     map[string]chan map[string]any
	promptID                    string
	threadID, turnID, sessionID string
	oldTurns                    map[string]bool
}

func spawn(argv []string, dir string, env []string) (*process, float64, error) {
	t := time.Now()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	prepare(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, 0, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, 0, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = in.Close()
		_ = out.Close()
		return nil, 0, err
	}
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		_ = stderr.Close()
		return nil, 0, fmt.Errorf("agent CLI could not be started")
	}
	p := &process{cmd: cmd, in: in, events: make(chan Event, 256), done: make(chan struct{}), stop: make(chan struct{}), waiters: map[string]chan map[string]any{}, oldTurns: map[string]bool{}}
	go func() { _, _ = io.Copy(io.Discard, stderr) }()
	_ = out // reader owns stdout; cmd.Stdout holds the write end, so retain reader below
	readers.Store(p, out)
	return p, float64(time.Since(t).Microseconds()) / 1000, nil
}

// Readers are removed as soon as the sole stdout reader starts.
var readers sync.Map

func (p *process) emit(e Event) {
	select {
	case p.events <- e:
	case <-p.stop:
	}
}
func (p *process) readLines(read func(string)) {
	v, _ := readers.LoadAndDelete(p)
	out := v.(io.ReadCloser)
	defer out.Close()
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), config.MaxBodyBytes)
	for scanner.Scan() {
		read(scanner.Text())
	}
	if scanner.Err() != nil {
		p.emit(Event{Kind: "error", Text: "agent output line exceeded the protocol limit"})
	}
	err := p.cmd.Wait()
	p.mu.Lock()
	for id, ch := range p.waiters {
		ch <- map[string]any{"error": map[string]any{"message": "agent process exited"}}
		delete(p.waiters, id)
	}
	p.mu.Unlock()
	code := 0
	if err != nil {
		code = -1
	}
	p.emit(Event{Kind: "exit", Text: fmt.Sprintf("the agent process exited (%d)", code)})
	close(p.done)
}
func (p *process) close() {
	p.once.Do(func() {
		close(p.stop)
		signal(p.cmd, false)
		_ = p.in.Close()
		go func() {
			exited := false
			select {
			case <-p.done:
				exited = true
			case <-time.After(2 * time.Second):
			}
			// Also after the agent exited, where its process group outlives it:
			// helpers it started may still run there.
			if !exited || killGroupAfterExit {
				signal(p.cmd, true)
			}
		}()
	})
}
func (p *process) send(m map[string]any, rpc bool) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if rpc {
		m["jsonrpc"] = "2.0"
	}
	return json.NewEncoder(p.in).Encode(m)
}
func (p *process) id() int { p.mu.Lock(); defer p.mu.Unlock(); p.next++; return p.next }
func idKey(v any) string   { return fmt.Sprint(v) }
func (p *process) request(ctx context.Context, method string, params map[string]any, rpc bool) (map[string]any, error) {
	id := p.id()
	ch := make(chan map[string]any, 1)
	key := idKey(id)
	p.mu.Lock()
	p.waiters[key] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.waiters, key); p.mu.Unlock() }()
	if err := p.send(map[string]any{"id": id, "method": method, "params": params}, rpc); err != nil {
		return nil, fmt.Errorf("agent process input closed")
	}
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%s timed out", method)
	case m := <-ch:
		if e, ok := m["error"]; ok {
			return nil, fmt.Errorf("%s", parsers.Str(parsers.Obj(e)["message"]))
		}
		return parsers.Obj(m["result"]), nil
	case <-p.done:
		return nil, fmt.Errorf("agent process exited")
	}
}
func (p *process) resolve(m map[string]any) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := idKey(m["id"])
	ch := p.waiters[key]
	if ch == nil {
		return false
	}
	delete(p.waiters, key)
	ch <- m
	return true
}
func Env(c config.Config, model, secret string) []string {
	values := map[string]string{}
	for _, v := range os.Environ() {
		k, v, _ := strings.Cut(v, "=")
		values[k] = v
	}
	values[config.MCPSecretEnv] = secret
	if c.IgnoreRules {
		for k, v := range c.Agent.IgnoreRulesEnv {
			values[k] = v
		}
	}
	if model != "" && c.Agent.ModelEnv != "" {
		_, m := agents.SplitProvider(model)
		values[c.Agent.ModelEnv] = m
	}
	out := []string{}
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	return out
}
func OneShotArgs(a agents.Spec, binary, prompt, access, model, effort string, ignore bool) ([]string, string) {
	if binary == "" {
		binary = a.Binary
	}
	argv := []string{binary}
	for _, v := range a.OneShotArgs {
		if v == "{prompt}" && a.PromptVia == "arg" {
			v = prompt
		}
		argv = append(argv, v)
	}
	if access == "full" {
		argv = append(argv, a.FullArgs...)
	} else {
		argv = append(argv, a.ReadonlyArgs...)
	}
	argv = append(argv, a.ModelArgs(model)...)
	argv = append(argv, a.EffortFlags(effort)...)
	if ignore {
		at := len(argv)
		if len(a.OneShotArgs) > 0 && !strings.HasPrefix(a.OneShotArgs[0], "-") {
			at = 2
		}
		tail := append([]string{}, argv[at:]...)
		argv = append(append(argv[:at], a.IgnoreRulesArgs...), tail...)
	}
	if a.PromptVia == "arg" {
		return argv, ""
	}
	return argv, prompt
}

// RunOnce streams without ever copying model output to operational logs.
func RunOnce(ctx context.Context, c config.Config, prompt, access, model string, emit func(Event)) float64 {
	argv, input := OneShotArgs(c.Agent, c.Binary, prompt, access, model, c.Effort, c.IgnoreRules)
	p, spawnMS, err := spawn(argv, c.Workdir, Env(c, model, ""))
	if err != nil {
		// "end" too: the turn waits for it, and without it a CLI that
		// cannot start looked like a slow one until the turn timed out.
		emit(Event{Kind: "error", Text: err.Error()})
		emit(Event{Kind: "end"})
		return 0
	}
	defer p.close()
	go p.readLines(func(line string) {
		parsed := parsers.Parse(c.Agent.Parser, line)
		if parsed.Delta != "" {
			suffix := ""
			if c.Agent.Parser == "text" {
				suffix = "\n"
			}
			p.emit(Event{Kind: "delta", Text: parsed.Delta + suffix})
		}
		if parsed.HasFinal {
			p.emit(Event{Kind: "final", Text: parsed.Final})
		}
		if parsed.HasError {
			p.emit(Event{Kind: "error", Text: parsed.Error})
		}
	})
	go func() {
		if input != "" {
			_, _ = io.WriteString(p.in, input)
		}
		_ = p.in.Close()
	}()
	var chunks strings.Builder
	final := ""
	hasFinal := false
	for {
		select {
		case <-ctx.Done():
			return spawnMS
		case e := <-p.events:
			switch e.Kind {
			case "delta":
				chunks.WriteString(e.Text)
				emit(e)
			case "final":
				final = e.Text
				hasFinal = true
			case "error":
				emit(e)
			case "exit":
				if hasFinal {
					if chunks.Len() == 0 && final != "" {
						emit(Event{Kind: "delta", Text: final})
					}
				} else {
					final = chunks.String()
				}
				if final == "" {
					emit(Event{Kind: "error", Text: e.Text})
				}
				emit(Event{Kind: "end", Text: prompts.Truncate(strings.TrimSpace(final), config.MaxAnswerChars)})
				return spawnMS
			}
		}
	}
}
