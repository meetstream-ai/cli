package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/meetstream-ai/cli/internal/agentkit/agents"
)

// ExecRunner is the real Runner: the command in the system temp folder, output captured.
func ExecRunner(ctx context.Context, argv []string) (string, string, error) {
	if len(argv) == 0 {
		return "", "", fmt.Errorf("no command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = os.TempDir()
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("%s did not answer in time", strings.Join(argv, " "))
	}
	return out.String(), errOut.String(), err
}

type rpcRequest struct {
	method string
	params map[string]any
}

// rpcSession starts a JSON-RPC stdio process, sends requests in order and returns
// each result (nil for a notification). An error reply or a timeout stops it.
// argv[0] is the binary; the process runs in the system temp folder.
func rpcSession(argv []string, requests []rpcRequest, timeout time.Duration, jsonrpc bool) ([]map[string]any, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = os.TempDir()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{}) // closed when stdout ends: the process exited or closed it
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
		}
		_ = cmd.Wait() // after the reads, as os/exec requires
	}()

	var mu sync.Mutex
	replies := map[int]map[string]any{}
	arrived := make(chan struct{}, 1)
	go func() {
		defer close(exited)
		reader := bufio.NewReaderSize(stdout, 1<<16)
		for {
			line, err := reader.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var msg map[string]any
				if json.Unmarshal(line, &msg) == nil {
					_, isCall := msg["method"]
					if id, ok := msg["id"].(float64); ok && !isCall {
						mu.Lock()
						replies[int(id)] = msg
						mu.Unlock()
						select {
						case arrived <- struct{}{}:
						default:
						}
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var results []map[string]any
	for index, req := range requests {
		id := index + 1
		msg := map[string]any{"id": id, "method": req.method, "params": req.params}
		notification := req.method == "initialized"
		if notification {
			delete(msg, "id")
		}
		if jsonrpc { // ACP sends it; Codex app-server omits it
			msg["jsonrpc"] = "2.0"
		}
		data, _ := json.Marshal(msg)
		if _, err := stdin.Write(append(data, '\n')); err != nil {
			return results, fmt.Errorf("%s: %w", req.method, err)
		}
		if notification { // no reply
			results = append(results, nil)
			continue
		}
		var reply map[string]any
	wait:
		for {
			mu.Lock()
			reply = replies[id]
			mu.Unlock()
			if reply != nil {
				break
			}
			select {
			case <-arrived:
			case <-exited:
				mu.Lock()
				reply = replies[id]
				mu.Unlock()
				break wait
			case <-deadline.C:
				break wait
			}
		}
		if reply == nil {
			return results, fmt.Errorf("%s did not answer within %d s", req.method, int(timeout.Seconds()))
		}
		if e, ok := reply["error"]; ok && e != nil {
			text := fmt.Sprint(e)
			if m := str(asMap(e)["message"]); m != "" {
				text = m
			}
			return results, fmt.Errorf("%s", text)
		}
		results = append(results, asMap(reply["result"]))
	}
	return results, nil
}

// ACPCatalog lists an ACP agent's models: initialize, then session/new in the temp folder.
func ACPCatalog(agent agents.Spec, binary string, timeout time.Duration) (Catalog, error) {
	init := map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "meetstream-agent-kit", "version": "models"},
	}
	argv := append([]string{binary}, agent.ACPArgs...)
	results, err := rpcSession(argv, []rpcRequest{
		{"initialize", init},
		{"session/new", map[string]any{"cwd": os.TempDir(), "mcpServers": []any{}}},
	}, timeout, true)
	if err != nil {
		return Catalog{}, err
	}
	source := strings.TrimSpace(agent.Binary + " " + strings.Join(agent.ACPArgs, " "))
	return ACPCatalogFrom(results[1], source, results[0]), nil
}

// CodexCatalog lists Codex's models over "codex app-server" (model/list).
func CodexCatalog(binary string, timeout time.Duration) (Catalog, error) {
	init := map[string]any{"clientInfo": map[string]any{"name": "meetstream-agent-kit", "title": "MeetStream Agent Kit", "version": "models"}}
	results, err := rpcSession([]string{binary, "app-server"}, []rpcRequest{
		{"initialize", init},
		{"initialized", map[string]any{}},
		{"model/list", map[string]any{}},
	}, timeout, false)
	if err != nil {
		return Catalog{}, err
	}
	return CodexCatalogFrom(results[2]), nil
}
